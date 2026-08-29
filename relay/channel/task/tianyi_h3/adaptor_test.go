package tianyi_h3

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func TestBuildRequestUsesTianyiDocumentedContract(t *testing.T) {
	ctx := jsonContext(`{"model":"minimax-h3","prompt":"a calm ocean at sunrise","resolution":"2K","duration":5,"ratio":"16:9"}`)
	info := testInfo("https://upstream.invalid")
	adaptor := &TaskAdaptor{}
	adaptor.Init(info)
	if err := adaptor.ValidateRequestAndSetAction(ctx, info); err != nil {
		t.Fatalf("validate: %v", err)
	}
	body, err := adaptor.BuildRequestBody(ctx, info)
	if err != nil {
		t.Fatalf("build body: %v", err)
	}
	var got requestPayload
	if err := common.Unmarshal(readAll(t, body), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.Model != ModelName || got.Resolution != "2K" || got.Duration != 5 || got.Ratio != DefaultRatio {
		t.Fatalf("unexpected payload: %+v", got)
	}
	if len(got.Content) != 1 || got.Content[0].Type != "text" || got.Content[0].Text != "a calm ocean at sunrise" {
		t.Fatalf("unexpected content: %+v", got.Content)
	}
	url, err := adaptor.BuildRequestURL(info)
	if err != nil || url != "https://upstream.invalid/v1/video_generation" {
		t.Fatalf("create URL = %q, %v", url, err)
	}

	adaptor.Init(testInfo("https://upstream.invalid/v1"))
	url, err = adaptor.BuildRequestURL(info)
	if err != nil || url != "https://upstream.invalid/v1/video_generation" {
		t.Fatalf("create URL with /v1 base = %q, %v", url, err)
	}
}

func TestBuildRequestURLRequiresConfiguredBaseURL(t *testing.T) {
	adaptor := &TaskAdaptor{}
	if _, err := adaptor.BuildRequestURL(testInfo("")); err == nil {
		t.Fatal("BuildRequestURL unexpectedly accepted an empty base URL")
	}
	if _, err := adaptor.FetchTask("", "task-1", map[string]any{"task_id": "task-1"}, ""); err == nil {
		t.Fatal("FetchTask unexpectedly accepted an empty base URL")
	}
}

func TestValidateRejectsUnverifiedMultimodalAndUnsupportedFields(t *testing.T) {
	for _, body := range []string{
		`{"model":"minimax-h3","prompt":"use image","image":"https://example.test/a.png"}`,
		`{"model":"minimax-h3","prompt":"use image","images":["https://example.test/a.png"]}`,
		`{"model":"minimax-h3","prompt":"use video","reference_video_url":"https://example.test/a.mp4"}`,
		`{"model":"minimax-h3","prompt":"wrong ratio","ratio":"9:16"}`,
		`{"model":"minimax-h3","prompt":"wrong resolution","resolution":"1080P"}`,
		`{"model":"minimax-h3","prompt":"wrong field","quality":"high"}`,
		`{"model":"minimax-h3","prompt":"fractional","duration":5.5}`,
		`{"model":"minimax-h3","prompt":"too short","duration":4}`,
		`{"model":"minimax-h3","prompt":"duplicate aliases","duration":5,"seconds":5}`,
		`{"model":"minimax-h3","prompt":"duplicate aliases","resolution":"2K","size":"2K"}`,
		`{"model":"minimax-h3","prompt":"wrong casing","Resolution":"2K"}`,
		`{"model":"minimax-h3","prompt":"wrong whitespace key"," resolution":"2K"}`,
	} {
		ctx := jsonContext(body)
		adaptor := &TaskAdaptor{}
		if err := adaptor.ValidateRequestAndSetAction(ctx, testInfo("https://upstream.invalid")); err == nil {
			t.Fatalf("validation unexpectedly accepted %s", body)
		}
	}

	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	_ = writer.WriteField("model", ModelName)
	_ = writer.WriteField("prompt", "multipart is not verified")
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/videos", &form)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = req
	if err := (&TaskAdaptor{}).ValidateRequestAndSetAction(ctx, testInfo("https://upstream.invalid")); err == nil {
		t.Fatal("multipart validation unexpectedly succeeded")
	}
}

func TestFakeUpstreamCreateQueryLifecycleUsesDistinctTianyiPaths(t *testing.T) {
	service.InitHttpClient()
	var mu sync.Mutex
	var createCount, queryCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer dev-key" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/video_generation":
			createCount++
			var payload requestPayload
			body, _ := io.ReadAll(r.Body)
			if err := common.Unmarshal(body, &payload); err != nil {
				t.Errorf("create body: %v", err)
			}
			if payload.Model != ModelName || len(payload.Content) != 1 || payload.Duration != MinDuration {
				t.Errorf("unexpected create payload: %+v", payload)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"task_id":"ty-task-1","status":"queued"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/query/video_generation/ty-task-1":
			queryCount++
			w.Header().Set("Content-Type", "application/json")
			switch queryCount {
			case 1:
				_, _ = io.WriteString(w, `{"task_id":"ty-task-1","status":"processing"}`)
			default:
				_, _ = io.WriteString(w, `{"task_id":"ty-task-1","status":"succeeded","video_url":"https://cdn.example.test/ty-task-1.mp4"}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	info := testInfo(server.URL)
	adaptor := &TaskAdaptor{}
	adaptor.Init(info)
	ctx := jsonContext(`{"model":"minimax-h3","prompt":"documented text request"}`)
	if err := adaptor.ValidateRequestAndSetAction(ctx, info); err != nil {
		t.Fatalf("validate: %v", err)
	}
	body, err := adaptor.BuildRequestBody(ctx, info)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	createURL, _ := adaptor.BuildRequestURL(info)
	createReq, _ := http.NewRequest(http.MethodPost, createURL, body)
	_ = adaptor.BuildRequestHeader(ctx, createReq, info)
	createResp, err := service.GetHttpClient().Do(createReq)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	createBody, _ := io.ReadAll(createResp.Body)
	_ = createResp.Body.Close()
	taskID, _, taskErr := adaptor.DoResponse(ctx, &http.Response{StatusCode: createResp.StatusCode, Body: io.NopCloser(bytes.NewReader(createBody))}, info)
	if taskErr != nil || taskID != "ty-task-1" {
		t.Fatalf("DoResponse() = task=%q err=%v body=%s", taskID, taskErr, createBody)
	}

	for i, want := range []model.TaskStatus{model.TaskStatusInProgress, model.TaskStatusSuccess} {
		resp, err := adaptor.FetchTask(server.URL, "dev-key", map[string]any{"task_id": taskID}, "")
		if err != nil {
			t.Fatalf("FetchTask(%d): %v", i, err)
		}
		pollBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		result, err := adaptor.ParseTaskResult(pollBody)
		if err != nil || result.Status != string(want) {
			t.Fatalf("ParseTaskResult(%d) = %+v, %v", i, result, err)
		}
		if want == model.TaskStatusSuccess && result.Url != "https://cdn.example.test/ty-task-1.mp4" {
			t.Fatalf("success URL = %q", result.Url)
		}
	}
	if createCount != 1 || queryCount != 2 {
		t.Fatalf("upstream counts = create:%d query:%d", createCount, queryCount)
	}
}

func TestDoResponseRejectsMissingTaskIDAndUnknownStatus(t *testing.T) {
	adaptor := &TaskAdaptor{}
	info := testInfo("https://upstream.invalid")
	for _, body := range []string{
		`{"status":"queued"}`,
		`{"task_id":"ty-task-1","status":"mystery"}`,
		`{"task_id":"ty-task-1","status":"failed","message":"rejected"}`,
	} {
		ctx := jsonContext(`{"model":"minimax-h3","prompt":"test"}`)
		_, _, taskErr := adaptor.DoResponse(ctx, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, info)
		if taskErr == nil {
			t.Fatalf("DoResponse unexpectedly accepted %s", body)
		}
	}
}

func TestConvertToOpenAIVideoUsesLocalProxy(t *testing.T) {
	task := &model.Task{
		TaskID:     "task_public",
		Status:     model.TaskStatusSuccess,
		Properties: model.Properties{OriginModelName: ModelName},
	}
	body, err := (&TaskAdaptor{}).ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !strings.Contains(string(body), `"task_public"`) || !strings.Contains(string(body), `/v1/videos/task_public/content`) {
		t.Fatalf("public response leaks or omits proxy URL: %s", body)
	}
}

func jsonContext(body string) *gin.Context {
	req := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = req
	return ctx
}

func testInfo(baseURL string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeTianyiH3,
			ChannelBaseUrl:    baseURL,
			ApiKey:            "dev-key",
			UpstreamModelName: ModelName,
		},
		OriginModelName: ModelName,
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"},
	}
}

func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}
