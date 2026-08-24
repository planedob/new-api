package service

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// Image2RequestCapability is the normalized, auditable request shape used for
// routing. It intentionally contains no provider or production channel ID.
type Image2RequestCapability struct {
	Operation  string
	Resolution string
	Size       string
	Quality    string
	N          uint
}

type Image2CandidateDecision struct {
	ChannelID int
	Selected  bool
	Reason    string
}

// Image2SmartRouter owns one ordered attempt chain for a request. Next marks
// a channel used before returning it, enforcing one attempt per channel.
type Image2SmartRouter struct {
	request    Image2RequestCapability
	candidates []*model.Channel
	next       int
	decisions  []Image2CandidateDecision
	configured bool
	priorities map[int]int
}

func Image2RouteMode() string {
	switch common.Image2RouteMode {
	case common.Image2RouteModeLegacy, common.Image2RouteModeObserve, common.Image2RouteModeAdvanced:
		return common.Image2RouteMode
	default:
		// Runtime mutations and malformed persisted values must not activate
		// enforcement. Return the established selector path instead.
		return common.Image2RouteModeLegacy
	}
}

// Image2SmartRoutingEnabled requires both the existing opt-in gate and the
// enforcing advanced route mode. legacy is intentionally an emergency
// selector rollback; observe evaluates declarations without changing the
// established selector.
func Image2SmartRoutingEnabled() bool {
	return common.GetImage2SmartRoutingEnabled() && Image2RouteMode() == common.Image2RouteModeAdvanced
}

// Image2SmartRoutingObserveEnabled evaluates the same capability contract as
// advanced mode but leaves the established selector in charge. This gives an
// operator a no-routing-change migration phase for capability declarations.
func Image2SmartRoutingObserveEnabled() bool {
	return common.GetImage2SmartRoutingEnabled() && Image2RouteMode() == common.Image2RouteModeObserve
}

func IsImage2SmartRoute(info *relaycommon.RelayInfo) bool {
	return info != nil && strings.EqualFold(strings.TrimSpace(info.OriginModelName), "gpt-image-2") &&
		(info.RelayMode == relayconstant.RelayModeImagesGenerations || info.RelayMode == relayconstant.RelayModeImagesEdits)
}

func ParseImage2RequestCapability(info *relaycommon.RelayInfo, request *dto.ImageRequest) (Image2RequestCapability, error) {
	if !IsImage2SmartRoute(info) || request == nil {
		return Image2RequestCapability{}, fmt.Errorf("not an Image2 image request")
	}
	operation := "generations"
	if info.RelayMode == relayconstant.RelayModeImagesEdits {
		operation = "edits"
	}
	resolution, err := image2Resolution(request.Size)
	if err != nil {
		return Image2RequestCapability{}, err
	}
	n := uint(1)
	if request.N != nil {
		n = *request.N
		if n == 0 || n > dto.MaxImageN {
			return Image2RequestCapability{}, fmt.Errorf("invalid Image2 n %d; expected a value between 1 and %d", n, dto.MaxImageN)
		}
	}
	return Image2RequestCapability{
		Operation:  operation,
		Resolution: resolution,
		Size:       canonicalImage2Size(request.Size),
		Quality:    strings.ToLower(strings.TrimSpace(request.Quality)),
		N:          n,
	}, nil
}

func canonicalImage2Size(size string) string {
	size = strings.ToLower(strings.TrimSpace(size))
	switch size {
	case "", "auto":
		return "auto"
	case "1024":
		return "1024x1024"
	case "2048":
		return "2048x2048"
	case "4096":
		return "4096x4096"
	case "uhd":
		return "3840x2160"
	default:
		return size
	}
}

func image2Resolution(size string) (string, error) {
	size = strings.ToLower(strings.TrimSpace(size))
	// OpenAI permits "auto". The first routing version deliberately maps it to
	// the same conservative default tier as an omitted size.
	if size == "" || size == "auto" || size == "1024" || size == "1024x1024" {
		return "1024", nil
	}
	if size == "2048" || size == "2048x2048" {
		return "2048", nil
	}
	if size == "uhd" || size == "4096" || size == "4096x4096" {
		return "uhd", nil
	}
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return "", fmt.Errorf("unsupported Image2 size %q", size)
	}
	w, wErr := strconv.Atoi(parts[0])
	h, hErr := strconv.Atoi(parts[1])
	if wErr != nil || hErr != nil || w < 1 || h < 1 {
		return "", fmt.Errorf("invalid Image2 size %q", size)
	}
	if w > 4096 || h > 4096 {
		return "", fmt.Errorf("unsupported Image2 size %q: dimensions exceed 4096", size)
	}
	if w <= 1024 && h <= 1024 {
		return "1024", nil
	}
	if w <= 2048 && h <= 2048 {
		return "2048", nil
	}
	return "uhd", nil
}

