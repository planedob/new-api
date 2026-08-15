package service

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func image2ContractContext(requestID string) *gin.Context {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader("{}"))
	ctx.Set(common.RequestIdKey, requestID)
	return ctx
}

func decodeImage2ErrorMetadata(t *testing.T, err *types.NewAPIError) (types.OpenAIError, Image2ErrorMetadata) {
	t.Helper()
	require.NotNil(t, err)
	openAIError := err.ToOpenAIError()
	require.NotEmpty(t, openAIError.Metadata)
	var metadata Image2ErrorMetadata
	require.NoError(t, common.Unmarshal(openAIError.Metadata, &metadata))
	return openAIError, metadata
}

func TestImage2UnsupportedConfigurationReportsAllMismatchedDimensions(t *testing.T) {
	channel := image2ProfileChannel(44, "supplier-secret", 10, []string{"generations"}, []string{"1024"}, []string{"standard"}, 1, false)
	router := newImage2SmartRouter(Image2RequestCapability{
		Operation: "edits", Resolution: "uhd", Size: "4096x4096", Quality: "high", N: 2,
	}, []*model.Channel{channel})
	require.Equal(t, 0, router.CandidateCount())

	err := NewImage2PreRouteError(image2ContractContext("req-image2-422"), &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-2",
		RelayMode:       relayconstant.RelayModeImagesGenerations,
	}, router)
	require.Equal(t, http.StatusUnprocessableEntity, err.StatusCode)
	require.Equal(t, types.ErrorCodeUnsupportedImageConfiguration, err.GetErrorCode())
	openAIError, metadata := decodeImage2ErrorMetadata(t, err)
	require.Equal(t, "req-image2-422", metadata.RequestID)
	require.Equal(t, "edits", metadata.Operation)
	require.Equal(t, "uhd", metadata.Resolution)
	require.Equal(t, "4096x4096", metadata.Size)
	require.Equal(t, "high", metadata.RequestedQuality)
	require.Equal(t, "auto", metadata.EffectiveQuality)
	require.Equal(t, "auto", metadata.Quality)
	require.EqualValues(t, 2, metadata.N)
	require.Equal(t, []string{"operation", "size", "n"}, metadata.UnsupportedDimensions)
	require.False(t, metadata.UpstreamCalled)
	require.False(t, metadata.Charged)
	require.Empty(t, metadata.Alternatives, "an edits request must not suggest switching to generations")
	require.Empty(t, metadata.SafeAlternatives)
	require.Contains(t, openAIError.Message, "operation=edits")
	require.Contains(t, openAIError.Message, "size=4096x4096")
	require.Contains(t, openAIError.Message, "requested_quality=high")
	require.Contains(t, openAIError.Message, "effective_quality=auto")
	require.Contains(t, openAIError.Message, "n=2")
	require.Contains(t, openAIError.Message, "current supported values")
	require.Contains(t, openAIError.Message, "safe alternatives")
	require.Contains(t, openAIError.Message, "upstream_called=false")
	require.Contains(t, openAIError.Message, "charged=false")
	for _, forbidden := range []string{"44", "supplier-secret", "channel", "provider"} {
		require.NotContains(t, openAIError.Message, forbidden)
	}
}

