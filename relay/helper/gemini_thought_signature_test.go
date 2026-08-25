package helper

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func geminiThoughtSignatureContext(t *testing.T, path string, body []byte) *gin.Context {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	t.Cleanup(func() { common.CleanupBodyStorage(context) })
	return context
}

func TestGetAndValidateGeminiRequestRejectsInvalidThoughtSignature(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{
			name: "stream user signature",
			path: "/v1beta/models/gemini-3-pro-image-preview:streamGenerateContent?alt=sse",
			body: `{"contents":[{"role":"user","parts":[{"text":"edit","thoughtSignature":"user-signature"}]}]}`,
		},
		{
			name: "non-stream default user signature",
			path: "/v1beta/models/gemini-3-pro-image-preview:generateContent",
			body: `{"contents":[{"parts":[{"text":"edit","thoughtSignature":"default-user-signature"}]}]}`,
		},
		{
			name: "null signature",
			path: "/v1beta/models/gemini-3-pro-image-preview:generateContent",
			body: `{"contents":[{"role":"model","parts":[{"thoughtSignature":null}]}]}`,
		},
		{
			name: "empty signature",
			path: "/v1beta/models/gemini-3-pro-image-preview:generateContent",
			body: `{"contents":[{"role":"model","parts":[{"thoughtSignature":""}]}]}`,
		},
		{
			name: "object signature",
			path: "/v1beta/models/gemini-3-pro-image-preview:generateContent",
			body: `{"contents":[{"role":"model","parts":[{"thoughtSignature":{}}]}]}`,
		},
		{
			name: "array signature",
			path: "/v1beta/models/gemini-3-pro-image-preview:generateContent",
			body: `{"contents":[{"role":"model","parts":[{"thoughtSignature":[]}]}]}`,
		},
		{
			name: "number signature",
			path: "/v1beta/models/gemini-3-pro-image-preview:generateContent",
			body: `{"contents":[{"role":"model","parts":[{"thoughtSignature":1}]}]}`,
		},
		{
			name: "boolean signature",
			path: "/v1beta/models/gemini-3-pro-image-preview:generateContent",
			body: `{"contents":[{"role":"model","parts":[{"thoughtSignature":true}]}]}`,
		},
		{
			name: "whitespace-only signature",
			path: "/v1beta/models/gemini-3-pro-image-preview:generateContent",
			body: `{"contents":[{"role":"model","parts":[{"thoughtSignature":"   "}]}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := GetAndValidateGeminiRequest(geminiThoughtSignatureContext(t, test.path, []byte(test.body)))
			require.Nil(t, request)
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			require.Contains(t, err.Error(), "thoughtSignature")
			require.NotContains(t, err.Error(), "user-signature")
			require.NotContains(t, err.Error(), "default-user-signature")
		})
	}
}

func TestGetAndValidateGeminiRequestPreservesModelSignatureAndPartOrder(t *testing.T) {
	body := []byte(`{"contents":[{"role":"model","parts":[{"text":"before"},{"functionCall":{"name":"lookup","args":{"q":"x"}},"thoughtSignature":"model-\u0073ignature"},{"text":"after"}]}]}`)
	context := geminiThoughtSignatureContext(t, "/v1beta/models/gemini-3-pro-image-preview:generateContent", body)

	request, err := GetAndValidateGeminiRequest(context)
	require.NoError(t, err)
	require.False(t, request.IsStream(context))
	require.Len(t, request.Contents, 1)
	require.Len(t, request.Contents[0].Parts, 3)
	require.True(t, bytes.Equal([]byte(`"model-\u0073ignature"`), request.Contents[0].Parts[1].ThoughtSignature))
	require.NotNil(t, request.Contents[0].Parts[1].FunctionCall)

	storage, err := common.GetBodyStorage(context)
	require.NoError(t, err)
	storedBody, err := storage.Bytes()
	require.NoError(t, err)
	require.Equal(t, body, storedBody)

	copyRequest, err := common.DeepCopy(request)
	require.NoError(t, err)
	marshaled, err := common.Marshal(copyRequest)
	require.NoError(t, err)
	var roundTripped dto.GeminiChatRequest
	require.NoError(t, common.Unmarshal(marshaled, &roundTripped))
	require.Equal(t, []string{"before", "", "after"}, []string{
		roundTripped.Contents[0].Parts[0].Text,
		roundTripped.Contents[0].Parts[1].Text,
		roundTripped.Contents[0].Parts[2].Text,
	})
	require.Equal(t, request.Contents[0].Parts[1].ThoughtSignature, roundTripped.Contents[0].Parts[1].ThoughtSignature)
	require.NotNil(t, roundTripped.Contents[0].Parts[1].FunctionCall)
}

func TestGetAndValidateGeminiRequestPreservesStreamModelSignature(t *testing.T) {
	body := []byte(`{"contents":[{"role":"model","parts":[{"text":"chunk","thoughtSignature":"stream-model-signature"}]}]}`)
	context := geminiThoughtSignatureContext(t, "/v1beta/models/gemini-3-pro-image-preview:streamGenerateContent?alt=sse", body)

	request, err := GetAndValidateGeminiRequest(context)
	require.NoError(t, err)
	require.True(t, request.IsStream(context))
	require.True(t, bytes.Equal([]byte(`"stream-model-signature"`), request.Contents[0].Parts[0].ThoughtSignature))
}
