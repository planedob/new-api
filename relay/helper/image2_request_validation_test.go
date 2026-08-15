package helper

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGptImage2ExplicitZeroNIsRejectedByRequestParser(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2","n":0}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	_, err := GetAndValidOpenAIImageRequest(ctx, relayconstant.Path2RelayMode("/v1/images/generations"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "n must be a positive integer")
}
