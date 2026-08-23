package codefoxasync

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
)

func TestClientSubmitPollAndFetchImageSuccess(t *testing.T) {
	const apiKey = "cf-test-key"
	var mu sync.Mutex
	pollCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/ecommerce/batch-generate":
			if got := r.Header.Get("Idempotency-Key"); got != "order-42" {
				t.Fatalf("idempotency header = %q", got)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read submit body: %v", err)
			}
			var request BatchRequest
			if err := common.Unmarshal(body, &request); err != nil {
				t.Fatalf("decode submit body: %v", err)
			}
			if request.Model != DefaultModel || request.Size != "1536x1024" || request.Quality != "hd" {
				t.Fatalf("normalized request = %#v", request)
			}
			if request.Seed == nil || *request.Seed != 0 {
				t.Fatalf("explicit zero seed was dropped: %#v", request.Seed)
			}
			if len(request.Prompts) != 2 || request.ReferenceImageURL == "" || request.CallbackURL == "" {
				t.Fatalf("optional request fields missing: %#v", request)
			}
			_, _ = io.WriteString(w, `{"success":true,"data":{"task_id":"up-task-42","status":"PENDING","total_count":2,"created_at":1717920000}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/ecommerce/tasks/up-task-42":
			mu.Lock()
			pollCalls++
			call := pollCalls
			mu.Unlock()
			if call == 1 {
				_, _ = io.WriteString(w, `{"success":true,"data":{"task_id":"up-task-42","product_id":"SKU-42","status":"PROCESSING","progress":{"total":2,"success":0,"failed":0,"pending":2},"created_at":1717920000}}`)
				return
			}
			_, _ = io.WriteString(w, `{"success":true,"data":{"task_id":"up-task-42","product_id":"SKU-42","status":"COMPLETED","progress":{"total":2,"success":2,"failed":0,"pending":0},"results":[{"item_index":0,"prompt":"front","image_url":"https://provider.invalid/img/0","status":"SUCCESS"},{"item_index":1,"prompt":"side","image_url":"https://provider.invalid/img/1","status":"SUCCESS"}],"created_at":1717920000,"completed_at":1717920180}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/ecommerce/images/up-task-42/1":
			w.Header().Set("Content-Type", "image/png")
			_, _ = io.WriteString(w, "PNG-ONE")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, apiKey)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	seed := uint32(0)
	request := BatchRequest{
		IdempotencyKey:    "order-42",
		ProductID:         "SKU-42",
		Prompts:           []string{" front ", "side"},
		Size:              "1536x1024",
		Quality:           "hd",
		Seed:              &seed,
		ReferenceImageURL: "https://example.test/reference.png",
		CallbackURL:       "https://example.test/callback",
	}
	submission, err := client.SubmitBatch(context.Background(), request)
	if err != nil {
		t.Fatalf("SubmitBatch() error = %v", err)
	}
	if submission.TaskID != "up-task-42" || submission.Status != StatusPending || submission.TotalCount != 2 {
		t.Fatalf("submission = %#v", submission)
	}
	task, err := client.Poll(context.Background(), submission.TaskID, PollOptions{MaxAttempts: 3, Interval: 0})
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if task.Status != StatusCompleted || task.SucceededCount() != 2 || task.FailedCount() != 0 {
		t.Fatalf("task = %#v", task)
	}
	if len(task.ImageProxyReferences()) != 2 || task.ImageProxyReferences()[1].ItemIndex != 1 {
		t.Fatalf("image proxy references = %#v", task.ImageProxyReferences())
	}
	imageResp, err := client.FetchImage(context.Background(), submission.TaskID, 1)
	if err != nil {
		t.Fatalf("FetchImage() error = %v", err)
	}
	defer imageResp.Body.Close()
	imageBody, err := io.ReadAll(imageResp.Body)
	if err != nil || string(imageBody) != "PNG-ONE" {
		t.Fatalf("image body = %q, err = %v", imageBody, err)
	}
}

func TestClientPartialSuccessPreservesItemsAndSanitizesErrors(t *testing.T) {
	const apiKey = "partial-secret-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ecommerce/tasks/task-partial" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"data":{"task_id":"task-partial","status":"PARTIAL_SUCCESS","progress":{"total":3,"success":2,"failed":1,"pending":0},"results":[{"item_index":0,"prompt":"front","image_url":"https://provider.invalid/0","status":"SUCCESS"},{"item_index":1,"prompt":"side","image_url":"https://provider.invalid/1","status":"SUCCESS"}],"errors":[{"item_index":2,"prompt":"bad","error_code":"CONTENT_POLICY_VIOLATION","error_message":"Authorization: Bearer partial-secret-key was rejected"}]}}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, apiKey)
	if err != nil {
		t.Fatal(err)
	}
	task, err := client.GetTask(context.Background(), "task-partial")
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if task.Status != StatusPartialSuccess || task.SucceededCount() != 2 || task.FailedCount() != 1 {
		t.Fatalf("partial task = %#v", task)
	}
	if len(task.Results) != 2 || len(task.Errors) != 1 {
		t.Fatalf("partial details = %#v", task)
	}
	if strings.Contains(task.Errors[0].ErrorMessage, apiKey) || strings.Contains(task.Errors[0].ErrorMessage, "Bearer partial-secret-key") {
		t.Fatalf("secret leaked in sanitized error: %q", task.Errors[0].ErrorMessage)
	}
	if task.Errors[0].ErrorCode != "CONTENT_POLICY_VIOLATION" {
		t.Fatalf("error code = %q", task.Errors[0].ErrorCode)
	}
}

