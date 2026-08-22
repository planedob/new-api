package helper

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestModelMappedHelperPreservesImage2OriginAndMapsUpstreamModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("model_mapping", `{"gpt-image-2":"gpt-image-2-adobe"}`)

	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-2",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-image-2",
		},
	}
	request := &dto.ImageRequest{Model: "gpt-image-2", Prompt: "a cat"}

	require.NoError(t, ModelMappedHelper(ctx, info, request))
	require.Equal(t, "gpt-image-2", info.OriginModelName)
	require.Equal(t, "gpt-image-2-adobe", info.ChannelMeta.UpstreamModelName)
	require.Equal(t, "gpt-image-2-adobe", request.Model)
}