func TestImage2UnsupportedConfigurationReportsSingleDimensionAndNoAlternative(t *testing.T) {
	channel := image2ProfileChannel(45, "provider-secret", 10, []string{"generations"}, []string{"1024"}, []string{"standard"}, 1, false)
	router := newImage2SmartRouter(Image2RequestCapability{
		Operation: "generations", Resolution: "2048", Size: "2048x2048", Quality: "standard", N: 1,
	}, []*model.Channel{channel})
	err := NewImage2PreRouteError(image2ContractContext("req-image2-single"), nil, router)
	openAIError, metadata := decodeImage2ErrorMetadata(t, err)
	require.Equal(t, http.StatusUnprocessableEntity, err.StatusCode)
	require.Equal(t, []string{"size"}, metadata.UnsupportedDimensions)
	require.NotEmpty(t, metadata.Alternatives)
	require.Contains(t, openAIError.Message, "size=2048x2048")
	require.Contains(t, openAIError.Message, "current supported values [1024x1024]")
	require.NotContains(t, openAIError.Message, "provider-secret")

	// A disabled declaration has no safe combination to suggest, but it is
	// still a configured Image2 route and must fail before billing.
	noAlternative := &model.Channel{Id: 46, Name: "hidden-provider"}
	noAlternative.SetSetting(dto.ChannelSettings{Image2DeclaredCapability: &dto.Image2ChannelCapability{Enabled: false}})
	noAlternativeRouter := newImage2SmartRouter(Image2RequestCapability{
		Operation: "generations", Resolution: "1024", Size: "1024x1024", Quality: "auto", N: 1,
	}, []*model.Channel{noAlternative})
	noAlternativeErr := NewImage2PreRouteError(image2ContractContext("req-image2-none"), nil, noAlternativeRouter)
	noAlternativeOpenAI, noAlternativeMetadata := decodeImage2ErrorMetadata(t, noAlternativeErr)
	require.Empty(t, noAlternativeMetadata.Alternatives)
	require.Contains(t, noAlternativeOpenAI.Message, "safe alternatives: none")
	require.Equal(t, []string{"operation", "size", "n"}, noAlternativeMetadata.UnsupportedDimensions)
	require.NotContains(t, noAlternativeOpenAI.Message, "hidden-provider")
}

func image2ExactProfilesChannel(id int, profiles ...dto.Image2CapabilityProfile) *model.Channel {
	channel := &model.Channel{Id: id, Name: fmt.Sprintf("profile-channel-%d", id)}
	channel.SetSetting(dto.ChannelSettings{Image2Capability: &dto.Image2ChannelCapability{
		Enabled:       true,
		Operations:    []string{"generations", "edits"},
		Resolutions:   []string{"1024", "2048"},
		EditsAccepted: true,
		Profiles:      profiles,
	}})
	return channel
}

func TestImage2UnsupportedCombinationPrefersSameOperationAndPreservesMaxN(t *testing.T) {
	channel := image2ExactProfilesChannel(48,
		dto.Image2CapabilityProfile{Operation: "generations", Resolution: "2048", Size: "2048x2048", Quality: "high", MaxN: 2},
		dto.Image2CapabilityProfile{Operation: "edits", Resolution: "1024", Size: "1024x1024", Quality: "standard", MaxN: 1},
	)
	router := newImage2SmartRouter(Image2RequestCapability{
		Operation: "edits", Resolution: "2048", Size: "2048x2048", Quality: "high", N: 2,
	}, []*model.Channel{channel})
	require.Equal(t, 0, router.CandidateCount())
	explanation := router.Explain()
	require.Equal(t, []string{"size", "n"}, explanation.UnsupportedDimensions)
	require.NotContains(t, explanation.UnsupportedDimensions, "operation")
	require.True(t, explanation.CombinationUnsupported)
	require.Equal(t, []string{"edits"}, explanation.SupportedValues["operation"])
	require.Equal(t, []string{"1024"}, explanation.SupportedValues["resolution"])
	require.Equal(t, []string{"1024x1024"}, explanation.SupportedValues["size"])
	require.Equal(t, []string{"auto"}, explanation.SupportedValues["quality"])
	require.Equal(t, []uint{1}, explanation.SupportedValues["n"])
	require.Len(t, explanation.Alternatives, 1)
	require.Equal(t, Image2Alternative{Operation: "edits", Size: "1024x1024", Quality: "auto", N: 1}, explanation.Alternatives[0], "same-operation alternative must be first")

	err := NewImage2PreRouteError(image2ContractContext("req-image2-combination"), nil, router)
	openAIError, metadata := decodeImage2ErrorMetadata(t, err)
	require.Equal(t, []string{"size", "n"}, metadata.UnsupportedDimensions)
	require.Contains(t, openAIError.Message, "complete combination unsupported")
	require.Contains(t, openAIError.Message, "size=2048x2048")
	require.Contains(t, openAIError.Message, "requested_quality=high")
	require.Contains(t, openAIError.Message, "effective_quality=auto")
	require.Contains(t, openAIError.Message, "n=2")
	require.NotContains(t, openAIError.Message, "operation=edits (requested edits; current supported values")
	require.Equal(t, explanation.Alternatives, metadata.Alternatives)
}