func TestClientFailureAndHTTPErrorDoNotExposeAPIKey(t *testing.T) {
	const apiKey = "failure-secret-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ecommerce/tasks/task-failed" {
			_, _ = io.WriteString(w, `{"success":true,"data":{"task_id":"task-failed","status":"FAILED","progress":{"total":2,"success":0,"failed":2,"pending":0},"errors":[{"item_index":0,"error_code":"UPSTREAM_SERVICE_ERROR","error_message":"failure-secret-key upstream failure"},{"item_index":1,"error_code":"API_ERROR","message":"temporary failure"}]}}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"Authorization: Bearer failure-secret-key is invalid","error":{"code":"authentication_error"}}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, apiKey)
	if err != nil {
		t.Fatal(err)
	}
	task, err := client.GetTask(context.Background(), "task-failed")
	if err != nil {
		t.Fatalf("failed task unexpectedly returned error: %v", err)
	}
	if task.Status != StatusFailed || task.FailedCount() != 2 {
		t.Fatalf("failed task = %#v", task)
	}
	if strings.Contains(task.Errors[0].ErrorMessage, apiKey) {
		t.Fatalf("secret leaked in task error: %q", task.Errors[0].ErrorMessage)
	}
	_, err = client.GetTask(context.Background(), "task-http-error")
	if err == nil || strings.Contains(err.Error(), apiKey) || strings.Contains(err.Error(), "Bearer ") {
		t.Fatalf("HTTP error leaked secret or was absent: %v", err)
	}
}

func TestClientDuplicateSubmissionCarriesSameIdempotencyKey(t *testing.T) {
	const apiKey = "duplicate-key"
	var mu sync.Mutex
	var calls int
	var headers []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/ecommerce/batch-generate" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		calls++
		headers = append(headers, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		_, _ = io.WriteString(w, `{"success":true,"data":{"task_id":"same-task","status":"PROCESSING","total_count":1,"created_at":1717920000}}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, apiKey)
	if err != nil {
		t.Fatal(err)
	}
	request := BatchRequest{IdempotencyKey: "same-order", Prompts: []string{"one"}}
	first, err := client.SubmitBatch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.SubmitBatch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 || first.TaskID != second.TaskID || len(headers) != 2 || headers[0] != "same-order" || headers[1] != "same-order" {
		t.Fatalf("duplicate submission evidence: calls=%d headers=%#v first=%#v second=%#v", calls, headers, first, second)
	}
}

func TestClientValidationFailsBeforeUpstream(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "validation-key")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SubmitBatch(context.Background(), BatchRequest{Prompts: []string{""}})
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("validation error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("invalid request reached upstream %d times", requests)
	}
}

