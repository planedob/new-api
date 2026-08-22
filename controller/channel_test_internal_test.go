package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
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
