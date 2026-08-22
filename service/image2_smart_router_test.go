package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func image2TestChannel(id, priority int, operations, resolutions []string, editsAccepted bool) *model.Channel {
	capability := &dto.Image2ChannelCapability{
		Enabled: true, Operations: operations, Resolutions: resolutions, EditsAccepted: editsAccepted, MaxN: 4, RoutePriority: priority,
	}
	setting := dto.ChannelSettings{Image2Capability: capability}
	setImage2TestVerification(&setting)
	channel := &model.Channel{Id: id}
	channel.SetSetting(setting)
	if channel.GetSetting().Image2Capability == nil {
		panic("image2 test channel lost capability setting")
	}
	return channel
}

func setImage2TestVerification(setting *dto.ChannelSettings) {
	digest, err := dto.Image2CapabilitySHA256(setting.Image2Capability)
	if err != nil {
		panic(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	setting.Image2CapabilityVerification = &dto.Image2CapabilityVerification{
		Status:           "passed",
		Source:           "fixed_channel_test",
		VerifiedAt:       now.Add(-time.Hour).Format(time.RFC3339),
		ValidUntil:       now.Add(time.Hour).Format(time.RFC3339),
		CapabilitySHA256: digest,
		EvidenceSHA256:   []string{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	}
}

func TestImage2SmartRouterDisabledKeepsLegacyPath(t *testing.T) {
	old := common.Image2SmartRoutingEnabled
	common.Image2SmartRoutingEnabled = false
	t.Cleanup(func() { common.Image2SmartRoutingEnabled = old })
	c, _ := gin.CreateTestContext(nil)
	router, err := NewImage2SmartRouter(c, &relaycommon.RelayInfo{OriginModelName: "gpt-image-2", RelayMode: relayconstant.RelayModeImagesGenerations}, &dto.ImageRequest{})
	require.NoError(t, err)
	require.Nil(t, router)
}

func TestImage2LegacyRouteModeKeepsLegacyPathWhenSmartRoutingIsEnabled(t *testing.T) {
	oldEnabled := common.Image2SmartRoutingEnabled
	oldMode := common.Image2RouteMode
	common.Image2SmartRoutingEnabled = true
	common.Image2RouteMode = common.Image2RouteModeLegacy
	t.Cleanup(func() {
		common.Image2SmartRoutingEnabled = oldEnabled
		common.Image2RouteMode = oldMode
	})

	c, _ := gin.CreateTestContext(nil)
	router, err := NewImage2SmartRouter(c, &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-2",
		RelayMode:       relayconstant.RelayModeImagesGenerations,
	}, &dto.ImageRequest{Size: "1024x1024"})

	require.NoError(t, err)
	require.Nil(t, router)
}

func TestImage2ObserveRouteModeDoesNotEnableEnforcement(t *testing.T) {
	oldEnabled := common.Image2SmartRoutingEnabled
	oldMode := common.Image2RouteMode
	common.Image2SmartRoutingEnabled = true
	common.Image2RouteMode = common.Image2RouteModeObserve
	t.Cleanup(func() {
		common.Image2SmartRoutingEnabled = oldEnabled
		common.Image2RouteMode = oldMode
	})

	require.False(t, Image2SmartRoutingEnabled())
	require.True(t, Image2SmartRoutingObserveEnabled())
}

func TestImage2UnknownRouteModeFailsClosedToLegacy(t *testing.T) {
	oldMode := common.Image2RouteMode
	common.Image2RouteMode = "not-a-mode"
	t.Cleanup(func() { common.Image2RouteMode = oldMode })

	require.Equal(t, common.Image2RouteModeLegacy, Image2RouteMode())
}

func TestImage2UnknownRouteModeCannotEnableSmartRouting(t *testing.T) {
	oldEnabled := common.Image2SmartRoutingEnabled
	oldMode := common.Image2RouteMode
	common.SetImage2SmartRoutingEnabled(true)
	common.Image2RouteMode = "not-a-mode"
	t.Cleanup(func() {
		common.SetImage2SmartRoutingEnabled(oldEnabled)
		common.Image2RouteMode = oldMode
	})

	require.False(t, Image2SmartRoutingEnabled())
	require.False(t, Image2SmartRoutingObserveEnabled())
}

func TestImage2SmartRouterSpecificChannelKeepsPinnedLegacyPath(t *testing.T) {
	old := common.Image2SmartRoutingEnabled
	common.Image2SmartRoutingEnabled = true
	t.Cleanup(func() { common.Image2SmartRoutingEnabled = old })

	c, _ := gin.CreateTestContext(nil)
	c.Set("specific_channel_id", "47")
	router, err := NewImage2SmartRouter(
		c,
		&relaycommon.RelayInfo{
			OriginModelName: "gpt-image-2",
			RelayMode:       relayconstant.RelayModeImagesGenerations,
		},
		&dto.ImageRequest{Size: "1024x1024"},
	)

	require.NoError(t, err)
	require.Nil(t, router, "a specifically selected channel must bypass smart routing")
}

func TestImage2SmartRouterResolutionAndEditFiltering(t *testing.T) {
	web := image2TestChannel(1, 10, []string{"generations"}, []string{"1024"}, false)
	codex := image2TestChannel(2, 20, []string{"generations", "edits"}, []string{"1024", "2048"}, true)
	adobe := image2TestChannel(3, 30, []string{"generations"}, []string{"1024", "2048", "uhd"}, false)

	for _, test := range []struct {
		name, resolution, operation string
		want                        []int
	}{
		{"1024 generation", "1024", "generations", []int{1, 2, 3}},
		{"2048 generation", "2048", "generations", []int{2, 3}},
		{"uhd generation", "uhd", "generations", []int{3}},
		{"edits excludes unaccepted", "1024", "edits", []int{2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := newImage2SmartRouter(Image2RequestCapability{Resolution: test.resolution, Operation: test.operation, N: 1}, []*model.Channel{adobe, codex, web})
			got := make([]int, 0, len(test.want))
			for range test.want {
				channel, err := router.Next()
				require.Nil(t, err)
				got = append(got, channel.Id)
			}
			require.Equal(t, test.want, got)
			_, err := router.Next()
			require.True(t, types.IsSkipRetryError(err))
		})
	}
}

// This simulates isolated fake upstreams: 503, 503, then 200. It exercises
// the same ordered candidate chain and safe replay decision used by Relay.
func TestImage2FakeUpstream503Then503Then200(t *testing.T) {
	router := newImage2SmartRouter(Image2RequestCapability{Operation: "generations", Resolution: "1024", N: 1}, []*model.Channel{
		image2TestChannel(1, 10, []string{"generations"}, []string{"1024"}, false),
		image2TestChannel(2, 20, []string{"generations"}, []string{"1024"}, false),
		image2TestChannel(3, 30, []string{"generations"}, []string{"1024"}, false),
	})
	statuses := []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusOK}
	servers := make([]*httptest.Server, 0, len(statuses))
	for _, status := range statuses {
		fakeStatus := status
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(fakeStatus) }))
		servers = append(servers, server)
		t.Cleanup(server.Close)
	}
	called := make([]int, 0, len(statuses))
	for index := range statuses {
		channel, err := router.Next()
		require.Nil(t, err)
		called = append(called, channel.Id)
		response, requestErr := http.Get(servers[index].URL)
		require.NoError(t, requestErr)
		status := response.StatusCode
		response.Body.Close()
		if status == http.StatusOK {
			break
		}
		decision := EvaluateSafeFailover(SafeFailoverInput{RelayMode: relayconstant.RelayModeImagesGenerations, ModelName: "gpt-image-2", Error: types.NewOpenAIError(errors.New("fake upstream"), "", status)})
		require.True(t, decision.Retry)
	}
	require.Equal(t, []int{1, 2, 3}, called)
}

