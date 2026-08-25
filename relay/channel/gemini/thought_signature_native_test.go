package gemini

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestConvertGeminiRequestPreservesModelThoughtSignaturePartOrderAndFunctionCall(t *testing.T) {
	body := []byte(`{"contents":[{"role":"model","parts":[{"text":"before"},{"functionCall":{"name":"lookup","args":{"q":"x"}},"thoughtSignature":"model-\u0073ignature"},{"text":"after"}]}]}`)
	var request dto.GeminiChatRequest
	require.NoError(t, common.Unmarshal(body, &request))
	context, _ := gin.CreateTestContext(httptest.NewRecorder())

	converted, err := (&Adaptor{}).ConvertGeminiRequest(context, &relaycommon.RelayInfo{}, &request)
	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeminiChatRequest)
	require.True(t, ok)

	marshaled, err := common.Marshal(convertedRequest)
	require.NoError(t, err)
	var roundTripped dto.GeminiChatRequest
	require.NoError(t, common.Unmarshal(marshaled, &roundTripped))
	require.Len(t, roundTripped.Contents[0].Parts, 3)
	require.Equal(t, "before", roundTripped.Contents[0].Parts[0].Text)
	require.NotNil(t, roundTripped.Contents[0].Parts[1].FunctionCall)
	require.True(t, bytes.Equal([]byte(`"model-\u0073ignature"`), roundTripped.Contents[0].Parts[1].ThoughtSignature))
	require.Equal(t, "after", roundTripped.Contents[0].Parts[2].Text)
}
