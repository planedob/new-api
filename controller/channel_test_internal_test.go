package controller

import (
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeChannelTestEndpointInfersImage2Generation(t *testing.T) {
	channel := &model.Channel{}
	for _, modelName := range []string{"gpt-image-2", "gpt-image-2-adobe", "image-2-1k", "dall-e-3"} {
		t.Run(modelName, func(t *testing.T) {
			require.Equal(t, string(constant.EndpointTypeImageGeneration), normalizeChannelTestEndpoint(channel, modelName, ""))
		})
	}
}

func TestChannelTestRelayInfoCanAcceptUnpricedCandidateWithoutGlobalMutation(t *testing.T) {
	info := &relaycommon.RelayInfo{}
	untouched := &relaycommon.RelayInfo{}

	prepareChannelTestRelayInfo(nil)
	prepareChannelTestRelayInfo(info)

	assert.True(t, info.IsChannelTest)
	assert.True(t, info.UserSetting.AcceptUnsetRatioModel)
	assert.False(t, untouched.IsChannelTest)
	assert.False(t, untouched.UserSetting.AcceptUnsetRatioModel)
}

func TestChannelTestUnpricedCandidateBypassesOnlySyntheticRelayInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModelListControllerTestDB(t)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/test/82", nil)
	ctx.Set("group", "default")

	newInfo := func() *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{
			UserId:          1,
			OriginModelName: "aibuff-channel-test-unpriced-candidate-prod-base",
			UserGroup:       "default",
			UsingGroup:      "default",
		}
	}
	ordinaryInfo := newInfo()
	_, err := relayhelper.ModelPriceHelper(ctx, ordinaryInfo, 0, &types.TokenCountMeta{})
	require.Error(t, err)
	require.ErrorContains(t, err, ordinaryInfo.OriginModelName)

	testInfo := newInfo()
	prepareChannelTestRelayInfo(testInfo)
	priceData, err := relayhelper.ModelPriceHelper(ctx, testInfo, 0, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.False(t, priceData.UsePrice)
	assert.False(t, ordinaryInfo.UserSetting.AcceptUnsetRatioModel)
}

func TestNormalizeChannelTestEndpointReusesSharedChannelProtocol(t *testing.T) {
	for _, test := range []struct {
		name        string
		channelType int
		modelName   string
		want        string
	}{
		{name: "anthropic", channelType: constant.ChannelTypeAnthropic, modelName: "claude-3-7-sonnet", want: string(constant.EndpointTypeAnthropic)},
		{name: "gemini", channelType: constant.ChannelTypeGemini, modelName: "gemini-2.5-flash", want: string(constant.EndpointTypeGemini)},
		{name: "image2", channelType: constant.ChannelTypeOpenAI, modelName: "gpt-image-2-wc", want: string(constant.EndpointTypeImageGeneration)},
	} {
		t.Run(test.name, func(t *testing.T) {
			channel := &model.Channel{Type: test.channelType}
			require.Equal(t, test.want, normalizeChannelTestEndpoint(channel, test.modelName, ""))
		})
	}
}

func TestNormalizeChannelTestEndpointDoesNotOverrideExplicitEndpoint(t *testing.T) {
	channel := &model.Channel{}
	require.Equal(t, string(constant.EndpointTypeOpenAI), normalizeChannelTestEndpoint(channel, "gpt-image-2", string(constant.EndpointTypeOpenAI)))
}

func TestChannelTestImageEditsUsesMultipartFixedReferenceWithoutGenerationFallback(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", nil)

	request, ok := buildTestRequest("gpt-image-2", string(constant.EndpointTypeImageEdits), &model.Channel{}, false).(*dto.ImageRequest)
	require.True(t, ok)
	require.NoError(t, prepareChannelTestImageEditRequest(c, request))
	require.Contains(t, c.Request.Header.Get("Content-Type"), "multipart/form-data")
	require.Equal(t, "/v1/images/edits", c.Request.URL.Path)

	require.NoError(t, c.Request.ParseMultipartForm(1<<20))
	require.Equal(t, "gpt-image-2", c.Request.PostForm.Get("model"))
	require.Equal(t, "1", c.Request.PostForm.Get("n"))
	require.Equal(t, "1024x1024", c.Request.PostForm.Get("size"))
	files := c.Request.MultipartForm.File["image"]
	require.Len(t, files, 1)
	file, err := files[0].Open()
	require.NoError(t, err)
	defer file.Close()
	decoded, err := png.Decode(file)
	require.NoError(t, err)
	assert.Equal(t, 256, decoded.Bounds().Dx())
	assert.Equal(t, 256, decoded.Bounds().Dy())

}

