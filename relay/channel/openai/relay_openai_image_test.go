package openai

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
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