func TestClientEscapesTaskIDWithoutDoubleEncoding(t *testing.T) {
	client, err := NewClient("https://provider.invalid", "escape-key")
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := client.ImageURL("task/a?b", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(endpoint, "%252F") || strings.Contains(endpoint, "%252B") || !strings.Contains(endpoint, "%2F") || !strings.Contains(endpoint, "%3Fb") {
		t.Fatalf("escaped endpoint = %q", endpoint)
	}
}

func TestTaskAdaptorUsesPublicIDAndMapsPartialBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	requestBody := `{"idempotency_key":"job-1","product_id":"sku-1","prompts":["one","two","three"],"seed":0}`
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations/jobs", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = req
	if _, err := common.GetBodyStorage(ctx); err != nil {
		t.Fatalf("GetBodyStorage() error = %v", err)
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta:   &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.invalid", ApiKey: "test-key"},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_public_1"},
	}
	adaptor := &TaskAdaptor{}
	adaptor.Init(info)
	if taskErr := adaptor.ValidateRequestAndSetAction(ctx, info); taskErr != nil {
		t.Fatalf("ValidateRequestAndSetAction() error = %v", taskErr)
	}
	request, err := requestFromContextOrBody(ctx)
	if err != nil || len(request.Prompts) != 3 || request.Seed == nil || *request.Seed != 0 {
		t.Fatalf("stored request = %#v err=%v", request, err)
	}
	if ratios := adaptor.EstimateBilling(ctx, info); ratios["n"] != 3 {
		t.Fatalf("billing ratios = %#v", ratios)
	}

	recorder := httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	response := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"success":true,"data":{"task_id":"provider-task-1","status":"PENDING","total_count":3,"created_at":1717920000}}`)),
	}
	upstreamID, _, taskErr := adaptor.DoResponse(ctx, response, info)
	if taskErr != nil || upstreamID != "provider-task-1" {
		t.Fatalf("DoResponse() id=%q err=%v", upstreamID, taskErr)
	}
	if strings.Contains(recorder.Body.String(), "provider-task-1") || !strings.Contains(recorder.Body.String(), "task_public_1") {
		t.Fatalf("public response leaked upstream id: %s", recorder.Body.String())
	}
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, want %d", recorder.Code, http.StatusAccepted)
	}

	partial := []byte(`{"success":true,"data":{"task_id":"provider-task-1","status":"PARTIAL_SUCCESS","progress":{"total":3,"success":2,"failed":1,"pending":0},"results":[{"item_index":0,"image_url":"https://provider.invalid/0"},{"item_index":1,"image_url":"https://provider.invalid/1"}],"errors":[{"item_index":2,"error_code":"CONTENT_POLICY_VIOLATION","error_message":"blocked"}]}}`)
	result, err := adaptor.ParseTaskResult(partial)
	if err != nil {
		t.Fatalf("ParseTaskResult() error = %v", err)
	}
	if result.Status != string(model.TaskStatusSuccess) || result.TotalTokens != 2 || result.CompletionTokens != 3 {
		t.Fatalf("partial TaskInfo = %#v", result)
	}
	quota := adaptor.AdjustBillingOnComplete(&model.Task{Quota: 300}, result)
	if quota != 200 {
		t.Fatalf("partial actual quota = %d, want 200", quota)
	}
}

func TestConvertToOpenAIImageTaskUsesPublicProxyAndHidesProviderDetails(t *testing.T) {
	oldServerAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://aibuff.example"
	t.Cleanup(func() { system_setting.ServerAddress = oldServerAddress })

	providerBody := []byte(`{"success":true,"data":{"task_id":"provider-secret-task","product_id":"sku-1","status":"PARTIAL_SUCCESS","progress":{"total":2,"success":1,"failed":1,"pending":0},"results":[{"item_index":0,"prompt":"front","image_url":"https://provider.invalid/secret.png","status":"SUCCESS"}],"errors":[{"item_index":1,"prompt":"bad","error_code":"CONTENT_POLICY_VIOLATION","error_message":"raw provider diagnostic"}],"created_at":1717920000,"completed_at":1717920180}}`)
	task := &model.Task{
		TaskID:     "task_public_1",
		Status:     model.TaskStatusSuccess,
		CreatedAt:  1717920000,
		FinishTime: 1717920180,
		Data:       providerBody,
	}
	body, err := (&TaskAdaptor{}).ConvertToOpenAIImageTask(task)
	if err != nil {
		t.Fatalf("ConvertToOpenAIImageTask() error = %v", err)
	}
	public := string(body)
	for _, forbidden := range []string{"provider-secret-task", "provider.invalid", "raw provider diagnostic"} {
		if strings.Contains(public, forbidden) {
			t.Fatalf("public response leaked %q: %s", forbidden, public)
		}
	}
	wantProxy := "https://aibuff.example/v1/images/batches/task_public_1/items/0/content"
	if !strings.Contains(public, wantProxy) || !strings.Contains(public, `"status":"PARTIAL_SUCCESS"`) {
		t.Fatalf("public response = %s", public)
	}
}

func TestTaskAdaptorFetchTaskIsReadOnlyAndUsesKeySnapshot(t *testing.T) {
	const key = "snapshot-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/ecommerce/tasks/provider-task" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{"success":true,"data":{"task_id":"provider-task","status":"PROCESSING","progress":{"total":1,"success":0,"failed":0,"pending":1},"errors":[{"error_message":"snapshot-key"}]}}`)
	}))
	defer server.Close()
	resp, err := (&TaskAdaptor{}).FetchTask(server.URL, key, map[string]any{"task_id": "provider-task"}, "")
	if err != nil {
		t.Fatalf("FetchTask() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("FetchTask() status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read sanitized task response: %v", err)
	}
	if strings.Contains(string(body), key) {
		t.Fatalf("FetchTask response leaked key: %s", body)
	}
}