func TestImage2UnsupportedCombinationTieUsesNearestSameOperationProfiles(t *testing.T) {
	channel := image2ExactProfilesChannel(49,
		dto.Image2CapabilityProfile{Operation: "generations", Resolution: "2048", Size: "2048x2048", Quality: "high", MaxN: 2},
		dto.Image2CapabilityProfile{Operation: "edits", Resolution: "1024", Size: "1024x1024", Quality: "standard", MaxN: 2},
		dto.Image2CapabilityProfile{Operation: "edits", Resolution: "2048", Size: "2048x2048", Quality: "high", MaxN: 2},
	)
	router := newImage2SmartRouter(Image2RequestCapability{
		Operation: "edits", Resolution: "2048", Size: "1536x1024", Quality: "standard", N: 1,
	}, []*model.Channel{channel})
	require.Equal(t, 0, router.CandidateCount())
	explanation := router.Explain()
	// Quality is normalized to the provider default, so only the exact size
	// dimension remains relevant to the same-operation explanation.
	require.Equal(t, []string{"size"}, explanation.UnsupportedDimensions)
	require.Equal(t, []string{"edits"}, explanation.SupportedValues["operation"])
	require.Equal(t, []string{"1024", "2048"}, explanation.SupportedValues["resolution"])
	require.Equal(t, []string{"1024x1024", "2048x2048"}, explanation.SupportedValues["size"])
	require.Equal(t, []string{"auto"}, explanation.SupportedValues["quality"])
	require.Equal(t, []uint{1, 2}, explanation.SupportedValues["n"])
	require.False(t, explanation.CombinationUnsupported)
	require.Equal(t, "edits", explanation.Alternatives[0].Operation)
	require.EqualValues(t, 2, explanation.Alternatives[0].N)
	require.Len(t, explanation.Alternatives, 2)
	require.Equal(t, "edits", explanation.Alternatives[1].Operation, "same-operation alternatives must precede unrelated operation profiles")
}

func TestImage2AlternativesStayInOperationAndAreBounded(t *testing.T) {
	request := Image2RequestCapability{Operation: "generations", Resolution: "2048", Size: "1536x1536", Quality: "auto", N: 1}
	options := []image2CapabilityOption{
		{alternative: Image2Alternative{Operation: "generations", Size: "1024x1024", Quality: "auto", N: 1}, resolution: "1024", exactSize: true, maxN: 1},
		{alternative: Image2Alternative{Operation: "generations", Size: "2048x2048", Quality: "auto", N: 1}, resolution: "2048", exactSize: true, maxN: 1},
		{alternative: Image2Alternative{Operation: "generations", Size: "2048x1152", Quality: "auto", N: 1}, resolution: "2048", exactSize: true, maxN: 1},
		{alternative: Image2Alternative{Operation: "generations", Size: "1024x768", Quality: "auto", N: 1}, resolution: "1024", exactSize: true, maxN: 1},
		{alternative: Image2Alternative{Operation: "edits", Size: "1024x1024", Quality: "auto", N: 1}, resolution: "1024", exactSize: true, maxN: 1},
	}
	alternatives := image2Alternatives(request, options)
	require.Len(t, alternatives, 3)
	for _, alternative := range alternatives {
		require.Equal(t, "generations", alternative.Operation)
	}
}

