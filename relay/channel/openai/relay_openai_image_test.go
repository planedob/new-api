package openai

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestDetectImageMimeTypeFromFileUsesBytesNotFilename(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", "reference.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	form, err := multipart.NewReader(bytes.NewReader(body.Bytes()), writer.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer form.RemoveAll()
	file, err := form.File["image"][0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	mimeType, err := detectImageMimeTypeFromFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "image/png" {
		t.Fatalf("detected MIME = %q, want image/png", mimeType)
	}
	firstByte := make([]byte, 1)
	if _, err := file.Read(firstByte); err != nil {
		t.Fatal(err)
	}
	if firstByte[0] != 0x89 {
		t.Fatalf("file was not rewound; first byte = %#x", firstByte[0])
	}
}

func TestDetectImageMimeTypeFromFilePreservesCompletePayload(t *testing.T) {
	original := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte("payload"), 200)...)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", "reference.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(original); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	form, err := multipart.NewReader(bytes.NewReader(body.Bytes()), writer.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer form.RemoveAll()
	file, err := form.File["image"][0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	if _, err := detectImageMimeTypeFromFile(file); err != nil {
		t.Fatal(err)
	}
	copyAfterDetection, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(copyAfterDetection, original) {
		t.Fatalf("payload changed after MIME detection: got %d bytes, want %d", len(copyAfterDetection), len(original))
	}
}

func TestOpenaiHandlerWithUsageRejectsEmptyImageDataBeforeWritingResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	response := &http.Response{Body: io.NopCloser(strings.NewReader(`{"created":1,"data":[]}`))}

	usage, apiErr := OpenaiHandlerWithUsage(context, &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeImagesGenerations,
		ChannelMeta: &relaycommon.ChannelMeta{},
	}, response)
	if usage != nil || apiErr == nil {
		t.Fatalf("empty image response = usage %#v, error %#v; want error before success", usage, apiErr)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("empty image response was written to client: %q", recorder.Body.String())
	}
}

func TestOpenaiHandlerWithUsageAcceptsURLOrBase64ImageData(t *testing.T) {
	for _, responseBody := range []string{
		`{"created":1,"data":[{"url":"https://example.invalid/image.png"}]}`,
		`{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`,
	} {
		t.Run(responseBody, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			response := &http.Response{Body: io.NopCloser(strings.NewReader(responseBody))}

			usage, apiErr := OpenaiHandlerWithUsage(context, &relaycommon.RelayInfo{
				RelayMode:   relayconstant.RelayModeImagesEdits,
				ChannelMeta: &relaycommon.ChannelMeta{},
			}, response)
			if usage == nil || apiErr != nil {
				t.Fatalf("valid image response = usage %#v, error %#v", usage, apiErr)
			}
			if recorder.Body.String() != responseBody {
				t.Fatalf("response body = %q, want %q", recorder.Body.String(), responseBody)
			}
		})
	}
}

func image2OpenAIRequestContext(t *testing.T, path, requestID string, body io.Reader, contentType string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, body)
	c.Request.Header.Set("Content-Type", contentType)
	c.Request.Header.Set("X-Request-ID", requestID)
	c.Set(common.RequestIdKey, requestID)
	c.Header(common.RequestIdKey, requestID)
	return c, recorder
}

func image2OpenAIInfo(baseURL, path, requestID string, mode int) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		RequestURLPath:  path,
		OriginModelName: "gpt-image-2",
		RequestId:       requestID,
		RelayMode:       mode,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeOpenAI,
			ChannelBaseUrl: baseURL,
			ApiKey:         "fake-upstream-key",
			HeadersOverride: map[string]interface{}{
				"re:(?i)^X-Request-ID$": "",
			},
			ChannelSetting: dto.ChannelSettings{},
		},
	}
}