func TestParseImage2RequestCapability(t *testing.T) {
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-image-2", RelayMode: relayconstant.RelayModeImagesEdits}
	capability, err := ParseImage2RequestCapability(info, &dto.ImageRequest{Size: "4096x4096"})
	require.NoError(t, err)
	require.Equal(t, "edits", capability.Operation)
	require.Equal(t, "uhd", capability.Resolution)
	require.Equal(t, "4096x4096", capability.Size)
	capability, err = ParseImage2RequestCapability(info, &dto.ImageRequest{Size: "auto"})
	require.NoError(t, err)
	require.Equal(t, "1024", capability.Resolution)
	require.Equal(t, "auto", capability.Size)
	zero := uint(0)
	_, err = ParseImage2RequestCapability(info, &dto.ImageRequest{Size: "1024x1024", N: &zero})
	require.ErrorContains(t, err, "between 1 and")
	tooMany := uint(dto.MaxImageN + 1)
	_, err = ParseImage2RequestCapability(info, &dto.ImageRequest{Size: "1024x1024", N: &tooMany})
	require.ErrorContains(t, err, "between 1 and")
}

func TestImage2SmartRouterRequiresCurrentVerificationBeforeCandidateSelection(t *testing.T) {
	channel := &model.Channel{Id: 44}
	channel.SetSetting(dto.ChannelSettings{Image2Capability: &dto.Image2ChannelCapability{
		Enabled: true, Operations: []string{"generations"}, Resolutions: []string{"1024"}, MaxN: 1,
	}})
	router, configured := newImage2SmartRouterIfConfigured(
		Image2RequestCapability{Operation: "generations", Resolution: "1024", N: 1},
		[]*model.Channel{channel},
	)
	require.True(t, configured)
	require.NotNil(t, router)
	require.False(t, router.HasCandidates())
	require.Contains(t, router.DecisionSummary(), "44:image2_verification_missing")
}