func TestImage2LegacyUHDUsesVerifiedLandscapeSizeOnly(t *testing.T) {
	channel := image2ProfileChannel(32, "czeq-secret", 10, []string{"generations"}, []string{"uhd"}, nil, 1, false)
	landscape := newImage2SmartRouter(Image2RequestCapability{
		Operation: "generations", Resolution: "uhd", Size: "3840x2160", Quality: "auto", N: 1,
	}, []*model.Channel{channel})
	require.Equal(t, 1, landscape.CandidateCount())

	square := newImage2SmartRouter(Image2RequestCapability{
		Operation: "generations", Resolution: "uhd", Size: "4096x4096", Quality: "auto", N: 1,
	}, []*model.Channel{channel})
	require.Equal(t, 0, square.CandidateCount())
	explanation := square.Explain()
	require.Equal(t, []string{"size"}, explanation.UnsupportedDimensions)
	require.Equal(t, []string{"3840x2160"}, explanation.SupportedValues["size"])
	require.Equal(t, []Image2Alternative{{Operation: "generations", Size: "3840x2160", Quality: "auto", N: 1}}, explanation.Alternatives)
}

func TestImage2LegacyUHDProfileWithoutSizeUsesVerifiedLandscapeOnly(t *testing.T) {
	channel := image2ExactProfilesChannel(33,
		dto.Image2CapabilityProfile{Operation: "generations", Resolution: "uhd", Size: "", Quality: "high", MaxN: 1},
	)
	setting, err := channel.ParseSetting()
	require.NoError(t, err)
	setting.Image2Capability.Resolutions = []string{"uhd"}
	setting.Image2Capability.Qualities = []string{"high"}
	channel.SetSetting(setting)

	landscape := newImage2SmartRouter(Image2RequestCapability{
		Operation: "generations", Resolution: "uhd", Size: "3840x2160", Quality: "auto", N: 1,
	}, []*model.Channel{channel})
	require.Equal(t, 1, landscape.CandidateCount())

	square := newImage2SmartRouter(Image2RequestCapability{
		Operation: "generations", Resolution: "uhd", Size: "4096x4096", Quality: "auto", N: 1,
	}, []*model.Channel{channel})
	require.Equal(t, 0, square.CandidateCount())
}

func TestImage2TemporarilyUnavailableIs503WithoutUpstreamOrCharge(t *testing.T) {
	channel := image2ProfileChannel(47, "disabled-provider", 10, []string{"generations"}, []string{"1024"}, []string{"high"}, 2, false)
	channel.Status = common.ChannelStatusAutoDisabled
	router := newImage2SmartRouter(Image2RequestCapability{
		Operation: "generations", Resolution: "1024", Size: "1024x1024", Quality: "high", N: 2,
	}, []*model.Channel{channel})
	require.Equal(t, 0, router.CandidateCount())
	err := NewImage2PreRouteError(image2ContractContext("req-image2-503"), nil, router)
	openAIError, metadata := decodeImage2ErrorMetadata(t, err)
	require.Equal(t, http.StatusServiceUnavailable, err.StatusCode)
	require.Equal(t, types.ErrorCodeImage2TemporarilyUnavailable, err.GetErrorCode())
	require.Empty(t, metadata.UnsupportedDimensions)
	require.NotEmpty(t, metadata.Alternatives)
	require.False(t, metadata.UpstreamCalled)
	require.False(t, metadata.Charged)
	require.Contains(t, openAIError.Message, "temporarily unavailable")
	require.Contains(t, openAIError.Message, "upstream_called=false")
	require.Contains(t, openAIError.Message, "charged=false")
	require.NotContains(t, openAIError.Message, "disabled-provider")
}

func TestNormalizeImage2UpstreamStatusOnlyProducesGatewayErrorsAfterAttempt(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		timedOut   bool
		wantStatus int
	}{
		{name: "upstream client error", status: http.StatusBadRequest, wantStatus: http.StatusBadRequest},
		{name: "upstream capacity", status: http.StatusTooManyRequests, wantStatus: http.StatusTooManyRequests},
		{name: "bad gateway response", status: http.StatusBadGateway, wantStatus: http.StatusBadGateway},
		{name: "gateway timeout", status: http.StatusGatewayTimeout, wantStatus: http.StatusGatewayTimeout},
		{name: "provider 524", status: 524, wantStatus: http.StatusGatewayTimeout},
		{name: "transport timeout", timedOut: true, wantStatus: http.StatusGatewayTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := types.NewErrorWithStatusCode(errors.New("upstream failure"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
			require.Same(t, err, NormalizeImage2UpstreamStatus(err, test.status, test.timedOut))
			require.Equal(t, test.wantStatus, err.StatusCode)
		})
	}
}