func TestTaskAdaptorNamespacesIdempotencyAndRejectsDirectCallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	makeContext := func(body string) *gin.Context {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/batches", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		return ctx
	}

	first := makeContext(`{"idempotency_key":"order-1","prompts":["one"]}`)
	firstInfo := &relaycommon.RelayInfo{UserId: 11}
	adaptor := &TaskAdaptor{}
	if taskErr := adaptor.ValidateRequestAndSetAction(first, firstInfo); taskErr != nil {
		t.Fatalf("first validation error = %v", taskErr)
	}
	firstRequest, err := requestFromContextOrBody(first)
	if err != nil {
		t.Fatal(err)
	}
	second := makeContext(`{"idempotency_key":"order-1","prompts":["one"]}`)
	if taskErr := adaptor.ValidateRequestAndSetAction(second, &relaycommon.RelayInfo{UserId: 12}); taskErr != nil {
		t.Fatalf("second validation error = %v", taskErr)
	}
	secondRequest, err := requestFromContextOrBody(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstRequest.IdempotencyKey == "order-1" || firstRequest.IdempotencyKey == secondRequest.IdempotencyKey {
		t.Fatalf("idempotency keys were not user-scoped: %q %q", firstRequest.IdempotencyKey, secondRequest.IdempotencyKey)
	}

	callback := makeContext(`{"prompts":["one"],"callback_url":"https://customer.example/webhook"}`)
	if taskErr := adaptor.ValidateRequestAndSetAction(callback, &relaycommon.RelayInfo{UserId: 11}); taskErr == nil {
		t.Fatal("direct callback_url unexpectedly accepted")
	}
}
