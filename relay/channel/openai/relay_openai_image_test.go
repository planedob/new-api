package openai

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func TestGetRequestURLPreservesImageGenerationAndEditsPaths(t *testing.T) {
	adaptor := &Adaptor{}
	for _, test := range []struct {
		name string
		path string
		mode int
	}{
		{name: "generation", path: "/v1/images/generations", mode: relayconstant.RelayModeImagesGenerations},
		{name: "edits", path: "/v1/images/edits", mode: relayconstant.RelayModeImagesEdits},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := adaptor.GetRequestURL(&relaycommon.RelayInfo{
				RelayFormat:    types.RelayFormatOpenAIImage,
				RelayMode:      test.mode,
				RequestURLPath: test.path,
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelBaseUrl: "https://token.secure-skill.com",
					ChannelType:    constant.ChannelTypeOpenAI,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			want := "https://token.secure-skill.com" + test.path
			if got != want {
				t.Fatalf("GetRequestURL() = %q, want %q", got, want)
			}
		})
	}
}

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

type failingAcceptedImageBody struct{}

func (failingAcceptedImageBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failingAcceptedImageBody) Close() error             { return nil }

func TestOpenaiImageAcceptedStatePrecedesBodyHandling(t *testing.T) {
	for _, test := range []struct {
		name string
		body io.ReadCloser
	}{
		{name: "invalid JSON", body: io.NopCloser(strings.NewReader(`{invalid-json`))},
		{name: "read failure", body: failingAcceptedImageBody{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			info := &relaycommon.RelayInfo{
				RelayMode:       relayconstant.RelayModeImagesGenerations,
				OriginModelName: "gpt-image-2",
				ChannelMeta:     &relaycommon.ChannelMeta{},
			}
			response := &http.Response{StatusCode: http.StatusOK, Body: test.body}

			usage, apiErr := OpenaiHandlerWithUsage(context, info, response)

			if usage != nil || apiErr == nil {
				t.Fatalf("accepted malformed response = usage %#v, error %#v; want local handling error", usage, apiErr)
			}
			if !info.UpstreamAccepted {
				t.Fatal("2xx Image2 response was not marked accepted before local body handling")
			}
			if recorder.Body.Len() != 0 {
				t.Fatalf("malformed response was written to client: %q", recorder.Body.String())
			}
		})
	}
}
