package service

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// Image2Alternative is a customer-safe, normalized suggestion. It deliberately
// has no channel ID, supplier name, base URL, or backend filter reason.
type Image2Alternative struct {
	Operation string `json:"operation"`
	Size      string `json:"size"`
	Quality   string `json:"quality"`
	N         uint   `json:"n"`
}

// Image2RoutingExplanation is derived from all configured declarations and
// tested fallback capabilities. It is used for the 422/503 response contract;
// backend-only channel decisions remain on Image2SmartRouter.decisions.
type Image2RoutingExplanation struct {
	Request               Image2RequestCapability
	UnsupportedDimensions []string
	SupportedValues       map[string]interface{}
	Alternatives          []Image2Alternative
	HasConfigured         bool
	HasTemporary          bool
}

// Image2ErrorMetadata is the structured, customer-safe metadata attached to
// an Image2 pre-route error. The same normalized request values are repeated in
// the message so clients that do not parse metadata still receive an actionable
// explanation.
type Image2ErrorMetadata struct {
	Operation             string                 `json:"operation"`
	Resolution            string                 `json:"resolution"`
	Size                  string                 `json:"size"`
	Quality               string                 `json:"quality"`
	N                     uint                   `json:"n"`
	UnsupportedDimensions []string               `json:"unsupported_dimensions"`
	SupportedValues       map[string]interface{} `json:"supported_values"`
	Alternatives          []Image2Alternative    `json:"alternatives"`
	SafeAlternatives      []Image2Alternative    `json:"safe_alternatives"`
	RequestID             string                 `json:"request_id"`
	UpstreamCalled        bool                   `json:"upstream_called"`
	Charged               bool                   `json:"charged"`
}

type image2CapabilityOption struct {
	alternative Image2Alternative
	resolution  string
	exactSize   bool
	// maxN is the declared upper bound for this option. Zero means the
	// declaration does not impose a limit (the capability schema's meaning).
	maxN uint
}

const image2ExplanationValueLimit = 64

func (r *Image2SmartRouter) Explain() Image2RoutingExplanation {
	if r == nil {
		return Image2RoutingExplanation{}
	}
	request := r.request
	request.Size = canonicalImage2Size(request.Size)
	if strings.TrimSpace(request.Quality) == "" {
		request.Quality = "auto"
	}
	if request.N == 0 {
		request.N = 1
	}
	options := r.options
	if len(options) == 0 {
		return Image2RoutingExplanation{
			Request:               request,
			UnsupportedDimensions: []string{"operation", "size", "quality", "n"},
			SupportedValues:       emptyImage2SupportedValues(),
			Alternatives:          []Image2Alternative{},
			HasConfigured:         r.configured,
			HasTemporary:          r.temporary,
		}
	}

	operationSupported := false
	sizeSupported := false
	qualitySupported := false
	nSupported := false
	for _, option := range options {
		if strings.EqualFold(option.alternative.Operation, request.Operation) {
			operationSupported = true
		}
		if option.resolution == request.Resolution &&
			(!option.exactSize || canonicalImage2Size(option.alternative.Size) == canonicalImage2Size(request.Size)) {
			sizeSupported = true
		}
		if strings.EqualFold(option.alternative.Quality, request.Quality) {
			qualitySupported = true
		}
		if option.maxN == 0 || option.maxN >= request.N {
			nSupported = true
		}
	}

	unsupported := make([]string, 0, 4)
	if !operationSupported {
		unsupported = append(unsupported, "operation")
	}
	if !sizeSupported {
		unsupported = append(unsupported, "size")
	}
	if !qualitySupported {
		unsupported = append(unsupported, "quality")
	}
	if !nSupported {
		unsupported = append(unsupported, "n")
	}
	if len(unsupported) == 0 && len(r.candidates) == 0 && !r.temporary {
		// Each individual value appears in at least one declaration, but no
		// declaration supports the complete tuple. Report the dimensions that
		// differ from the nearest safe alternatives instead of returning an empty
		// explanation. This is important for profile-based declarations where
		// cross-products are intentionally forbidden.
		unsupported = image2NearestMismatchDimensions(request, options)
	}
	if len(unsupported) == 0 && len(r.candidates) == 0 {
		// A compatible option exists, but every option is currently disabled.
		// Keep the explanation actionable without mislabeling it unsupported.
		unsupported = []string{}
	}

	return Image2RoutingExplanation{
		Request:               request,
		UnsupportedDimensions: unsupported,
		SupportedValues:       image2SupportedValues(options),
		Alternatives:          image2Alternatives(options),
		HasConfigured:         r.configured,
		HasTemporary:          r.temporary,
	}
}

