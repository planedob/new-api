package service

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
)

func TestRedactProtectedTaskResponseBody(t *testing.T) {
	body := []byte(`{"success":true,"data":{"task_id":"provider-task","id":"provider-id","status":"PARTIAL_SUCCESS","results":[{"item_index":0,"image_url":"https://example.test/image.png"}],"errors":[{"error_code":"POLICY","error_message":"raw diagnostic"}]}}`)
	redacted := string(redactProtectedTaskResponseBody(body, constant.ChannelTypeCodeFoxAsync))
	for _, forbidden := range []string{"provider-task", "provider-id", "raw diagnostic"} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("redacted response leaked %q: %s", forbidden, redacted)
		}
	}
	for _, required := range []string{"PARTIAL_SUCCESS", "https://example.test/image.png", "POLICY"} {
		if !strings.Contains(redacted, required) {
			t.Fatalf("redacted response lost %q: %s", required, redacted)
		}
	}
}

func TestRedactTaskResponseForLogHidesSignedResults(t *testing.T) {
	body := []byte(`{"status":"completed","images":[{"url":"https://example.test/private?signature=secret","b64_json":"large-secret-payload"}]}`)
	redacted := string(redactTaskResponseForLog(body, constant.ChannelTypeAPIMart))
	for _, forbidden := range []string{"signature=secret", "large-secret-payload"} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("log response leaked %q: %s", forbidden, redacted)
		}
	}
}