func TestNormalizeImage2UpstreamStatusRedactsSupplierDetailsFromClientError(t *testing.T) {
	err := types.NewOpenAIError(errors.New("provider-secret channel-44 https://supplier.example/v1/key"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	NormalizeImage2UpstreamStatus(err, http.StatusBadGateway, false)
	openAIError := err.ToOpenAIError()
	require.Equal(t, http.StatusBadGateway, err.StatusCode)
	require.NotContains(t, openAIError.Message, "provider-secret")
	require.NotContains(t, openAIError.Message, "channel-44")
	require.NotContains(t, openAIError.Message, "supplier.example")
	require.Contains(t, openAIError.Message, "invalid response")
}

func TestImage2Upstream429IsPreservedButNeverReplayed(t *testing.T) {
	err := types.NewErrorWithStatusCode(errors.New("capacity"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	NormalizeImage2UpstreamStatus(err, http.StatusTooManyRequests, false)
	require.Equal(t, http.StatusTooManyRequests, err.StatusCode)
	decision := EvaluateImage2SafeFailover(SafeFailoverInput{
		RelayMode: relayconstant.RelayModeImagesGenerations,
		ModelName: "gpt-image-2",
		Error:     err,
	})
	require.False(t, decision.Retry)
	require.Equal(t, "image2_upstream_capacity_no_replay", decision.Reason)
	require.Contains(t, err.ToOpenAIError().Message, "status 429")
}

func TestImage2FakeUpstream429StopsAttemptChainWithoutReplay(t *testing.T) {
	serverCalls := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		serverCalls++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = strings.NewReader(`{"error":{"message":"capacity"}}`).WriteTo(w)
	}))
	t.Cleanup(fake.Close)
	router := newImage2SmartRouter(Image2RequestCapability{
		Operation: "generations", Resolution: "1024", Size: "1024x1024", Quality: "standard", N: 1,
	}, []*model.Channel{
		image2ProfileChannel(51, "first", 10, []string{"generations"}, []string{"1024"}, []string{"standard"}, 1, false),
		image2ProfileChannel(52, "second", 20, []string{"generations"}, []string{"1024"}, []string{"standard"}, 1, false),
	})
	attempts := 0
	for {
		_, routeErr := router.Next()
		require.Nil(t, routeErr)
		attempts++
		response, requestErr := http.Get(fake.URL)
		require.NoError(t, requestErr)
		response.Body.Close()
		err := types.NewErrorWithStatusCode(errors.New("capacity"), types.ErrorCodeBadResponseStatusCode, response.StatusCode)
		NormalizeImage2UpstreamStatus(err, response.StatusCode, false)
		decision := EvaluateImage2SafeFailover(SafeFailoverInput{
			RelayMode: relayconstant.RelayModeImagesGenerations,
			ModelName: "gpt-image-2",
			Error:     err,
		})
		require.Equal(t, http.StatusTooManyRequests, err.StatusCode)
		if !decision.Retry {
			require.Equal(t, "image2_upstream_capacity_no_replay", decision.Reason)
			break
		}
	}
	require.Equal(t, 1, attempts)
	require.Equal(t, 1, serverCalls)
}

func TestParseImage2RequestCapabilityRejectsInvalidZeroNAndQuality(t *testing.T) {
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-image-2", RelayMode: relayconstant.RelayModeImagesGenerations}
	zero := uint(0)
	_, err := ParseImage2RequestCapability(info, &dto.ImageRequest{N: &zero})
	require.Error(t, err)
	require.Contains(t, err.Error(), "n")
	_, err = ParseImage2RequestCapability(info, &dto.ImageRequest{Quality: "ultra"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "quality")
}