func image2NearestMismatchDimensions(request Image2RequestCapability, options []image2CapabilityOption) []string {
	if len(options) == 0 {
		return []string{"operation", "size", "quality", "n"}
	}
	best := 5
	seen := make(map[string]struct{})
	for _, option := range options {
		mismatches := make([]string, 0, 4)
		if !strings.EqualFold(option.alternative.Operation, request.Operation) {
			mismatches = append(mismatches, "operation")
		}
		if option.resolution != request.Resolution ||
			(option.exactSize && canonicalImage2Size(option.alternative.Size) != canonicalImage2Size(request.Size)) {
			mismatches = append(mismatches, "size")
		}
		if !strings.EqualFold(option.alternative.Quality, request.Quality) {
			mismatches = append(mismatches, "quality")
		}
		if option.maxN != 0 && option.maxN < request.N {
			mismatches = append(mismatches, "n")
		}
		if len(mismatches) < best {
			best = len(mismatches)
			seen = make(map[string]struct{}, len(mismatches))
		}
		if len(mismatches) == best {
			for _, mismatch := range mismatches {
				seen[mismatch] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for _, dimension := range []string{"operation", "size", "quality", "n"} {
		if _, ok := seen[dimension]; ok {
			result = append(result, dimension)
		}
	}
	if len(result) == 0 {
		return []string{"operation", "size", "quality", "n"}
	}
	return result
}

func emptyImage2SupportedValues() map[string]interface{} {
	return map[string]interface{}{
		"operation":  []string{},
		"resolution": []string{},
		"size":       []string{},
		"quality":    []string{},
		"n":          []uint{},
	}
}

func image2SupportedValues(options []image2CapabilityOption) map[string]interface{} {
	operations := make(map[string]struct{})
	resolutions := make(map[string]struct{})
	sizes := make(map[string]struct{})
	qualities := make(map[string]struct{})
	nValues := make(map[uint]struct{})
	for _, option := range options {
		operations[option.alternative.Operation] = struct{}{}
		resolutions[option.resolution] = struct{}{}
		sizes[option.alternative.Size] = struct{}{}
		qualities[option.alternative.Quality] = struct{}{}
		if option.maxN == 0 {
			// Zero is the schema's unlimited sentinel. Keep the customer-facing
			// list bounded and safe: n=1 is always a valid non-fan-out choice.
			nValues[1] = struct{}{}
		} else {
			// Expose finite declared values without allowing an accidentally huge
			// declaration to create an unbounded response. Larger values remain
			// actionable through the safe alternatives and the error message.
			for n := uint(1); n <= option.maxN && n <= 16; n++ {
				nValues[n] = struct{}{}
			}
		}
	}
	operationList := sortedStringSet(operations)
	resolutionList := sortedStringSet(resolutions)
	sizeList := sortedStringSet(sizes)
	qualityList := sortedStringSet(qualities)
	nList := make([]uint, 0, len(nValues))
	for n := range nValues {
		nList = append(nList, n)
	}
	sort.Slice(nList, func(i, j int) bool { return nList[i] < nList[j] })
	return map[string]interface{}{
		"operation":  operationList,
		"resolution": resolutionList,
		"size":       sizeList,
		"quality":    qualityList,
		"n":          nList,
	}
}

func sortedStringSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	if len(result) > image2ExplanationValueLimit {
		result = result[:image2ExplanationValueLimit]
	}
	return result
}

func image2Alternatives(options []image2CapabilityOption) []Image2Alternative {
	seen := make(map[string]struct{}, len(options))
	result := make([]Image2Alternative, 0, len(options))
	for _, option := range options {
		alternative := option.alternative
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%d", alternative.Operation, alternative.Size, alternative.Quality, alternative.N)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, alternative)
		if len(result) >= image2ExplanationValueLimit {
			break
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left := fmt.Sprintf("%s/%s/%s/%d", result[i].Operation, result[i].Size, result[i].Quality, result[i].N)
		right := fmt.Sprintf("%s/%s/%s/%d", result[j].Operation, result[j].Size, result[j].Quality, result[j].N)
		return left < right
	})
	return result
}

func image2OptionsForCapability(capability *dto.Image2ChannelCapability) []image2CapabilityOption {
	if capability == nil || !capability.Enabled {
		return nil
	}
	options := make([]image2CapabilityOption, 0)
	if len(capability.Profiles) > 0 {
		for _, profile := range capability.Profiles {
			quality := strings.ToLower(strings.TrimSpace(profile.Quality))
			if quality == "default" || quality == "" {
				quality = "auto"
			}
			resolution := strings.ToLower(strings.TrimSpace(profile.Resolution))
			size := canonicalImage2Size(profile.Size)
			if profile.Size == "" {
				size = canonicalImage2Size(resolution)
			}
			options = append(options, image2CapabilityOption{
				alternative: Image2Alternative{Operation: strings.ToLower(strings.TrimSpace(profile.Operation)), Size: size, Quality: quality, N: 1},
				resolution:  resolution,
				exactSize:   profile.Size != "",
				maxN:        profile.MaxN,
			})
		}
		return options
	}
	// image2Incompatibility treats omitted/auto quality as the provider default
	// even when an explicit standard/high allow-list is present. Include that
	// implicit default in the explanation so a size-only mismatch is not
	// incorrectly reported as a quality mismatch.
	qualities := []string{"auto"}
	for _, quality := range capability.Qualities {
		quality = strings.ToLower(strings.TrimSpace(quality))
		if quality != "" && quality != "auto" {
			qualities = append(qualities, quality)
		}
	}
	// Alternatives are suggestions, not an exhaustive n contract. Keep them
	// bounded and conservative so an unbounded declaration never suggests a
	// potentially expensive fan-out.
	n := uint(1)
	for _, operation := range capability.Operations {
		operation = strings.ToLower(strings.TrimSpace(operation))
		if operation == "edits" && !capability.EditsAccepted {
			continue
		}
		for _, resolution := range capability.Resolutions {
			resolution = strings.ToLower(strings.TrimSpace(resolution))
			for _, quality := range qualities {
				options = append(options, image2CapabilityOption{
					alternative: Image2Alternative{Operation: operation, Size: canonicalImage2Size(resolution), Quality: quality, N: n},
					resolution:  resolution,
					maxN:        capability.MaxN,
				})
			}
		}
	}
	return options
}

func image2ErrorMessage(explanation Image2RoutingExplanation, temporarilyUnavailable bool) string {
	request := explanation.Request
	if temporarilyUnavailable {
		return fmt.Sprintf(
			"Image2 request is temporarily unavailable: operation=%s, resolution=%s, size=%s, quality=%s, n=%d; a configured compatible capability exists but all matching channels are temporarily unavailable; upstream_called=false; charged=false",
			request.Operation, request.Resolution, request.Size, request.Quality, request.N,
		)
	}
	dimensions := make([]string, 0, len(explanation.UnsupportedDimensions))
	for _, dimension := range explanation.UnsupportedDimensions {
		requestValue := image2RequestDimensionValue(request, dimension)
		supported := explanation.SupportedValues[dimension]
		dimensions = append(dimensions, fmt.Sprintf("%s=%s (requested %s; current supported values %s)", dimension, requestValue, requestValue, formatImage2SupportedValues(supported)))
	}
	if len(dimensions) == 0 {
		dimensions = append(dimensions, "the complete operation/size/quality/n combination")
	}
	return fmt.Sprintf(
		"unsupported Image2 configuration: %s; safe alternatives: %s; operation=%s, resolution=%s, size=%s, quality=%s, n=%d; upstream_called=false; charged=false",
		strings.Join(dimensions, "; "), formatImage2Alternatives(explanation.Alternatives), request.Operation, request.Resolution, request.Size, request.Quality, request.N,
	)
}

func image2RequestDimensionValue(request Image2RequestCapability, dimension string) string {
	switch dimension {
	case "operation":
		return request.Operation
	case "size":
		return request.Size
	case "quality":
		return request.Quality
	case "n":
		return strconv.FormatUint(uint64(request.N), 10)
	default:
		return "unknown"
	}
}

func formatImage2SupportedValues(value interface{}) string {
	return fmt.Sprintf("%v", value)
}

func formatImage2Alternatives(alternatives []Image2Alternative) string {
	if len(alternatives) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(alternatives))
	for _, alternative := range alternatives {
		parts = append(parts, fmt.Sprintf("operation=%s,size=%s,quality=%s,n=%d", alternative.Operation, alternative.Size, alternative.Quality, alternative.N))
	}
	return strings.Join(parts, " | ")
}

// NewImage2PreRouteError creates the only customer-facing errors emitted for
// an Image2 request that has not called an upstream. It is intentionally called
// before pricing and pre-consume in controller.Relay.
func NewImage2PreRouteError(c *gin.Context, info *relaycommon.RelayInfo, router *Image2SmartRouter) *types.NewAPIError {
	if router == nil {
		return nil
	}
	explanation := router.Explain()
	temporarilyUnavailable := explanation.HasTemporary && len(router.candidates) == 0 && len(explanation.UnsupportedDimensions) == 0
	statusCode := http.StatusUnprocessableEntity
	errorCode := types.ErrorCodeUnsupportedImageConfiguration
	if temporarilyUnavailable {
		statusCode = http.StatusServiceUnavailable
		errorCode = types.ErrorCodeImage2TemporarilyUnavailable
	}
	requestID := ""
	if c != nil {
		requestID = c.GetString(common.RequestIdKey)
	}
	metadata := Image2ErrorMetadata{
		Operation:             explanation.Request.Operation,
		Resolution:            explanation.Request.Resolution,
		Size:                  explanation.Request.Size,
		Quality:               explanation.Request.Quality,
		N:                     explanation.Request.N,
		UnsupportedDimensions: append([]string{}, explanation.UnsupportedDimensions...),
		SupportedValues:       explanation.SupportedValues,
		Alternatives:          append([]Image2Alternative{}, explanation.Alternatives...),
		SafeAlternatives:      append([]Image2Alternative{}, explanation.Alternatives...),
		RequestID:             requestID,
		UpstreamCalled:        false,
		Charged:               false,
	}
	return types.NewErrorWithStatusCode(
		fmt.Errorf("%s", image2ErrorMessage(explanation, temporarilyUnavailable)),
		errorCode,
		statusCode,
		types.ErrOptionWithSkipRetry(),
		types.ErrOptionWithMetadata(metadata),
	)
}

// NormalizeImage2UpstreamStatus keeps the Image2 status boundary explicit:
// only an actual upstream attempt may become 502/504. The caller is
// responsible for passing the upstream HTTP status (or the timeout marker).
func NormalizeImage2UpstreamStatus(err *types.NewAPIError, upstreamStatus int, timedOut bool) *types.NewAPIError {
	if err == nil {
		return nil
	}
	if timedOut || upstreamStatus == http.StatusGatewayTimeout || upstreamStatus == 524 {
		err.StatusCode = http.StatusGatewayTimeout
	} else if upstreamStatus >= http.StatusBadRequest && upstreamStatus < http.StatusInternalServerError {
		// Preserve upstream client/capacity semantics (especially 429). The
		// Image2 contract only rewrites malformed gateway/server failures; a
		// caller-visible 4xx must not be mislabeled as 502 or pre-route 503.
		err.StatusCode = upstreamStatus
	} else {
		err.StatusCode = http.StatusBadGateway
	}
	// Upstream adaptor errors can contain a supplier name, URL, channel hint,
	// or response body. Keep those details server-side; Image2 clients receive
	// only a bounded status-class explanation while the request ID still ties
	// the event to backend diagnostics.
	safeMessage := image2SafeUpstreamMessage(err.StatusCode, timedOut)
	err.Err = errors.New(safeMessage)
	switch relayError := err.RelayError.(type) {
	case types.OpenAIError:
		relayError.Message = safeMessage
		relayError.Metadata = nil
		err.RelayError = relayError
	case *types.OpenAIError:
		if relayError != nil {
			relayError.Message = safeMessage
			relayError.Metadata = nil
			err.RelayError = relayError
		}
	}
	return err
}

func image2SafeUpstreamMessage(statusCode int, timedOut bool) string {
	if timedOut || statusCode == http.StatusGatewayTimeout || statusCode == 524 {
		return "Image2 upstream request timed out"
	}
	if statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError {
		return fmt.Sprintf("Image2 upstream rejected the request (status %d)", statusCode)
	}
	if statusCode >= http.StatusInternalServerError {
		return "Image2 upstream returned an invalid response"
	}
	return "Image2 upstream request failed"
}

func IsImage2PreRouteError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	return err.GetErrorCode() == types.ErrorCodeUnsupportedImageConfiguration ||
		err.GetErrorCode() == types.ErrorCodeImage2TemporarilyUnavailable
}

func image2UpstreamTimeout(err error) bool {
	if err == nil {
		return false
	}
	type timeoutError interface{ Timeout() bool }
	if timeout, ok := err.(timeoutError); ok && timeout.Timeout() {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "timeout") || strings.Contains(strings.ToLower(err.Error()), "timed out")
}

// IsImage2UpstreamTimeout is kept small and exported for the relay adapter so
// it can classify a transport failure without exposing provider details to the
// customer response.
func IsImage2UpstreamTimeout(err error) bool {
	return image2UpstreamTimeout(err)
}
