package types

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewAPIErrorMetadataIsSerializedToOpenAIError(t *testing.T) {
	err := NewErrorWithStatusCode(
		errors.New("explainable error"),
		ErrorCodeUnsupportedImageConfiguration,
		http.StatusUnprocessableEntity,
		ErrOptionWithMetadata(map[string]any{
			"operation":       "edits",
			"unsupported":     []string{"size", "quality"},
			"upstream_called": false,
			"charged":         false,
		}),
	)
	openAIError := err.ToOpenAIError()
	require.Equal(t, ErrorCodeUnsupportedImageConfiguration, openAIError.Code)
	require.NotEmpty(t, openAIError.Metadata)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(openAIError.Metadata, &metadata))
	require.Equal(t, "edits", metadata["operation"])
	require.Equal(t, false, metadata["upstream_called"])
	require.Equal(t, false, metadata["charged"])
}
