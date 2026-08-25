package middleware

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	projecti18n "github.com/QuantumNous/new-api/i18n"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGetPlaygroundGroupReadsJSONImageRequest(t *testing.T) {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"/pg/images/generations",
		bytes.NewBufferString(`{"model":"gpt-image-2","group":" image2-test "}`),
	)
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request

	group, err := getPlaygroundGroup(context)
	if err != nil {
		t.Fatalf("getPlaygroundGroup() error = %v", err)
	}
	if group != "image2-test" {
		t.Fatalf("getPlaygroundGroup() = %q, want %q", group, "image2-test")
	}
}

func TestGetPlaygroundGroupReadsMultipartImageEdit(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "gpt-image-2"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("group", "image2-test"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("image", "reference.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("not-a-real-image")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/pg/images/edits", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request

	group, err := getPlaygroundGroup(context)
	if err != nil {
		t.Fatalf("getPlaygroundGroup() error = %v", err)
	}
	if group != "image2-test" {
		t.Fatalf("getPlaygroundGroup() = %q, want %q", group, "image2-test")
	}
}

func TestGetModelFromRequestReadsMultipartImageEdit(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "gpt-image-2"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("group", "image2-test"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/pg/images/edits", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request

	modelRequest, err := getModelFromRequest(context)
	if err != nil {
		t.Fatalf("getModelFromRequest() error = %v", err)
	}
	if modelRequest.Model != "gpt-image-2" || modelRequest.Group != "image2-test" {
		t.Fatalf("getModelFromRequest() = %#v, want model and group from multipart body", modelRequest)
	}
}

func TestPlaygroundImage2ChatRequestIsRejected(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		path  string
		model string
		want  bool
	}{
		{name: "image2 chat", path: "/pg/chat/completions", model: "gpt-image-2", want: true},
		{name: "image2 variant chat", path: "/pg/chat/completions", model: " GPT-IMAGE-2-4k ", want: true},
		{name: "image endpoint", path: "/pg/images/generations", model: "gpt-image-2", want: false},
		{name: "text chat", path: "/pg/chat/completions", model: "gpt-5.4", want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isPlaygroundImage2ChatRequest(testCase.path, testCase.model); got != testCase.want {
				t.Fatalf("isPlaygroundImage2ChatRequest(%q, %q) = %v, want %v", testCase.path, testCase.model, got, testCase.want)
			}
		})
	}
}

func TestReadOnlyTaskFetchRequestRequiresGETAndKnownFetchPath(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{name: "video task", method: http.MethodGet, path: "/v1/videos/task_public", want: true},
		{name: "video content", method: http.MethodGet, path: "/v1/videos/task_public/content", want: true},
		{name: "legacy video task", method: http.MethodGet, path: "/v1/video/generations/task_public", want: true},
		{name: "image job", method: http.MethodGet, path: "/v1/images/generations/jobs/task_public", want: true},
		{name: "image batch", method: http.MethodGet, path: "/v1/images/batches/task_public", want: true},
		{name: "playground image job", method: http.MethodGet, path: "/pg/images/jobs/task_public", want: true},
		{name: "post task", method: http.MethodPost, path: "/v1/videos/task_public", want: false},
		{name: "remix", method: http.MethodGet, path: "/v1/videos/task_public/remix", want: false},
		{name: "remix suffix", method: http.MethodGet, path: "/v1/videos/task_public/remix/other", want: false},
		{name: "unknown video action", method: http.MethodGet, path: "/v1/videos/task_public/extra", want: false},
		{name: "unknown image job action", method: http.MethodGet, path: "/v1/images/generations/jobs/task_public/extra", want: false},
		{name: "unknown image batch action", method: http.MethodGet, path: "/v1/images/batches/task_public/extra", want: false},
		{name: "unknown playground job action", method: http.MethodGet, path: "/pg/images/jobs/task_public/extra", want: false},
		{name: "empty task id", method: http.MethodGet, path: "/v1/videos/", want: false},
		{name: "collection", method: http.MethodGet, path: "/v1/videos", want: false},
		{name: "unrelated", method: http.MethodGet, path: "/v1/chat/completions", want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isReadOnlyTaskFetchRequest(testCase.method, testCase.path); got != testCase.want {
				t.Fatalf("isReadOnlyTaskFetchRequest(%q, %q) = %v, want %v", testCase.method, testCase.path, got, testCase.want)
			}
		})
	}
}

func TestDistributeAllowsEmptyModelOnlyForReadOnlyTaskGET(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.NoError(t, projecti18n.Init())
	for _, testCase := range []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{name: "task get", method: http.MethodGet, path: "/v1/videos/task_public", wantStatus: http.StatusNoContent},
		{name: "content get", method: http.MethodGet, path: "/v1/videos/task_public/content", wantStatus: http.StatusNoContent},
		{name: "empty post remains model limited", method: http.MethodPost, path: "/v1/videos/task_public", body: `{}`, wantStatus: http.StatusForbidden},
		{name: "remix remains model limited", method: http.MethodPost, path: "/v1/videos/task_public/remix", body: `{}`, wantStatus: http.StatusForbidden},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) {
				common.SetContextKey(c, constant.ContextKeyTokenModelLimitEnabled, true)
				common.SetContextKey(c, constant.ContextKeyTokenModelLimit, map[string]bool{"minimax-hailuo-2.3": true})
				c.Next()
			})
			router.Handle(testCase.method, testCase.path, Distribute(), func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			})

			request := httptest.NewRequest(testCase.method, testCase.path, bytes.NewBufferString(testCase.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
		})
	}
}