// NewImage2SmartRouter filters every model-bound candidate before selecting.
// Capability metadata is opt-in, therefore an unconfigured channel is safely
// excluded rather than guessed to support a request.
func NewImage2SmartRouter(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ImageRequest) (router *Image2SmartRouter, err error) {
	observe := Image2SmartRoutingObserveEnabled()
	if (!Image2SmartRoutingEnabled() && !observe) || !IsImage2SmartRoute(info) {
		return nil, nil
	}
	if observe {
		// Observe is a side-channel migration aid. A bad capability declaration
		// or evaluator panic must never take the established legacy request path
		// down with it.
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.LogError(c, fmt.Sprintf("image2 smart routing observe evaluation panic: %v; legacy selector unchanged", recovered))
				router = nil
				err = nil
			}
		}()
	}
	if _, pinned := c.Get("specific_channel_id"); pinned {
		// An administrator-selected channel is an explicit routing contract.
		// Keep the established selector so the first attempt uses that channel
		// and the existing retry guard prevents any automatic channel switch.
		logger.LogInfo(c, "image2 smart routing bypassed for a specifically selected channel")
		return nil, nil
	}
	req, err := ParseImage2RequestCapability(info, request)
	if err != nil {
		return nil, err
	}
	group := common.GetContextKeyString(c, constant.ContextKeyAutoGroup)
	if group == "" {
		group = info.UsingGroup
	}
	if group == "" || group == "auto" {
		group = info.TokenGroup
	}
	if group == "" || group == "auto" {
		return nil, fmt.Errorf("Image2 smart routing requires a resolved channel group")
	}

	channels, err := model.GetSatisfiedChannels(group, info.OriginModelName)
	if err != nil {
		return nil, err
	}
	router, configured := newImage2SmartRouterIfConfigured(req, channels)
	if observe {
		if !configured {
			logger.LogWarn(c, fmt.Sprintf(
				"image2 smart routing observe: no configured capability in group %s; legacy selector unchanged",
				group,
			))
			return nil, nil
		}
		logger.LogInfo(c, fmt.Sprintf(
			"image2 smart routing observe: request=%s/%s quality=%q n=%d group=%s candidates=%s; legacy selector unchanged",
			req.Operation, req.Resolution, req.Quality, req.N, group, router.DecisionSummary(),
		))
		return nil, nil
	}
	if !configured {
		// A switch-only rollout must not turn a missing capability migration
		// into a complete outage. Fall back only when no channel has opted in;
		// once any capability is configured, incompatibility remains fail-closed.
		logger.LogWarn(c, fmt.Sprintf(
			"image2 smart routing has no configured capability in group %s; using legacy routing",
			group,
		))
		return nil, nil
	}
	logger.LogInfo(c, fmt.Sprintf("image2 smart routing: request=%s/%s quality=%q n=%d group=%s candidates=%s", req.Operation, req.Resolution, req.Quality, req.N, group, router.DecisionSummary()))
	return router, nil
}

func newImage2SmartRouterIfConfigured(req Image2RequestCapability, channels []*model.Channel) (*Image2SmartRouter, bool) {
	router := newImage2SmartRouter(req, channels)
	if !router.configured {
		return nil, false
	}
	return router, true
}

