package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImage2OversizeRequestFailsBeforeLegacyChannelSelection(t *testing.T) {
	old := common.Image2SmartRoutingEnabled
	common.Image2SmartRoutingEnabled = true
	t.Cleanup(func() { common.Image2SmartRoutingEnabled = old })
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-2",
		RelayMode:       relayconstant.RelayModeImagesGenerations,
	}
	for _, size := range []string{"4097x4096", "8192x8192"} {
		t.Run(size, func(t *testing.T) {
			validationErr := image2SmartRequestValidationError(info, &dto.ImageRequest{Size: size})
			require.NotNil(t, validationErr)
			require.Equal(t, http.StatusBadRequest, validationErr.StatusCode)
			require.Equal(t, types.ErrorCodeInvalidRequest, validationErr.GetErrorCode())
			require.True(t, types.IsSkipRetryError(validationErr))
		})
	}
}

func TestRelayImage2OversizeRequestFailsAtRequestEntryWithoutSideEffects(t *testing.T) {
	old := common.Image2SmartRoutingEnabled
	common.Image2SmartRoutingEnabled = true
	t.Cleanup(func() { common.Image2SmartRoutingEnabled = old })

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/images/generations",
		strings.NewReader(`{"model":"gpt-image-2","size":"8192x8192"}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(ctx, constant.ContextKeyOriginalModel, "gpt-image-2")
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
	ctx.Set("use_channel", []string{})

	Relay(ctx, types.RelayFormatOpenAIImage)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Empty(t, ctx.GetStringSlice("use_channel"), "invalid Image2 size must not select a channel")
	_, routerActive := ctx.Get("image2_smart_router_active")
	require.False(t, routerActive, "invalid Image2 size must not activate smart routing")
}

func TestImage2PreRouteErrorMetadataIsQueryableAndSanitized(t *testing.T) {
	for _, requestPath := range []string{"/v1/images/edits", "/pg/images/edits"} {
		t.Run(requestPath, func(t *testing.T) {
			request := service.Image2RequestCapability{Operation: "edits", Resolution: "1024", Quality: "auto", N: 1}
			routeErr := types.NewErrorWithStatusCode(
				fmt.Errorf("no compatible Image2 channel remains"),
				types.ErrorCodeGetChannelFailed,
				http.StatusInternalServerError,
				types.ErrOptionWithSkipRetry(),
			)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, requestPath, strings.NewReader("secret image payload"))
			common.SetContextKey(ctx, constant.ContextKeyAutoGroup, "resolved-auto-group")
			metadata := image2PreRouteErrorMetadata(ctx, &relaycommon.RelayInfo{
				OriginModelName: "gpt-image-2",
				UsingGroup:      "stale-using-group",
				TokenGroup:      "auto",
			}, request, 0, "44:operation_unsupported", routeErr)

			require.Equal(t, "channel_selection", metadata["stage"])
			require.Equal(t, "edits", metadata["operation"])
			require.Equal(t, 0, metadata["candidate_count"])
			require.Equal(t, false, metadata["upstream_called"])
			require.Equal(t, 0, metadata["quota"])
			require.Equal(t, requestPath, metadata["request_path"])
			require.Equal(t, "resolved-auto-group", metadata["group"])
			encoded := common.MapToJsonStr(metadata)
			require.NotContains(t, encoded, "secret image payload")
		})
	}
}
