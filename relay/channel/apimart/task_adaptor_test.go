package apimart

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

func TestTaskAdaptorDoResponseReturnsOnlyPublicJobID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	info := &relaycommon.RelayInfo{
		StartTime: time.Unix(123, 0),
		TaskRelayInfo: &relaycommon.TaskRelayInfo{
			PublicTaskID: "task_public_123",
		},
		OriginModelName: ModelName,
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"code":200,"data":[{"status":"submitted","task_id":"provider-secret-123"}]}`)),
	}

	taskID, taskData, taskErr := (&TaskAdaptor{}).DoResponse(c, resp, info)
	if taskErr != nil {
		t.Fatalf("DoResponse returned error: %v", taskErr)
	}
	if taskID != "provider-secret-123" {
		t.Fatalf("task id = %q, want internal provider id", taskID)
	}
	if strings.Contains(recorder.Body.String(), "provider-secret-123") {
		t.Fatalf("provider task id leaked in public response: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "task_public_123") {
		t.Fatalf("public task id missing: %s", recorder.Body.String())
	}
	if strings.Contains(string(taskData), "provider-secret-123") {
		t.Fatalf("provider task id leaked in persisted task data: %s", taskData)
	}
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, want %d", recorder.Code, http.StatusAccepted)
	}
}

func TestConvertToOpenAIImageTaskReturnsCompletedImagesWithoutProviderID(t *testing.T) {
	task := &model.Task{
		TaskID:     "task_public_123",
		CreatedAt:  123,
		FinishTime: 456,
		Status:     model.TaskStatusSuccess,
		Properties: model.Properties{OriginModelName: ModelName},
		Data:       []byte(`{"data":{"status":"completed","task_id":"provider-secret-123","result":{"images":[{"url":"https://example.test/a.png"},{"b64_json":"ZmFrZQ=="}]}}}`),
	}
	body, err := (&TaskAdaptor{}).ConvertToOpenAIImageTask(task)
	if err != nil {
		t.Fatalf("ConvertToOpenAIImageTask() error = %v", err)
	}
	public := string(body)
	if strings.Contains(public, "provider-secret-123") || !strings.Contains(public, "task_public_123") || !strings.Contains(public, `"status":"succeeded"`) {
		t.Fatalf("public task response = %s", public)
	}
	if !strings.Contains(public, "https://example.test/a.png") || !strings.Contains(public, "ZmFrZQ==") {
		t.Fatalf("completed images missing: %s", public)
	}
}

func TestTaskAdaptorBuildRequestBodyOverridesClientModel(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations/jobs", strings.NewReader(`{"model":"client-model","prompt":"a cat","size":"1024x1024"}`))
	request.Header.Set("Content-Type", "application/json")
	c.Request = request
	info := &relaycommon.RelayInfo{
		OriginModelName: ModelName,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: ModelName},
	}
	a := &TaskAdaptor{}
	if taskErr := a.ValidateRequestAndSetAction(c, info); taskErr != nil {
		t.Fatalf("validation failed: %v", taskErr)
	}
	body, err := a.BuildRequestBody(c, info)
	if err != nil {
		t.Fatalf("BuildRequestBody failed: %v", err)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"model":"gpt-image-2"`) {
		t.Fatalf("upstream model was not bound: %s", data)
	}
	if strings.Contains(string(data), "client-model") {
		t.Fatalf("client model was not replaced: %s", data)
	}
	common.CleanupBodyStorage(c)
}

func TestParseTaskResultNormalizesImagesAndFailures(t *testing.T) {
	result, err := ParseTaskResult([]byte(`{"data":{"status":"completed","result":{"images":[{"url":["https://example.test/a.png"]},{"b64_json":"ZmFrZQ=="}]}}}`))
	if err != nil {
		t.Fatalf("ParseTaskResult failed: %v", err)
	}
	if result.Status != StatusCompleted || len(result.Images) != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	response, err := NormalizeImageResponse(result, 123)
	if err != nil || len(response.Data) != 2 {
		t.Fatalf("NormalizeImageResponse failed: %v %#v", err, response)
	}

	failed, err := ParseTaskResult([]byte(`{"data":{"status":"failed","error":{"message":"provider-secret-task"}}}`))
	if err != nil {
		t.Fatalf("failed task should normalize, got error: %v", err)
	}
	if failed.Status != StatusFailed || failed.Error == nil {
		t.Fatalf("unexpected failed task: %#v", failed)
	}
	if strings.Contains(failed.Error.Error(), "provider-secret-task") {
		t.Fatal("provider message leaked from normalized error")
	}
}

func TestTaskAdaptorParseTaskResultMapsProgress(t *testing.T) {
	adaptor := &TaskAdaptor{}
	result, err := adaptor.ParseTaskResult([]byte(`{"data":{"status":"processing","progress":"40%"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "IN_PROGRESS" || result.Progress != "40%" {
		t.Fatalf("unexpected task info: %#v", result)
	}

	var _ interface {
		Init(*relaycommon.RelayInfo)
		ValidateRequestAndSetAction(*gin.Context, *relaycommon.RelayInfo) *dto.TaskError
	} = adaptor
}
