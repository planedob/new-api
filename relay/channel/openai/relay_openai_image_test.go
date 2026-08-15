package openai

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
)

func TestOpenAIImage2EditNormalizesQualityBeforeWire(t *testing.T) {
	var input bytes.Buffer
	writer := multipart.NewWriter(&input)
	if err := writer.WriteField("model", "gpt-image-2"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("prompt", "edit"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("quality", "high"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("image", "input.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("png")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(input.Bytes()))
	context.Request.Header.Set("Content-Type", writer.FormDataContentType())
	if _, err := context.MultipartForm(); err != nil {
		t.Fatal(err)
	}

	converted, err := (&Adaptor{}).ConvertImageRequest(context, &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-2",
		RelayMode:       relayconstant.RelayModeImagesEdits,
	}, dto.ImageRequest{Model: "gpt-image-2", Quality: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	requestBody, ok := converted.(*bytes.Buffer)
	if !ok {
		t.Fatalf("converted image request type = %T, want *bytes.Buffer", converted)
	}
	contentType := context.Request.Header.Get("Content-Type")
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(requestBody, params["boundary"])
	fields := map[string]string{}
	for {
		formPart, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if formPart.FormName() == "quality" {
			value, readErr := io.ReadAll(formPart)
			if readErr != nil {
				t.Fatal(readErr)
			}
			fields["quality"] = string(value)
		}
	}
	if fields["quality"] != "auto" {
		t.Fatalf("wire quality = %q, want auto", fields["quality"])
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