func TestImage2SmartRouterRejectsFailedExpiredAndFutureVerification(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	failed := image2TestChannel(44, 10, []string{"generations"}, []string{"1024"}, false)
	failedSetting := failed.GetSetting()
	failedSetting.Image2CapabilityVerification.Status = "failed"
	failed.SetSetting(failedSetting)

	expired := image2TestChannel(74, 20, []string{"generations"}, []string{"1024"}, false)
	expiredSetting := expired.GetSetting()
	expiredSetting.Image2CapabilityVerification.VerifiedAt = now.Add(-2 * time.Hour).Format(time.RFC3339)
	expiredSetting.Image2CapabilityVerification.ValidUntil = now.Add(-time.Hour).Format(time.RFC3339)
	expired.SetSetting(expiredSetting)

	future := image2TestChannel(75, 30, []string{"generations"}, []string{"1024"}, false)
	futureSetting := future.GetSetting()
	futureSetting.Image2CapabilityVerification.VerifiedAt = now.Add(time.Hour).Format(time.RFC3339)
	futureSetting.Image2CapabilityVerification.ValidUntil = now.Add(2 * time.Hour).Format(time.RFC3339)
	future.SetSetting(futureSetting)

	router := newImage2SmartRouter(Image2RequestCapability{Operation: "generations", Resolution: "1024", N: 1}, []*model.Channel{failed, expired, future})
	require.False(t, router.HasCandidates())
	require.Contains(t, router.DecisionSummary(), "44:image2_verification_failed")
	require.Contains(t, router.DecisionSummary(), "74:image2_verification_expired")
	require.Contains(t, router.DecisionSummary(), "75:image2_verification_not_yet_valid")
}

func TestImage2SmartRouterProfilesDoNotInventUnverifiedSizeQualityCombination(t *testing.T) {
	channel := image2TestChannel(47, 10, []string{"generations", "edits"}, []string{"2048"}, true)
	setting := channel.GetSetting()
	setting.Image2Capability.Qualities = []string{"high"}
	setting.Image2Capability.Profiles = []dto.Image2CapabilityProfile{
		{Operation: "generations", Resolution: "2048", Size: "2048x2048", Quality: "high", MaxN: 1},
	}
	setImage2TestVerification(&setting)
	channel.SetSetting(setting)

	supported := newImage2SmartRouter(Image2RequestCapability{
		Operation: "generations", Resolution: "2048", Size: "2048x2048", Quality: "high", N: 1,
	}, []*model.Channel{channel})
	require.True(t, supported.HasCandidates())
	selected, err := supported.Next()
	require.Nil(t, err)
	require.Equal(t, channel.Id, selected.Id)

	untested := newImage2SmartRouter(Image2RequestCapability{
		Operation: "generations", Resolution: "2048", Size: "1536x2048", Quality: "high", N: 1,
	}, []*model.Channel{channel})
	require.False(t, untested.HasCandidates())
	require.Contains(t, untested.DecisionSummary(), "47:capability_profile_unverified")
}

func TestImage2SmartRouterExcludesChannelWithoutCapabilityMetadata(t *testing.T) {
	configured := image2TestChannel(1, 10, []string{"generations"}, []string{"1024"}, false)
	unconfigured := &model.Channel{Id: 2}
	router := newImage2SmartRouter(Image2RequestCapability{Operation: "generations", Resolution: "1024", N: 1}, []*model.Channel{unconfigured, configured})

	channel, err := router.Next()
	require.Nil(t, err)
	require.Equal(t, configured.Id, channel.Id)
	require.Contains(t, router.DecisionSummary(), "2:image2_capability_not_enabled")
	_, err = router.Next()
	require.True(t, types.IsSkipRetryError(err))
}

func TestImage2SmartRouterDoesNotTreatDisabledCapabilityAsConfigured(t *testing.T) {
	channel := &model.Channel{Id: 6}
	channel.SetSetting(dto.ChannelSettings{Image2Capability: &dto.Image2ChannelCapability{Enabled: false}})
	router, configured := newImage2SmartRouterIfConfigured(
		Image2RequestCapability{Operation: "generations", Resolution: "1024", N: 1},
		[]*model.Channel{channel},
	)
	require.False(t, configured)
	require.Nil(t, router)
}