func newImage2SmartRouter(req Image2RequestCapability, channels []*model.Channel) *Image2SmartRouter {
	router := &Image2SmartRouter{
		request:    req,
		decisions:  make([]Image2CandidateDecision, 0, len(channels)),
		priorities: make(map[int]int, len(channels)),
	}
	now := time.Now().UTC()
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		setting := dto.ChannelSettings{}
		if channel.Setting != nil && strings.TrimSpace(*channel.Setting) != "" {
			if err := common.Unmarshal([]byte(*channel.Setting), &setting); err != nil {
				router.configured = true
				router.decisions = append(router.decisions, Image2CandidateDecision{ChannelID: channel.Id, Reason: "image2_capability_invalid"})
				continue
			}
		}
		capability := setting.Image2Capability
		if (capability != nil && capability.Enabled) || setting.Image2CapabilityVerification != nil {
			router.configured = true
		}
		if capability == nil {
			router.decisions = append(router.decisions, Image2CandidateDecision{ChannelID: channel.Id, Reason: "image2_capability_not_enabled"})
			continue
		}
		if err := capability.Validate(); err != nil {
			router.decisions = append(router.decisions, Image2CandidateDecision{ChannelID: channel.Id, Reason: "image2_capability_invalid"})
			continue
		}
		if !capability.Enabled {
			router.decisions = append(router.decisions, Image2CandidateDecision{ChannelID: channel.Id, Reason: "image2_capability_not_enabled"})
			continue
		}
		if reason := setting.Image2CapabilityVerification.RoutingReason(now, channel.Id, capability); reason != "" {
			router.decisions = append(router.decisions, Image2CandidateDecision{ChannelID: channel.Id, Reason: reason})
			continue
		}
		if reason := image2Incompatibility(req, capability); reason != "" {
			router.decisions = append(router.decisions, Image2CandidateDecision{ChannelID: channel.Id, Reason: reason})
			continue
		}
		router.priorities[channel.Id] = capability.RoutePriority
		router.candidates = append(router.candidates, channel)
	}
	sort.SliceStable(router.candidates, func(i, j int) bool {
		left, right := router.priorities[router.candidates[i].Id], router.priorities[router.candidates[j].Id]
		if left != right {
			return left < right
		}
		return router.candidates[i].Id < router.candidates[j].Id
	})
	for _, channel := range router.candidates {
		router.decisions = append(router.decisions, Image2CandidateDecision{ChannelID: channel.Id, Reason: "compatible"})
	}
	return router
}

func image2Incompatibility(req Image2RequestCapability, capability *dto.Image2ChannelCapability) string {
	if capability == nil || !capability.Enabled {
		return "image2_capability_not_enabled"
	}
	if req.N == 0 || req.N > capability.MaxN {
		return "n_exceeds_limit"
	}
	if len(capability.Profiles) > 0 {
		requestQuality := strings.ToLower(strings.TrimSpace(req.Quality))
		if requestQuality == "" || requestQuality == "auto" {
			requestQuality = "default"
		}
		for _, profile := range capability.Profiles {
			if !strings.EqualFold(strings.TrimSpace(profile.Operation), req.Operation) ||
				!strings.EqualFold(strings.TrimSpace(profile.Resolution), req.Resolution) ||
				!strings.EqualFold(strings.TrimSpace(profile.Quality), requestQuality) {
				continue
			}
			if canonicalImage2Size(profile.Size) != canonicalImage2Size(req.Size) {
				continue
			}
			if profile.MaxN >= req.N {
				return ""
			}
		}
		return "capability_profile_unverified"
	}
	if !containsFold(capability.Operations, req.Operation) {
		return "operation_unsupported"
	}
	if req.Operation == "edits" && !capability.EditsAccepted {
		return "edits_not_accepted"
	}
	if !containsFold(capability.Resolutions, req.Resolution) {
		return "resolution_unsupported"
	}
	if req.Resolution == "uhd" && req.Size != "" && canonicalImage2Size(req.Size) != "auto" && canonicalImage2Size(req.Size) != canonicalImage2Size("uhd") {
		return "size_unsupported"
	}
	// Omitted/auto quality means "use the provider default". Every explicit
	// quality must be declared; an empty list is not a wildcard.
	quality := strings.ToLower(strings.TrimSpace(req.Quality))
	if quality != "" && quality != "auto" {
		if len(capability.Qualities) == 0 {
			return "quality_unverified"
		}
		if !containsFold(capability.Qualities, quality) {
			return "quality_unsupported"
		}
	}
	return ""
}

func containsFold(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(strings.TrimSpace(candidate), value) {
			return true
		}
	}
	return false
}

func (r *Image2SmartRouter) Next() (*model.Channel, *types.NewAPIError) {
	if r == nil || r.next >= len(r.candidates) {
		return nil, types.NewError(fmt.Errorf("no compatible Image2 channel remains"), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	channel := r.candidates[r.next]
	r.next++
	for i := range r.decisions {
		if r.decisions[i].ChannelID == channel.Id {
			r.decisions[i].Selected = true
			r.decisions[i].Reason = "selected"
			break
		}
	}
	return channel, nil
}

// HasCandidates reports whether capability filtering left at least one
// eligible channel. The relay checks this before billing so an enforced
// no-compatible-candidate result fails closed without a pre-consume/refund
// cycle.
func (r *Image2SmartRouter) HasCandidates() bool {
	return r != nil && len(r.candidates) > 0
}

func (r *Image2SmartRouter) DecisionSummary() string {
	if r == nil {
		return ""
	}
	parts := make([]string, 0, len(r.decisions))
	for _, decision := range r.decisions {
		parts = append(parts, fmt.Sprintf("%d:%s", decision.ChannelID, decision.Reason))
	}
	return strings.Join(parts, ",")
}
