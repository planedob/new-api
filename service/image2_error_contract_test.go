package service

import (
	"errors"
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
	require.Equal(t, "high", metadata.Quality)
	require.EqualValues(t, 2, metadata.N)
	require.Equal(t, []string{"operation", "size", "quality", "n"}, metadata.UnsupportedDimensions)
	require.False(t, metadata.UpstreamCalled)
	require.False(t, metadata.Charged)
	require.Len(t, metadata.Alternatives, 2)
	require.Len(t, metadata.SafeAlternatives, 2)
	require.Contains(t, metadata.Alternatives, Image2Alternative{Operation: "generations", Size: "1024x1024", Quality: "standard", N: 1})
	require.Contains(t, openAIError.Message, "operation=edits")
	require.Contains(t, openAIError.Message, "size=4096x4096")
	require.Contains(t, openAIError.Message, "quality=high")
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
	require.Equal(t, []string{"operation", "size", "quality", "n"}, noAlternativeMetadata.UnsupportedDimensions)
	require.NotContains(t, noAlternativeOpenAI.Message, "hidden-provider")
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