func TestImageEditsEndpointHasDistinctSharedPath(t *testing.T) {
	info, ok := common.GetDefaultEndpointInfo(constant.EndpointTypeImageEdits)
	require.True(t, ok)
	assert.Equal(t, "/v1/images/edits", info.Path)
	assert.Equal(t, http.MethodPost, info.Method)
}

func TestBuildImage2ChannelTestEvidenceBindsActualProbeBytes(t *testing.T) {
	channel := &model.Channel{Id: 82}
	capability := &dto.Image2ChannelCapability{
		Enabled: true, Operations: []string{"generations", "edits"}, Resolutions: []string{"1024"},
		MaxN: 1, EditsAccepted: true,
	}
	channel.SetSetting(dto.ChannelSettings{Image2Capability: capability})
	testedAt := time.Date(2026, time.August, 23, 15, 0, 0, 0, time.UTC)
	evidence, evidenceDigest, err := buildImage2ChannelTestEvidence(
		channel,
		constant.EndpointTypeImageEdits,
		testedAt,
		[]byte("exact multipart request bytes"),
		[]byte("exact response bytes"),
	)
	require.NoError(t, err)
	require.NotNil(t, evidence)
	assert.Equal(t, channel.Id, evidence.ChannelID)
	assert.Equal(t, "edits", evidence.Operation)
	assert.Equal(t, "/v1/images/edits", evidence.Endpoint)
	assert.Equal(t, 1, int(evidence.RequestCount))
	assert.Equal(t, http.StatusOK, evidence.StatusCode)
	assert.NotEmpty(t, evidence.RequestSHA256)
	assert.NotEmpty(t, evidence.ResponseSHA256)
	recomputed, err := dto.Image2FixedChannelTestEvidenceSHA256(*evidence)
	require.NoError(t, err)
	assert.Equal(t, recomputed, evidenceDigest)
}

func TestIsImageGenerationChannelTestModelDoesNotBroadenToVisionModels(t *testing.T) {
	require.True(t, common.IsImageGenerationModel("gpt-image-2-adobe"))
	require.True(t, common.IsImageGenerationModel("image-2-1k"))
	require.False(t, common.IsImageGenerationModel("gpt-4o"))
	require.False(t, common.IsImageGenerationModel("gpt-4o-mini-vision"))
}

func TestSettleTestQuotaUsesTieredBilling(t *testing.T) {
	info := &relaycommon.RelayInfo{
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:   "tiered_expr",
			ExprString:    `param("stream") == true ? tier("stream", p * 3) : tier("base", p * 2)`,
			ExprHash:      billingexpr.ExprHashString(`param("stream") == true ? tier("stream", p * 3) : tier("base", p * 2)`),
			GroupRatio:    1,
			EstimatedTier: "stream",
			QuotaPerUnit:  common.QuotaPerUnit,
			ExprVersion:   1,
		},
		BillingRequestInput: &billingexpr.RequestInput{
			Body: []byte(`{"stream":true}`),
		},
	}

	quota, result := settleTestQuota(info, types.PriceData{
		ModelRatio:      1,
		CompletionRatio: 2,
	}, &dto.Usage{
		PromptTokens: 1000,
	})

	require.Equal(t, 1500, quota)
	require.NotNil(t, result)
	require.Equal(t, "stream", result.MatchedTier)
}

func TestBuildTestLogOtherInjectsTieredInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	info := &relaycommon.RelayInfo{
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode: "tiered_expr",
			ExprString:  `tier("base", p * 2)`,
		},
		ChannelMeta: &relaycommon.ChannelMeta{},
	}
	priceData := types.PriceData{
		GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
	}
	usage := &dto.Usage{
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 12,
		},
	}

	other := buildTestLogOther(ctx, info, priceData, usage, &billingexpr.TieredResult{
		MatchedTier: "base",
	})

	require.Equal(t, "tiered_expr", other["billing_mode"])
	require.Equal(t, "base", other["matched_tier"])
	require.NotEmpty(t, other["expr_b64"])
}
