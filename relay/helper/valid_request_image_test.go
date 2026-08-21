package helper

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func imageEditMultipartContext(t *testing.T, filename string, data []byte, mask []byte) *gin.Context {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"model":  "gpt-image-2",
		"prompt": "turn the image purple",
	} {
		require.NoError(t, writer.WriteField(key, value))
	}
	part, err := writer.CreateFormFile("image", filename)
	require.NoError(t, err)
	_, err = part.Write(data)
	require.NoError(t, err)
	if mask != nil {
		maskPart, maskErr := writer.CreateFormFile("mask", "mask.png")
		require.NoError(t, maskErr)
		_, maskErr = maskPart.Write(mask)
		require.NoError(t, maskErr)
	}
	require.NoError(t, writer.Close())

	request := httptest.NewRequest("POST", "/v1/images/edits", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	return context
}

func TestGetAndValidOpenAIImageRequestUsesImageBytesNotFilename(t *testing.T) {
	// A minimal PNG signature plus a valid IHDR-shaped body is enough for
	// net/http's content sniffer; the filename deliberately claims JPEG.
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	context := imageEditMultipartContext(t, "reference.jpg", png, nil)

	request, err := GetAndValidOpenAIImageRequest(context, relayconstant.RelayModeImagesEdits)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", request.Model)
	require.NotNil(t, context.Request.MultipartForm)
}

func TestGetAndValidOpenAIImageRequestRejectsEmptyImage(t *testing.T) {
	context := imageEditMultipartContext(t, "empty.png", nil, nil)

	_, err := GetAndValidOpenAIImageRequest(context, relayconstant.RelayModeImagesEdits)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty")
}

func TestGetAndValidOpenAIImageRequestRejectsSpoofedImage(t *testing.T) {
	context := imageEditMultipartContext(t, "reference.png", []byte("not an image"), nil)

	_, err := GetAndValidOpenAIImageRequest(context, relayconstant.RelayModeImagesEdits)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported image content")
}

func TestGetAndValidOpenAIImageRequestRejectsOversizedImage(t *testing.T) {
	previousLimit := constant.MaxImage2InputMB
	constant.MaxImage2InputMB = 1
	t.Cleanup(func() { constant.MaxImage2InputMB = previousLimit })

	data := bytes.Repeat([]byte("x"), 1<<20+1)
	context := imageEditMultipartContext(t, "large.png", data, nil)

	_, err := GetAndValidOpenAIImageRequest(context, relayconstant.RelayModeImagesEdits)
	require.Error(t, err)
	require.ErrorIs(t, err, common.ErrRequestBodyTooLarge)
}
