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
	// CombinationUnsupported distinguishes a cross-product miss from a value
	// that is absent everywhere. It is deliberately kept out of the client
	// metadata: unsupported_dimensions and alternatives are the stable machine
	// contract, while this bit only controls the human-readable message.
	CombinationUnsupported bool
}

// Image2ErrorMetadata is the structured, customer-safe metadata attached to
// an Image2 pre-route error. The same normalized request values are repeated in
// the message so clients that do not parse metadata still receive an actionable
// explanation.
type Image2ErrorMetadata struct {
	Operation             string                 `json:"operation"`
	Resolution            string                 `json:"resolution"`
	Size                  string                 `json:"size"`
	RequestedQuality      string                 `json:"requested_quality"`
	EffectiveQuality      string                 `json:"effective_quality"`
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

const (
	image2ExplanationValueLimit = 64
	image2AlternativeLimit      = 3
)

func (r *Image2SmartRouter) Explain() Image2RoutingExplanation {
	if r == nil {
		return Image2RoutingExplanation{}
	}
	request := normalizeImage2RequestCapability(r.request)
	request.Size = canonicalImage2Size(request.Size)
	if request.N == 0 {
		request.N = 1
	}
	options := r.options
	if len(options) == 0 {
		return Image2RoutingExplanation{
			Request:               request,
			UnsupportedDimensions: []string{"operation", "size", "n"},
			SupportedValues:       emptyImage2SupportedValues(),
			Alternatives:          []Image2Alternative{},
			HasConfigured:         r.configured,
			HasTemporary:          r.temporary,
		}
	}

	// A profile is an exact tuple, not an independent allow-list. Prefer the
	// requested operation before comparing dimensions so a globally supported
	// operation is never reported as unsupported merely because another
	// operation has a closer tuple. This is the key distinction between a
	// missing marginal value and an unsupported cross-product.
	preferredOptions := image2PreferredOptions(request, options)
	nearestOptions := image2NearestOptions(request, preferredOptions)
	if len(nearestOptions) == 0 {
		nearestOptions = preferredOptions
	}
	unsupported := make([]string, 0, 4)
	if len(options) > 0 {
		unsupported = image2NearestMismatchDimensions(request, preferredOptions)
	}
	combinationUnsupported := len(r.candidates) == 0 && !r.temporary && len(options) > 0 &&
		image2AllDimensionsHaveSupport(request, options) && len(unsupported) > 0
	if len(unsupported) == 0 && len(r.candidates) == 0 {
		// A compatible option exists, but every option is currently disabled.
		// Keep the explanation actionable without mislabeling it unsupported.
		unsupported = []string{}
	}
	if len(nearestOptions) == 0 {
		nearestOptions = options
	}
	if len(nearestOptions) == 0 && len(options) == 0 {
		unsupported = []string{"operation", "size", "n"}
	}

	return Image2RoutingExplanation{
		Request:                request,
		UnsupportedDimensions:  unsupported,
		SupportedValues:        image2SupportedValues(nearestOptions),
		Alternatives:           image2Alternatives(request, options),
		HasConfigured:          r.configured,
		HasTemporary:           r.temporary,
		CombinationUnsupported: combinationUnsupported,
	}
}

func image2PreferredOptions(request Image2RequestCapability, options []image2CapabilityOption) []image2CapabilityOption {
	if len(options) == 0 {
		return nil
	}
	sameOperation := make([]image2CapabilityOption, 0, len(options))
	for _, option := range options {
		if strings.EqualFold(option.alternative.Operation, request.Operation) {
			sameOperation = append(sameOperation, option)
		}
	}
	if len(sameOperation) > 0 {
		return sameOperation
	}
	return options
}

func image2AllDimensionsHaveSupport(request Image2RequestCapability, options []image2CapabilityOption) bool {
	operationSupported := false
	sizeSupported := false
	nSupported := false
	for _, option := range options {
		if strings.EqualFold(option.alternative.Operation, request.Operation) {
			operationSupported = true
		}
		if image2OptionMatchesSize(request, option) {
			sizeSupported = true
		}
		if option.maxN == 0 || option.maxN >= request.N {
			nSupported = true
		}
	}
	return operationSupported && sizeSupported && nSupported
}

func image2OptionMismatches(request Image2RequestCapability, option image2CapabilityOption) []string {
	mismatches := make([]string, 0, 4)
	if !strings.EqualFold(option.alternative.Operation, request.Operation) {
		mismatches = append(mismatches, "operation")
	}
	if !image2OptionMatchesSize(request, option) {
		mismatches = append(mismatches, "size")
	}
	if option.maxN != 0 && option.maxN < request.N {
		mismatches = append(mismatches, "n")
	}
	return mismatches
}

func image2OptionMatchesSize(request Image2RequestCapability, option image2CapabilityOption) bool {
	return option.resolution == request.Resolution &&
		(!option.exactSize || canonicalImage2Size(option.alternative.Size) == canonicalImage2Size(request.Size))
}

func image2NearestOptions(request Image2RequestCapability, options []image2CapabilityOption) []image2CapabilityOption {
	if len(options) == 0 {
		return nil
	}
	best := 5
	nearest := make([]image2CapabilityOption, 0, len(options))
	for _, option := range options {
		mismatchCount := len(image2OptionMismatches(request, option))
		if mismatchCount < best {
			best = mismatchCount
			nearest = nearest[:0]
		}
		if mismatchCount == best {
			nearest = append(nearest, option)
		}
	}
	return nearest
}

func image2NearestMismatchDimensions(request Image2RequestCapability, options []image2CapabilityOption) []string {
	nearest := image2NearestOptions(request, options)
	seen := make(map[string]struct{}, 4)
	for _, option := range nearest {
		for _, mismatch := range image2OptionMismatches(request, option) {
			seen[mismatch] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for _, dimension := range []string{"operation", "size", "quality", "n"} {
		if _, ok := seen[dimension]; ok {
			result = append(result, dimension)
		}
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

func image2Alternatives(request Image2RequestCapability, options []image2CapabilityOption) []Image2Alternative {
	// An alternative is actionable only when it preserves the requested
	// operation. Never tell an edits client to silently switch to generations,
	// or vice versa.
	ordered := make([]image2CapabilityOption, 0, len(options))
	for _, option := range options {
		if strings.EqualFold(option.alternative.Operation, request.Operation) {
			ordered = append(ordered, option)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		leftMismatch := len(image2OptionMismatches(request, ordered[i]))
		rightMismatch := len(image2OptionMismatches(request, ordered[j]))
		if leftMismatch != rightMismatch {
			return leftMismatch < rightMismatch
		}
		left := fmt.Sprintf("%s/%s/%s/%d", ordered[i].alternative.Operation, ordered[i].alternative.Size, ordered[i].alternative.Quality, ordered[i].alternative.N)
		right := fmt.Sprintf("%s/%s/%s/%d", ordered[j].alternative.Operation, ordered[j].alternative.Size, ordered[j].alternative.Quality, ordered[j].alternative.N)
		return left < right
	})
	seen := make(map[string]struct{}, len(ordered))
	result := make([]Image2Alternative, 0, len(ordered))
	for _, option := range ordered {
		alternative := option.alternative
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%d", alternative.Operation, alternative.Size, alternative.Quality, alternative.N)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, alternative)
		if len(result) >= image2AlternativeLimit {
			break
		}
	}
	return result
}

func image2SuggestedN(maxN uint) uint {
	if maxN == 0 {
		// Unlimited declarations are intentionally represented by a conservative
		// one-image suggestion. The n field remains a safe alternative, not a
		// promise to fan out to the declared ceiling.
		return 1
	}
	return maxN
}

func image2OptionsForCapability(capability *dto.Image2ChannelCapability) []image2CapabilityOption {
	if capability == nil || !capability.Enabled {
		return nil
	}
	options := make([]image2CapabilityOption, 0)
	if len(capability.Profiles) > 0 {
		for _, profile := range capability.Profiles {
			// Keep the declared profile quality in channel settings for audit, but
			// do not expose standard/high as distinct routing alternatives.
			quality := "auto"
			resolution := strings.ToLower(strings.TrimSpace(profile.Resolution))
			size := canonicalImage2Size(profile.Size)
			if profile.Size == "" {
				size = canonicalImage2Size(resolution)
			}
			options = append(options, image2CapabilityOption{
				alternative: Image2Alternative{Operation: strings.ToLower(strings.TrimSpace(profile.Operation)), Size: size, Quality: quality, N: image2SuggestedN(profile.MaxN)},
				resolution:  resolution,
				exactSize:   profile.Size != "" || resolution == "uhd",
				maxN:        profile.MaxN,
			})
		}
		return options
	}
	// Quality is normalized to provider default for every legal client value.
	qualities := []string{"auto"}
	// Alternatives are suggestions, not an exhaustive n contract. Keep them
	// bounded and conservative so an unbounded declaration never suggests a
	// potentially expensive fan-out.
	for _, operation := range capability.Operations {
		operation = strings.ToLower(strings.TrimSpace(operation))
		if operation == "edits" && !capability.EditsAccepted {
			continue
		}
		for _, resolution := range capability.Resolutions {
			resolution = strings.ToLower(strings.TrimSpace(resolution))
			for _, quality := range qualities {
				options = append(options, image2CapabilityOption{
					alternative: Image2Alternative{Operation: operation, Size: canonicalImage2Size(resolution), Quality: quality, N: image2SuggestedN(capability.MaxN)},
					resolution:  resolution,
					exactSize:   resolution == "uhd",
					maxN:        capability.MaxN,
				})
			}
		}
	}
	return options
}

func image2ErrorMessage(explanation Image2RoutingExplanation, temporarilyUnavailable bool) string {
	request := normalizeImage2RequestCapability(explanation.Request)
	if temporarilyUnavailable {
		return fmt.Sprintf(
			"Image2 request is temporarily unavailable: operation=%s, resolution=%s, size=%s, requested_quality=%s, effective_quality=%s, n=%d; a configured compatible capability exists but all matching channels are temporarily unavailable; upstream_called=false; charged=false",
			request.Operation, request.Resolution, request.Size, request.RequestedQuality, request.EffectiveQuality, request.N,
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
	prefix := "unsupported Image2 configuration"
	if explanation.CombinationUnsupported {
		prefix += ": complete combination unsupported"
	}
	return fmt.Sprintf(
		"%s: %s; safe alternatives: %s; operation=%s, resolution=%s, size=%s, requested_quality=%s, effective_quality=%s, n=%d; upstream_called=false; charged=false",
		prefix,
		strings.Join(dimensions, "; "), formatImage2Alternatives(explanation.Alternatives), request.Operation, request.Resolution, request.Size, request.RequestedQuality, request.EffectiveQuality, request.N,
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
	explanation.Request = normalizeImage2RequestCapability(explanation.Request)
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
		RequestedQuality:      explanation.Request.RequestedQuality,
		EffectiveQuality:      explanation.Request.EffectiveQuality,
		Quality:               explanation.Request.EffectiveQuality,
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