func TestImage2FakeUpstreamGenerationIsCalledOnceAndKeepsRequestID(t *testing.T) {
	service.InitHttpClient()
	requestID := "image2-generation-request-1"
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/v1/images/generations", r.URL.Path)
		require.Equal(t, requestID, r.Header.Get("X-Request-ID"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"ZmFrZS1nZW5lcmF0aW9u"}]}`)
	}))
	defer server.Close()

	body := strings.NewReader(`{"model":"gpt-image-2","prompt":"a fake image","size":"1024x1024"}`)
	c, recorder := image2OpenAIRequestContext(t, "/v1/images/generations", requestID, body, "application/json")
	info := image2OpenAIInfo(server.URL, "/v1/images/generations", requestID, relayconstant.RelayModeImagesGenerations)
	adaptor := &Adaptor{}

	converted, err := adaptor.ConvertImageRequest(c, info, dto.ImageRequest{Model: "gpt-image-2", Prompt: "a fake image", Size: "1024x1024"})
	require.NoError(t, err)
	convertedJSON, err := common.Marshal(converted)
	require.NoError(t, err)
	responseAny, err := adaptor.DoRequest(c, info, bytes.NewReader(convertedJSON))
	require.NoError(t, err)
	response := responseAny.(*http.Response)
	usage, apiErr := adaptor.DoResponse(c, response, info)
	if apiErr != nil {
		t.Fatalf("generation response error: %s", apiErr.Error())
	}
	require.NotNil(t, usage)
	require.Equal(t, 1, calls)
	require.Equal(t, requestID, c.GetString(common.RequestIdKey))
	require.Equal(t, requestID, c.Writer.Header().Get(common.RequestIdKey))
	require.Contains(t, recorder.Body.String(), "ZmFrZS1nZW5lcmF0aW9u")

	accepted := service.EvaluateSafeFailover(service.SafeFailoverInput{
		RelayMode:  relayconstant.RelayModeImagesGenerations,
		ModelName:  "gpt-image-2",
		Error:      types.NewErrorWithStatusCode(errors.New("upstream accepted job_id=fake-1"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway),
		ImageGuard: time.Minute,
	})
	require.False(t, accepted.Retry)
	require.Equal(t, "upstream_accepted", accepted.Reason)

	written := service.EvaluateSafeFailover(service.SafeFailoverInput{
		RelayMode:       relayconstant.RelayModeImagesGenerations,
		ModelName:       "gpt-image-2",
		ResponseWritten: true,
		Error:           types.NewErrorWithStatusCode(errors.New("late upstream failure"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway),
	})
	require.False(t, written.Retry)
	require.Equal(t, "response_started", written.Reason)
	require.Equal(t, 1, calls, "accepted or written responses must not trigger a replay")
}

func TestImage2FakeUpstreamEditsIsCalledOnceAndReturnsNonEmptyImage(t *testing.T) {
	service.InitHttpClient()
	requestID := "image2-edits-request-1"
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/v1/images/edits", r.URL.Path)
		require.Equal(t, requestID, r.Header.Get("X-Request-ID"))
		require.Contains(t, r.Header.Get("Content-Type"), "multipart/form-data")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"url":"https://fake.invalid/edited.png"}]}`)
	}))
	defer server.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "edit this fake image"))
	part, err := writer.CreateFormFile("image", "reference.jpg")
	require.NoError(t, err)
	_, err = part.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDRfake-payload"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	c, recorder := image2OpenAIRequestContext(t, "/v1/images/edits", requestID, bytes.NewReader(body.Bytes()), writer.FormDataContentType())
	info := image2OpenAIInfo(server.URL, "/v1/images/edits", requestID, relayconstant.RelayModeImagesEdits)
	adaptor := &Adaptor{}

	converted, err := adaptor.ConvertImageRequest(c, info, dto.ImageRequest{Model: "gpt-image-2"})
	require.NoError(t, err)
	responseAny, err := adaptor.DoRequest(c, info, converted.(io.Reader))
	require.NoError(t, err)
	response := responseAny.(*http.Response)
	usage, apiErr := adaptor.DoResponse(c, response, info)
	if apiErr != nil {
		t.Fatalf("edit response error: %s", apiErr.Error())
	}
	require.NotNil(t, usage)
	require.Equal(t, 1, calls)
	require.Equal(t, requestID, c.GetString(common.RequestIdKey))
	require.Equal(t, requestID, c.Writer.Header().Get(common.RequestIdKey))
	require.Contains(t, recorder.Body.String(), "edited.png")

	accepted := service.EvaluateSafeFailover(service.SafeFailoverInput{
		RelayMode:  relayconstant.RelayModeImagesEdits,
		ModelName:  "gpt-image-2",
		Error:      types.NewErrorWithStatusCode(errors.New("upstream accepted operation_id=fake-edit-1"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway),
		ImageGuard: time.Minute,
	})
	require.False(t, accepted.Retry)
	require.Equal(t, "upstream_accepted", accepted.Reason)

	written := service.EvaluateSafeFailover(service.SafeFailoverInput{
		RelayMode:       relayconstant.RelayModeImagesEdits,
		ModelName:       "gpt-image-2",
		ResponseWritten: true,
		Error:           types.NewErrorWithStatusCode(errors.New("late upstream failure"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway),
	})
	require.False(t, written.Retry)
	require.Equal(t, "response_started", written.Reason)
	require.Equal(t, 1, calls, "accepted or written edit responses must not trigger a replay")
}
