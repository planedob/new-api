package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRelayGeminiThoughtSignatureRejectsBeforeUpstreamWithPassThroughOnOrOff(t *testing.T) {
	previousPassThrough := model_setting.GetGlobalSettings().PassThroughRequestEnabled
	t.Cleanup(func() { model_setting.GetGlobalSettings().PassThroughRequestEnabled = previousPassThrough })

	tests := []struct {
		name        string
		path        string
		passThrough bool
		body        string
	}{
		{
			name:        "non-stream pass-through off user role",
			path:        "/v1beta/models/gemini-3-pro-image-preview:generateContent",
			passThrough: false,
			body:        `{"contents":[{"role":"user","parts":[{"text":"edit","thoughtSignature":"do-not-echo"}]}]}`,
		},
		{
			name:        "stream pass-through on default user role",
			path:        "/v1beta/models/gemini-3-pro-image-preview:streamGenerateContent?alt=sse",
			passThrough: true,
			body:        `{"contents":[{"parts":[{"text":"edit","thoughtSignature":"do-not-echo"}]}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model_setting.GetGlobalSettings().PassThroughRequestEnabled = test.passThrough
			request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(test.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = request

			Relay(context, types.RelayFormatGemini)
			common.CleanupBodyStorage(context)

			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			require.False(t, common.GetContextKeyBool(context, constant.ContextKeyUpstreamCalled))
			require.Contains(t, recorder.Body.String(), "thoughtSignature")
			require.NotContains(t, recorder.Body.String(), "do-not-echo")
		})
	}
}
