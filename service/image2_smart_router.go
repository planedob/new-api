package service

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

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
}

func Image2SmartRoutingEnabled() bool { return common.Image2SmartRoutingEnabled }

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
	}
	return Image2RequestCapability{Operation: operation, Resolution: resolution, Quality: strings.ToLower(strings.TrimSpace(request.Quality)), N: n}, nil
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
func NewImage2SmartRouter(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ImageRequest) (*Image2SmartRouter, error) {
	if !Image2SmartRoutingEnabled() || !IsImage2SmartRoute(info) {
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
	router := newImage2SmartRouter(req, channels)
	logger.LogInfo(c, fmt.Sprintf("image2 smart routing: request=%s/%s quality=%q n=%d group=%s candidates=%s", req.Operation, req.Resolution, req.Quality, req.N, group, router.DecisionSummary()))
	return router, nil
}

func newImage2SmartRouter(req Image2RequestCapability, channels []*model.Channel) *Image2SmartRouter {
	router := &Image2SmartRouter{request: req, decisions: make([]Image2CandidateDecision, 0, len(channels))}
	for _, channel := range channels {
		capability := channel.GetSetting().Image2Capability
		if reason := image2Incompatibility(req, capability); reason != "" {
			router.decisions = append(router.decisions, Image2CandidateDecision{ChannelID: channel.Id, Reason: reason})
			continue
		}
		router.candidates = append(router.candidates, channel)
	}
	sort.SliceStable(router.candidates, func(i, j int) bool {
		left, right := router.candidates[i].GetSetting().Image2Capability, router.candidates[j].GetSetting().Image2Capability
		if left.RoutePriority != right.RoutePriority {
			return left.RoutePriority < right.RoutePriority
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
	if !containsFold(capability.Operations, req.Operation) {
		return "operation_unsupported"
	}
	if req.Operation == "edits" && !capability.EditsAccepted {
		return "edits_not_accepted"
	}
	if !containsFold(capability.Resolutions, req.Resolution) {
		return "resolution_unsupported"
	}
	if len(capability.Qualities) > 0 && !containsFold(capability.Qualities, req.Quality) {
		return "quality_unsupported"
	}
	if capability.MaxN > 0 && req.N > capability.MaxN {
		return "n_exceeds_limit"
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
