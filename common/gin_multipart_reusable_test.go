package common

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
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

func TestParseMultipartFormReusableAppendsEqualExistingValuesExactlyOnce(t *testing.T) {
	context := reusableMultipartContext(t)
	context.Request.PostForm = url.Values{"tag": {"first"}}
	context.Request.Form = url.Values{"tag": {"first"}, "existing": {"kept"}}

	first, err := ParseMultipartFormReusable(context)
	require.NoError(t, err)
	second, err := ParseMultipartFormReusable(context)
	require.NoError(t, err)

	require.Same(t, first, second)
	require.Equal(t, []string{"first", "first", "first", "second"}, context.Request.PostForm["tag"])
	require.Equal(t, []string{"first", "first", "first", "second"}, context.Request.Form["tag"])
	require.Equal(t, []string{"kept"}, context.Request.Form["existing"])
}

func TestIsMultipartFormDataIsCaseInsensitive(t *testing.T) {
	require.True(t, IsMultipartFormData("Multipart/Form-Data; Boundary=case-sensitive-value"))
	require.True(t, IsMultipartFormData("MULTIPART/FORM-DATA"))
	require.False(t, IsMultipartFormData("application/json"))
}

func TestCleanupBodyStorageClearsMultipartContentTypeState(t *testing.T) {
	context := reusableMultipartContext(t)
	_, err := ParseMultipartFormReusable(context)
	require.NoError(t, err)
	require.NotNil(t, context.Value(keyOriginalMultipartContentType))

	CleanupBodyStorage(context)
	require.Nil(t, context.Value(keyOriginalMultipartContentType))
}

func TestClassifyImage2RequestValidationErrorUsesStableSafeContracts(t *testing.T) {
	tests := []struct {
		err      error
		wantCode string
		wantHTTP int
		unsafe   string
	}{
		{err: errBoundaryNotFound, wantCode: Image2ValidationMissingBoundary, wantHTTP: 400, unsafe: "boundary not found"},
		{err: io.ErrUnexpectedEOF, wantCode: Image2ValidationTruncatedBody, wantHTTP: 400, unsafe: "unexpected EOF"},
		{err: ErrRequestBodyUnavailable, wantCode: Image2ValidationBodyUnavailable, wantHTTP: 400},
		{err: ErrImageInputRequired, wantCode: Image2ValidationMissingImage, wantHTTP: 400},
		{err: ErrImageInputEmpty, wantCode: Image2ValidationEmptyImage, wantHTTP: 400},
		{err: ErrImageInputUnsupported, wantCode: Image2ValidationUnsupported, wantHTTP: 400},
		{err: ErrRequestBodyTooLarge, wantCode: Image2ValidationTooLarge, wantHTTP: 413},
		{err: errors.New("parser detail must stay private"), wantCode: Image2ValidationMalformed, wantHTTP: 400, unsafe: "parser detail"},
	}
	for _, test := range tests {
		code, message, statusCode := ClassifyImage2RequestValidationError(test.err)
		require.Equal(t, test.wantCode, code)
		require.Equal(t, test.wantHTTP, statusCode)
		if test.unsafe != "" {
			require.NotContains(t, message, test.unsafe)
		}
	}
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
