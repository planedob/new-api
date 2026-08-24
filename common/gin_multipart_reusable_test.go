package common

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func reusableMultipartContext(t *testing.T) *gin.Context {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("tag", "first"))
	require.NoError(t, writer.WriteField("tag", "first"))
	require.NoError(t, writer.WriteField("tag", "second"))
	part, err := writer.CreateFormFile("image", "reference.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	request := httptest.NewRequest("POST", "/v1/images/edits?trace=query", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	t.Cleanup(func() {
		CleanupBodyStorage(context)
		if request.MultipartForm != nil {
			_ = request.MultipartForm.RemoveAll()
		}
	})
	return context
}

func TestParseMultipartFormReusableIsIdempotentAndPreservesValues(t *testing.T) {
	context := reusableMultipartContext(t)

	first, err := ParseMultipartFormReusable(context)
	require.NoError(t, err)
	second, err := ParseMultipartFormReusable(context)
	require.NoError(t, err)

	require.Same(t, first, second)
	require.Same(t, first, context.Request.MultipartForm)
	require.Equal(t, []string{"first", "first", "second"}, context.Request.PostForm["tag"])
	require.Equal(t, []string{"query"}, context.Request.Form["trace"])
	require.Equal(t, []string{"gpt-image-2"}, context.Request.Form["model"])
	require.Equal(t, []string{"first", "first", "second"}, context.Request.Form["tag"])
}

func TestParseMultipartFormReusableReusesStdlibForm(t *testing.T) {
	context := reusableMultipartContext(t)
	require.NoError(t, context.Request.ParseMultipartForm(1<<20))
	stdlibForm := context.Request.MultipartForm

	form, err := ParseMultipartFormReusable(context)
	require.NoError(t, err)
	require.Same(t, stdlibForm, form)
	require.Equal(t, []string{"first", "first", "second"}, context.Request.PostForm["tag"])
	require.Equal(t, []string{"query"}, context.Request.Form["trace"])
}

func TestGetRequestBodyRejectsPreviouslyDrainedPositiveLengthBody(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/images/edits", bytes.NewReader([]byte("multipart bytes")))
	request.Body = io.NopCloser(bytes.NewReader(nil))
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request

	_, err := GetRequestBody(context)
	require.ErrorIs(t, err, ErrRequestBodyUnavailable)
	storage, _ := context.Get(KeyBodyStorage)
	require.Nil(t, storage)
}

func TestGetRequestBodyDoesNotRequireContentLengthEquality(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/images/edits", bytes.NewReader([]byte("decoded-body")))
	request.ContentLength = 128
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	t.Cleanup(func() { CleanupBodyStorage(context) })

	storage, err := GetBodyStorage(context)
	require.NoError(t, err)
	require.EqualValues(t, len("decoded-body"), storage.Size())
}