func TestImage2SmartRouterMalformedCapabilitySettingFailsClosed(t *testing.T) {
	raw := "{not-json"
	channel := &model.Channel{Id: 7, Setting: &raw}
	router, configured := newImage2SmartRouterIfConfigured(
		Image2RequestCapability{Operation: "generations", Resolution: "1024", N: 1},
		[]*model.Channel{channel},
	)
	require.True(t, configured)
	require.NotNil(t, router)
	require.False(t, router.HasCandidates())
	require.Contains(t, router.DecisionSummary(), "7:image2_capability_invalid")
}

func TestImage2SmartRouterFallsBackOnlyWhenAllCapabilitiesAreMissing(t *testing.T) {
	unconfigured := []*model.Channel{{Id: 1}, {Id: 2}}
	request := Image2RequestCapability{Operation: "generations", Resolution: "1024", N: 1}
	router, capabilityConfigured := newImage2SmartRouterIfConfigured(request, unconfigured)
	require.False(t, capabilityConfigured)
	require.Nil(t, router, "a capability migration omission must preserve legacy routing")

	configuredButIncompatible := image2TestChannel(
		3,
		10,
		[]string{"generations"},
		[]string{"uhd"},
		false,
	)
	configuredChannels := append(unconfigured, configuredButIncompatible)
	router, capabilityConfigured = newImage2SmartRouterIfConfigured(request, configuredChannels)
	require.True(t, capabilityConfigured)
	require.NotNil(t, router, "configured but incompatible capabilities must remain fail-closed")
	_, routeErr := router.Next()
	require.True(t, types.IsSkipRetryError(routeErr))
	require.Contains(t, router.DecisionSummary(), "resolution_unsupported")
	require.False(t, router.HasCandidates())
}

func TestImage2SmartRouterQualityFiltering(t *testing.T) {
	channel := image2TestChannel(1, 10, []string{"generations"}, []string{"1024"}, false)
	setting := channel.GetSetting()
	setting.Image2Capability.Qualities = []string{"standard", "high"}
	setImage2TestVerification(&setting)
	channel.SetSetting(setting)

	t.Run("omitted quality uses provider default", func(t *testing.T) {
		router := newImage2SmartRouter(Image2RequestCapability{
			Operation:  "generations",
			Resolution: "1024",
			N:          1,
		}, []*model.Channel{channel})

		selected, err := router.Next()
		require.Nil(t, err)
		require.Equal(t, channel.Id, selected.Id)
	})

	t.Run("explicit unsupported quality is rejected", func(t *testing.T) {
		router := newImage2SmartRouter(Image2RequestCapability{
			Operation:  "generations",
			Resolution: "1024",
			Quality:    "ultra",
			N:          1,
		}, []*model.Channel{channel})

		_, err := router.Next()
		require.True(t, types.IsSkipRetryError(err))
		require.Contains(t, router.DecisionSummary(), "1:quality_unsupported")
	})

	t.Run("explicit quality is not inferred from an empty declaration", func(t *testing.T) {
		setting := channel.GetSetting()
		setting.Image2Capability.Qualities = nil
		setImage2TestVerification(&setting)
		channel.SetSetting(setting)
		router := newImage2SmartRouter(Image2RequestCapability{
			Operation:  "generations",
			Resolution: "1024",
			Quality:    "high",
			N:          1,
		}, []*model.Channel{channel})

		_, err := router.Next()
		require.True(t, types.IsSkipRetryError(err))
		require.Contains(t, router.DecisionSummary(), "1:quality_unverified")
	})

	t.Run("quantity above max n is rejected", func(t *testing.T) {
		setting := channel.GetSetting()
		setting.Image2Capability.MaxN = 1
		setImage2TestVerification(&setting)
		channel.SetSetting(setting)
		router := newImage2SmartRouter(Image2RequestCapability{
			Operation:  "generations",
			Resolution: "1024",
			N:          2,
		}, []*model.Channel{channel})

		_, err := router.Next()
		require.True(t, types.IsSkipRetryError(err))
		require.Contains(t, router.DecisionSummary(), "1:n_exceeds_limit")
	})
}

func TestImage2SafeFailoverStopsDeterministicErrors(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   *types.NewAPIError
		retry bool
	}{
		{"503 can move to next fake upstream", types.NewOpenAIError(errors.New("unavailable"), "", http.StatusServiceUnavailable), true},
		{"400 does not move", types.NewOpenAIError(errors.New("bad request"), "", http.StatusBadRequest), false},
		{"accepted task does not move", types.NewOpenAIError(errors.New("task_id=abc"), "", http.StatusServiceUnavailable), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision := EvaluateSafeFailover(SafeFailoverInput{RelayMode: relayconstant.RelayModeImagesGenerations, ModelName: "gpt-image-2", Error: test.err})
			require.Equal(t, test.retry, decision.Retry)
		})
	}
}
