package secureskill

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func TestBuildRequestBodyConvertsMultipartImagesAndUsesFixedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body, contentType := multipartBody(t, 1)
	req := httptest.NewRequest(http.MethodPost, "/v1/videos", body)
	req.Header.Set("Content-Type", contentType)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = req
	if _, err := common.GetBodyStorage(ctx); err != nil {
		t.Fatalf("cache multipart body: %v", err)
	}
	info := testInfo("https://upstream.invalid")
	adaptor := &TaskAdaptor{}
	adaptor.Init(info)

	if taskErr := adaptor.ValidateRequestAndSetAction(ctx, info); taskErr != nil {
		t.Fatalf("ValidateRequestAndSetAction() error = %v", taskErr)
	}
	bodyReader, err := adaptor.BuildRequestBody(ctx, info)
	if err != nil {
		t.Fatalf("BuildRequestBody() error = %v", err)
	}
	bodyBytes, err := io.ReadAll(bodyReader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	var got requestPayload
	if err := common.Unmarshal(bodyBytes, &got); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	var wire map[string]interface{}
	if err := common.Unmarshal(bodyBytes, &wire); err != nil {
		t.Fatalf("unmarshal request wire body: %v", err)
	}
	for _, field := range []string{"image_urls", "duration"} {
		if _, exists := wire[field]; exists {
			t.Fatalf("upstream request must not contain legacy field %q: %s", field, bodyBytes)
		}
	}
	for _, field := range []string{"images", "seconds"} {
		if _, exists := wire[field]; !exists {
			t.Fatalf("upstream request must contain ZZone field %q: %s", field, bodyBytes)
		}
	}
	if got.Model != ModelName {
		t.Fatalf("model = %q, want %q", got.Model, ModelName)
	}
	if got.Prompt != "make the subject move naturally" {
		t.Fatalf("prompt = %q", got.Prompt)
	}
	if got.Seconds != minDuration {
		t.Fatalf("seconds = %d, want %d", got.Seconds, minDuration)
	}
	if len(got.Images) != 1 || !strings.HasPrefix(got.Images[0], "data:image/png;base64,") {
		t.Fatalf("images = %#v", got.Images)
	}
}

func TestParseTaskResultAcceptsSecureSkillDurationEncodings(t *testing.T) {
	adaptor := &TaskAdaptor{}
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "number", body: `{"status":"completed","duration":5}`},
		{name: "integral decimal number", body: `{"status":"completed","duration":5.0}`},
		{name: "numeric string", body: `{"status":"completed","duration":"5"}`},
		{name: "numeric decimal string", body: `{"status":"completed","duration":"5.0"}`},
		{name: "provider seconds suffix", body: `{"status":"completed","duration":"6s","seconds":"6"}`},
		{name: "integral exponent", body: `{"status":"completed","duration":50e-1}`},
		{name: "null", body: `{"status":"completed","duration":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := adaptor.ParseTaskResult([]byte(tc.body)); err != nil {
				t.Fatalf("ParseTaskResult() error = %v", err)
			}
		})
	}
}

func TestParseTaskResultRejectsInvalidSecureSkillDuration(t *testing.T) {
	adaptor := &TaskAdaptor{}
	for _, body := range []string{
		`{"status":"completed","duration":"not-a-number"}`,
		`{"status":"completed","duration":5.5}`,
		`{"status":"completed","duration":true}`,
		`{"status":"completed","duration":"9223372036854775808"}`,
		`{"status":"completed","duration":5.0000000000000001}`,
		`{"status":"completed","duration":"5e-1"}`,
		`{"status":"completed","duration":"01"}`,
		`{"status":"completed","duration":"6seconds"}`,
		`{"status":"completed","duration":false}`,
	} {
		if _, err := adaptor.ParseTaskResult([]byte(body)); err == nil {
			t.Fatalf("ParseTaskResult(%s) unexpectedly succeeded", body)
		}
	}
}

func TestBuildRequestHeaderAddsIdempotencyKeyOnlyForSecureSkillNativeH3(t *testing.T) {
	tests := []struct {
		name        string
		channelType int
		want        string
	}{
		{name: "native H3", channelType: constant.ChannelTypeSecureSkillNativeH3, want: "aibuff-task_public"},
		{name: "legacy ZZone contract", channelType: constant.ChannelTypeSecureSkill, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := testInfoForType("https://upstream.invalid", tt.channelType)
			adaptor := &TaskAdaptor{}
			adaptor.Init(info)
			req := httptest.NewRequest(http.MethodPost, "https://upstream.invalid/v1/videos", nil)
			if err := adaptor.BuildRequestHeader(nil, req, info); err != nil {
				t.Fatalf("BuildRequestHeader() error = %v", err)
			}
			if got := req.Header.Get("Idempotency-Key"); got != tt.want {
				t.Fatalf("Idempotency-Key = %q, want %q", got, tt.want)
			}
			if tt.channelType == constant.ChannelTypeSecureSkillNativeH3 && req.GetBody != nil {
				t.Fatal("native H3 request must not expose GetBody to the HTTP client")
			}
		})
	}
}

func TestNativeH3CreateSendsOneDeterministicIdempotencyKey(t *testing.T) {
	service.InitHttpClient()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/videos" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		requests++
		if got := r.Header.Get("Idempotency-Key"); got != "aibuff-task_public" {
			t.Fatalf("Idempotency-Key = %q, want aibuff-task_public", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"h3-task-1","status":"queued"}`)
	}))
	defer server.Close()

	info := testInfoForType(server.URL, constant.ChannelTypeSecureSkillNativeH3)
	adaptor := &TaskAdaptor{}
	adaptor.Init(info)
	ctx := jsonContext(`{"model":"minimax-h3","prompt":"move","images":["https://example.test/input.png"],"duration":5}`)
	if taskErr := adaptor.ValidateRequestAndSetAction(ctx, info); taskErr != nil {
		t.Fatalf("validate: %v", taskErr)
	}
	body, err := adaptor.BuildRequestBody(ctx, info)
	if err != nil {
		t.Fatalf("BuildRequestBody() error = %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/videos", body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := adaptor.BuildRequestHeader(ctx, req, info); err != nil {
		t.Fatalf("BuildRequestHeader() error = %v", err)
	}
	resp, err := service.GetHttpClient().Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || requests != 1 {
		t.Fatalf("status=%d requests=%d, want 200/1", resp.StatusCode, requests)
	}
}

func TestNativeH3CreateDoesNotFollowBodyPreservingRedirect(t *testing.T) {
	service.InitHttpClient()
	redirects := 0
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/videos":
			posts++
			w.Header().Set("Location", "/redirect-target")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "/redirect-target":
			redirects++
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	info := testInfoForType(server.URL, constant.ChannelTypeSecureSkillNativeH3)
	adaptor := &TaskAdaptor{}
	adaptor.Init(info)
	ctx := jsonContext(`{"model":"minimax-h3","prompt":"move","images":["https://example.test/input.png"],"duration":5}`)
	if taskErr := adaptor.ValidateRequestAndSetAction(ctx, info); taskErr != nil {
		t.Fatalf("validate: %v", taskErr)
	}
	body, err := adaptor.BuildRequestBody(ctx, info)
	if err != nil {
		t.Fatalf("BuildRequestBody() error = %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/videos", body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := adaptor.BuildRequestHeader(ctx, req, info); err != nil {
		t.Fatalf("BuildRequestHeader() error = %v", err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", resp.StatusCode)
	}
	if posts != 1 || redirects != 0 {
		t.Fatalf("POSTs=%d redirects=%d, want 1/0", posts, redirects)
	}
}

func TestBuildRequestBodyKeepsSecureSkillAndZZoneWireContractsIsolated(t *testing.T) {
	tests := []struct {
		name      string
		channel   int
		want      []string
		forbidden []string
	}{
		{name: "zzone", channel: constant.ChannelTypeSecureSkill, want: []string{"images", "seconds"}, forbidden: []string{"image_urls", "duration"}},
		{name: "secureskill_native", channel: constant.ChannelTypeSecureSkillNativeH3, want: []string{"image_urls", "duration"}, forbidden: []string{"images", "seconds"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := jsonContext(`{"model":"minimax-h3","prompt":"move","images":["https://example.test/input.png"],"duration":5}`)
			info := testInfoForType("https://upstream.invalid", tt.channel)
			adaptor := &TaskAdaptor{}
			adaptor.Init(info)
			if taskErr := adaptor.ValidateRequestAndSetAction(ctx, info); taskErr != nil {
				t.Fatalf("ValidateRequestAndSetAction() error = %v", taskErr)
			}
			body, err := adaptor.BuildRequestBody(ctx, info)
			if err != nil {
				t.Fatalf("BuildRequestBody() error = %v", err)
			}
			var wire map[string]interface{}
			if err := common.Unmarshal(readAll(t, body), &wire); err != nil {
				t.Fatalf("unmarshal wire body: %v", err)
			}
			for _, field := range tt.want {
				if _, ok := wire[field]; !ok {
					t.Fatalf("missing %q in %#v", field, wire)
				}
			}
			for _, field := range tt.forbidden {
				if _, ok := wire[field]; ok {
					t.Fatalf("forbidden %q in %#v", field, wire)
				}
			}
		})
	}
}

func TestValidateRequestFailsClosedBeforeUpstreamForUnsupportedH3Inputs(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "pure text", body: `{"model":"minimax-h3","prompt":"text only"}`, want: "at least one input image"},
		{name: "too many images", body: `{"model":"minimax-h3","prompt":"many","images":["https://a/1.png","https://a/2.png","https://a/3.png","https://a/4.png","https://a/5.png","https://a/6.png"]}`, want: "at most 5"},
		{name: "audio urls", body: `{"model":"minimax-h3","prompt":"audio","images":["https://a/1.png"],"audio_urls":["https://a/audio.mp3"]}`, want: "audio input"},
		{name: "metadata audio urls", body: `{"model":"minimax-h3","prompt":"audio","images":["https://a/1.png"],"metadata":{"audio_urls":["https://a/audio.mp3"]}}`, want: "audio input"},
		{name: "reference video", body: `{"model":"minimax-h3","prompt":"video","images":["https://a/1.png"],"reference_video_url":"https://a/ref.mp4"}`, want: "reference video"},
		{name: "unverified resolution", body: `{"model":"minimax-h3","prompt":"size","images":["https://a/1.png"],"size":"720p"}`, want: "resolution/size"},
		{name: "wrong model", body: `{"model":"minimax-h2","prompt":"wrong model","images":["https://a/1.png"]}`, want: "unsupported minimax-h3 model"},
		{name: "duration zero", body: `{"model":"minimax-h3","prompt":"zero","images":["https://a/1.png"],"duration":0}`, want: "between 5 and 15"},
		{name: "duration invalid", body: `{"model":"minimax-h3","prompt":"invalid","images":["https://a/1.png"],"duration":"not-a-number"}`, want: "invalid minimax-h3 duration"},
		{name: "duration below contract", body: `{"model":"minimax-h3","prompt":"short","images":["https://a/1.png"],"duration":4}`, want: "between 5 and 15"},
		{name: "duration above contract", body: `{"model":"minimax-h3","prompt":"long","images":["https://a/1.png"],"duration":16}`, want: "between 5 and 15"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := jsonContext(tt.body)
			info := testInfo("https://upstream.invalid")
			adaptor := &TaskAdaptor{}
			adaptor.Init(info)
			taskErr := adaptor.ValidateRequestAndSetAction(ctx, info)
			if taskErr == nil {
				t.Fatal("ValidateRequestAndSetAction() unexpectedly succeeded")
			}
			if !strings.Contains(strings.ToLower(taskErr.Message), strings.ToLower(tt.want)) {
				t.Fatalf("error = %q, want substring %q", taskErr.Message, tt.want)
			}
		})
	}
}

func TestValidateRequestAcceptsOneTwoAndFiveImages(t *testing.T) {
	for _, count := range []int{1, 2, 5} {
		t.Run(fmt.Sprintf("%d_images", count), func(t *testing.T) {
			refs := make([]string, count)
			for i := range refs {
				refs[i] = fmt.Sprintf("https://example.test/input-%d.png", i+1)
			}
			payload := map[string]interface{}{
				"model":  ModelName,
				"prompt": "move",
				"images": refs,
			}
			body, err := common.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			ctx := jsonContext(string(body))
			info := testInfo("https://upstream.invalid")
			adaptor := &TaskAdaptor{}
			adaptor.Init(info)
			if taskErr := adaptor.ValidateRequestAndSetAction(ctx, info); taskErr != nil {
				t.Fatalf("ValidateRequestAndSetAction() error = %v", taskErr)
			}
			got, err := relaycommon.GetTaskRequest(ctx)
			if err != nil {
				t.Fatalf("GetTaskRequest() error = %v", err)
			}
			if len(got.Images) != count {
				t.Fatalf("images = %d, want %d", len(got.Images), count)
			}
		})
	}
}

func TestDurationDecimalJSONIsNotSilentlyDefaulted(t *testing.T) {
	ctx := jsonContext(`{"model":"minimax-h3","prompt":"move","images":["https://example.test/input.png"],"duration":8.0}`)
	info := testInfo("https://upstream.invalid")
	adaptor := &TaskAdaptor{}
	adaptor.Init(info)
	if taskErr := adaptor.ValidateRequestAndSetAction(ctx, info); taskErr != nil {
		t.Fatalf("ValidateRequestAndSetAction() error = %v", taskErr)
	}
	body, err := adaptor.BuildRequestBody(ctx, info)
	if err != nil {
		t.Fatalf("BuildRequestBody() error = %v", err)
	}
	var got requestPayload
	if err := common.Unmarshal(readAll(t, body), &got); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if got.Seconds != 8 {
		t.Fatalf("seconds = %d, want 8 (JSON 8.0 must not silently become default 5)", got.Seconds)
	}
}

func TestDurationUpperBoundary15IsAcceptedAndForwarded(t *testing.T) {
	ctx := jsonContext(`{"model":"minimax-h3","prompt":"move","images":["https://example.test/input.png"],"duration":15}`)
	info := testInfo("https://upstream.invalid")
	adaptor := &TaskAdaptor{}
	adaptor.Init(info)
	if taskErr := adaptor.ValidateRequestAndSetAction(ctx, info); taskErr != nil {
		t.Fatalf("ValidateRequestAndSetAction() rejected duration 15: %v", taskErr)
	}
	body, err := adaptor.BuildRequestBody(ctx, info)
	if err != nil {
		t.Fatalf("BuildRequestBody() error = %v", err)
	}
	var got requestPayload
	if err := common.Unmarshal(readAll(t, body), &got); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if got.Seconds != maxDuration {
		t.Fatalf("seconds = %d, want %d", got.Seconds, maxDuration)
	}
}

func TestInvalidH3BoundariesStopBeforeUpstreamTaskOrBillingStages(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "pure text", body: `{"model":"minimax-h3","prompt":"text only"}`},
		{name: "zero images", body: `{"model":"minimax-h3","prompt":"zero","images":[]}`},
		{name: "more than five images", body: `{"model":"minimax-h3","prompt":"many","images":["https://a/1.png","https://a/2.png","https://a/3.png","https://a/4.png","https://a/5.png","https://a/6.png"]}`},
		{name: "audio", body: `{"model":"minimax-h3","prompt":"audio","images":["https://a/1.png"],"audio_urls":["https://a/audio.mp3"]}`},
		{name: "reference video", body: `{"model":"minimax-h3","prompt":"video","images":["https://a/1.png"],"reference_video_url":"https://a/ref.mp4"}`},
		{name: "wrong model", body: `{"model":"minimax-h2","prompt":"wrong","images":["https://a/1.png"]}`},
		{name: "invalid size", body: `{"model":"minimax-h3","prompt":"size","images":["https://a/1.png"],"size":"720p"}`},
		{name: "duration zero", body: `{"model":"minimax-h3","prompt":"zero","images":["https://a/1.png"],"duration":0}`},
		{name: "duration invalid", body: `{"model":"minimax-h3","prompt":"invalid","images":["https://a/1.png"],"duration":"bad"}`},
		{name: "duration above limit", body: `{"model":"minimax-h3","prompt":"long","images":["https://a/1.png"],"duration":16}`},
	}

	upstreamPosts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/videos" {
			upstreamPosts++
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"unexpected","status":"queued"}`)
	}))
	defer server.Close()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := jsonContext(tt.body)
			info := testInfo(server.URL)
			adaptor := &TaskAdaptor{}
			adaptor.Init(info)

			// These counters model the controller stages that must remain
			// unreachable until validation succeeds.
			fakeTasks, fakeBillingSessions, fakePreConsumes := 0, 0, 0
			taskErr := adaptor.ValidateRequestAndSetAction(ctx, info)
			if taskErr == nil {
				fakeBillingSessions++
				fakePreConsumes++
				resp, err := http.Post(server.URL+"/v1/videos", "application/json", strings.NewReader(`{"model":"minimax-h3"}`))
				if err != nil {
					t.Fatalf("unexpected fake submit error: %v", err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					fakeTasks++
				}
			}
			if taskErr == nil {
				t.Fatalf("invalid boundary unexpectedly passed validation; tasks=%d billing_sessions=%d pre_consumes=%d", fakeTasks, fakeBillingSessions, fakePreConsumes)
			}
			if fakeTasks != 0 || fakeBillingSessions != 0 || fakePreConsumes != 0 {
				t.Fatalf("invalid boundary reached stages: tasks=%d billing_sessions=%d pre_consumes=%d", fakeTasks, fakeBillingSessions, fakePreConsumes)
			}
		})
	}
	if upstreamPosts != 0 {
		t.Fatalf("invalid boundaries issued %d upstream POST requests, want zero", upstreamPosts)
	}
}

func TestValidateMultipartRejectsAudioAndReferenceVideo(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
		file  bool
		want  string
	}{
		{name: "audio urls", field: "audio_urls", value: "https://example.test/audio.mp3", want: "audio input"},
		{name: "reference video file", field: "reference_video", file: true, want: "reference video"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, contentType := multipartBodyWithExtra(t, tt.field, tt.value, tt.file)
			req := httptest.NewRequest(http.MethodPost, "/v1/videos", body)
			req.Header.Set("Content-Type", contentType)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = req
			info := testInfo("https://upstream.invalid")
			adaptor := &TaskAdaptor{}
			adaptor.Init(info)
			taskErr := adaptor.ValidateRequestAndSetAction(ctx, info)
			if taskErr == nil {
				t.Fatal("ValidateRequestAndSetAction() unexpectedly succeeded")
			}
			if !strings.Contains(strings.ToLower(taskErr.Message), strings.ToLower(tt.want)) {
				t.Fatalf("error = %q, want substring %q", taskErr.Message, tt.want)
			}
		})
	}
}

func TestValidateMultipartRejectsInvalidDurations(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "zero", value: "0", want: "between 5 and 15"},
		{name: "invalid", value: "not-a-number", want: "invalid minimax-h3 duration"},
		{name: "below contract", value: "4", want: "between 5 and 15"},
		{name: "too long", value: "16", want: "between 5 and 15"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, contentType := multipartBodyWithExtra(t, "duration", tt.value, false)
			req := httptest.NewRequest(http.MethodPost, "/v1/videos", body)
			req.Header.Set("Content-Type", contentType)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = req
			info := testInfo("https://upstream.invalid")
			adaptor := &TaskAdaptor{}
			adaptor.Init(info)
			taskErr := adaptor.ValidateRequestAndSetAction(ctx, info)
			if taskErr == nil {
				t.Fatal("ValidateRequestAndSetAction() unexpectedly succeeded")
			}
			if !strings.Contains(strings.ToLower(taskErr.Message), strings.ToLower(tt.want)) {
				t.Fatalf("error = %q, want substring %q", taskErr.Message, tt.want)
			}
		})
	}
}

func TestFakeUpstreamCreateAndReadOnlyPollingLifecycle(t *testing.T) {
	service.InitHttpClient()
	var mu sync.Mutex
	createRequests := 0
	statusRequests := 0
	contentRequests := 0
	statusBodies := []string{
		`{"id":"h3-task-1","status":"queued"}`,
		`{"id":"h3-task-1","status":"in_progress","progress":42}`,
		`{"id":"h3-task-1","status":"completed"}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos":
			createRequests++
			payload, _ := io.ReadAll(r.Body)
			var request requestPayload
			if err := common.Unmarshal(payload, &request); err != nil {
				t.Errorf("fake upstream request decode: %v", err)
			}
			if request.Model != ModelName || len(request.Images) != 1 || request.Seconds != minDuration {
				t.Errorf("unexpected create payload: %+v", request)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"h3-task-1","status":"queued"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/h3-task-1":
			statusRequests++
			idx := statusRequests - 1
			if idx >= len(statusBodies) {
				idx = len(statusBodies) - 1
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, statusBodies[idx])
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/h3-task-1/content":
			contentRequests++
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"message":"content unavailable"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adaptor := &TaskAdaptor{}
	info := testInfo(server.URL)
	adaptor.Init(info)

	// Use the adapter's validated JSON contract to create exactly one task.
	ctx := jsonContext(`{"model":"minimax-h3","prompt":"move","images":["https://example.test/input.png"],"duration":5}`)
	if taskErr := adaptor.ValidateRequestAndSetAction(ctx, info); taskErr != nil {
		t.Fatalf("validate: %v", taskErr)
	}
	bodyReader, err := adaptor.BuildRequestBody(ctx, info)
	if err != nil {
		t.Fatalf("build body: %v", err)
	}
	createReq, err := http.NewRequest(http.MethodPost, server.URL+"/v1/videos", bodyReader)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	createReq.Header.Set("Authorization", "Bearer test-key")
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := service.GetHttpClient().Do(createReq)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	createBody, _ := io.ReadAll(createResp.Body)
	_ = createResp.Body.Close()
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", createResp.StatusCode, createBody)
	}
	publicTaskID, _, taskErr := adaptor.DoResponse(ctx, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(createBody)),
	}, info)
	if taskErr != nil {
		t.Fatalf("DoResponse() error = %v", taskErr)
	}
	if publicTaskID != "h3-task-1" {
		t.Fatalf("upstream task ID = %q, want h3-task-1", publicTaskID)
	}

	for _, want := range []model.TaskStatus{model.TaskStatusQueued, model.TaskStatusInProgress, model.TaskStatusSuccess} {
		resp, err := adaptor.FetchTask(server.URL, "test-key", map[string]interface{}{"task_id": "h3-task-1"}, "")
		if err != nil {
			t.Fatalf("FetchTask() error = %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		parsed, err := adaptor.ParseTaskResult(body)
		if err != nil {
			t.Fatalf("ParseTaskResult() error = %v", err)
		}
		if parsed.Status != string(want) {
			t.Fatalf("status = %s, want %s", parsed.Status, want)
		}
	}

	// Content is a separate read-only GET.  A failed content fetch must not
	// recreate the task or increment the fake upstream's create/charge count.
	contentResp, err := service.GetHttpClient().Get(BuildContentURL(server.URL, "h3-task-1"))
	if err != nil {
		t.Fatalf("content GET error = %v", err)
	}
	_, _ = io.ReadAll(contentResp.Body)
	_ = contentResp.Body.Close()
	if contentResp.StatusCode != http.StatusBadGateway {
		t.Fatalf("content status = %d, want 502", contentResp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if createRequests != 1 {
		t.Fatalf("create requests = %d, want exactly 1", createRequests)
	}
	if statusRequests != 3 {
		t.Fatalf("status requests = %d, want 3", statusRequests)
	}
	if contentRequests != 1 {
		t.Fatalf("content requests = %d, want 1", contentRequests)
	}
}

func TestFakeUpstreamFailureDoesNotReturnTaskID(t *testing.T) {
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"h3-failed","status":"failed","error":{"code":"quota","message":"provider rejected task"}}`)
	}))
	defer server.Close()

	ctx := jsonContext(`{"model":"minimax-h3","prompt":"fail","images":["https://example.test/input.png"]}`)
	info := testInfo(server.URL)
	adaptor := &TaskAdaptor{}
	adaptor.Init(info)
	if taskErr := adaptor.ValidateRequestAndSetAction(ctx, info); taskErr != nil {
		t.Fatalf("validate: %v", taskErr)
	}
	bodyReader, err := adaptor.BuildRequestBody(ctx, info)
	if err != nil {
		t.Fatalf("build body: %v", err)
	}
	failureBody := doLocalPost(t, server.URL+"/v1/videos", bodyReader)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(failureBody))}
	defer resp.Body.Close()
	taskID, _, taskErr := adaptor.DoResponse(httptestContext(), resp, info)
	if taskErr == nil {
		t.Fatal("DoResponse() unexpectedly succeeded")
	}
	if taskID != "" {
		t.Fatalf("task ID = %q, want empty", taskID)
	}
}

func TestFakeUpstreamFailedStatusIsTerminalAndDoesNotRecreateTask(t *testing.T) {
	service.InitHttpClient()
	var mu sync.Mutex
	createRequests := 0
	statusRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos":
			createRequests++
			_, _ = io.WriteString(w, `{"id":"h3-terminal-failure","status":"queued"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/h3-terminal-failure":
			statusRequests++
			_, _ = io.WriteString(w, `{"id":"h3-terminal-failure","status":"failed","error":{"message":"provider rejected task"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	info := testInfo(server.URL)
	adaptor := &TaskAdaptor{}
	adaptor.Init(info)
	ctx := jsonContext(`{"model":"minimax-h3","prompt":"move","images":["https://example.test/input.png"]}`)
	if taskErr := adaptor.ValidateRequestAndSetAction(ctx, info); taskErr != nil {
		t.Fatalf("validate: %v", taskErr)
	}
	bodyReader, err := adaptor.BuildRequestBody(ctx, info)
	if err != nil {
		t.Fatalf("build body: %v", err)
	}
	createBody := doLocalPost(t, server.URL+"/v1/videos", bodyReader)
	createResult, err := adaptor.ParseTaskResult([]byte(`{"status":"queued"}`))
	if err != nil || createResult.Status != string(model.TaskStatusQueued) {
		t.Fatalf("initial task parse = %#v, err=%v", createResult, err)
	}
	// The controller would persist the provider ID from DoResponse and then
	// poll it.  The failed poll is terminal; it must not call POST again.
	_, _, taskErr := adaptor.DoResponse(httptestContext(), &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(createBody)),
	}, info)
	if taskErr != nil {
		t.Fatalf("DoResponse() error = %v", taskErr)
	}
	resp, err := adaptor.FetchTask(server.URL, "test-key", map[string]interface{}{"task_id": "h3-terminal-failure"}, "")
	if err != nil {
		t.Fatalf("FetchTask() error = %v", err)
	}
	statusBody := readAll(t, resp.Body)
	_ = resp.Body.Close()
	parsed, err := adaptor.ParseTaskResult(statusBody)
	if err != nil {
		t.Fatalf("ParseTaskResult() error = %v", err)
	}
	if parsed.Status != string(model.TaskStatusFailure) {
		t.Fatalf("status = %s, want %s", parsed.Status, model.TaskStatusFailure)
	}

	mu.Lock()
	defer mu.Unlock()
	if createRequests != 1 || statusRequests != 1 {
		t.Fatalf("create requests=%d status requests=%d, want 1/1", createRequests, statusRequests)
	}
}

func TestFakeUpstreamTimeoutIsReadOnlyAndDoesNotCreateTask(t *testing.T) {
	service.InitHttpClient()
	createRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			createRequests++
		}
		if r.Method == http.MethodGet {
			<-time.After(100 * time.Millisecond)
		}
	}))
	defer server.Close()

	client := &http.Client{Timeout: 20 * time.Millisecond}
	resp, err := client.Get(server.URL + "/v1/videos/h3-timeout")
	if err == nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		t.Fatal("timeout GET unexpectedly succeeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("error = %v, want timeout", err)
	}
	if createRequests != 0 {
		t.Fatalf("create requests = %d, want 0", createRequests)
	}
}

func TestBuildContentURLEscapesTaskIDAndUsesReadOnlyPath(t *testing.T) {
	got := BuildContentURL("https://token.secure-skill.com/v1/", "task/with spaces")
	want := "https://token.secure-skill.com/v1/videos/task%2Fwith%20spaces/content"
	if got != want {
		t.Fatalf("BuildContentURL() = %q, want %q", got, want)
	}
}

func jsonContext(body string) *gin.Context {
	req := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = req
	return ctx
}

func httptestContext() *gin.Context {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	return ctx
}

func testInfo(baseURL string) *relaycommon.RelayInfo {
	return testInfoForType(baseURL, constant.ChannelTypeSecureSkill)
}

func testInfoForType(baseURL string, channelType int) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       channelType,
			ChannelBaseUrl:    baseURL,
			ApiKey:            "test-key",
			UpstreamModelName: ModelName,
		},
		OriginModelName: ModelName,
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"},
	}
}

func multipartBody(t *testing.T, imageCount int) (io.Reader, string) {
	return multipartBodyWithImageCountAndExtra(t, imageCount, "", "", false)
}

func multipartBodyWithExtra(t *testing.T, extraField, extraValue string, extraFile bool) (io.Reader, string) {
	return multipartBodyWithImageCountAndExtra(t, 1, extraField, extraValue, extraFile)
}

func multipartBodyWithImageCountAndExtra(t *testing.T, imageCount int, extraField, extraValue string, extraFile bool) (io.Reader, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("prompt", "make the subject move naturally"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if err := writer.WriteField("model", ModelName); err != nil {
		t.Fatalf("model: %v", err)
	}
	for i := 0; i < imageCount; i++ {
		part, err := writer.CreateFormFile("input_reference", "input.png")
		if err != nil {
			t.Fatalf("file part: %v", err)
		}
		if _, err := part.Write([]byte("\x89PNG\r\n\x1a\nlocal-test-image")); err != nil {
			t.Fatalf("file bytes: %v", err)
		}
	}
	if extraField != "" {
		if extraFile {
			part, err := writer.CreateFormFile(extraField, "reference.mp4")
			if err != nil {
				t.Fatalf("extra file part: %v", err)
			}
			if _, err := part.Write([]byte("fake-video")); err != nil {
				t.Fatalf("extra file bytes: %v", err)
			}
		} else if err := writer.WriteField(extraField, extraValue); err != nil {
			t.Fatalf("extra field: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return bytes.NewReader(body.Bytes()), writer.FormDataContentType()
}

func doLocalPost(t *testing.T, endpoint string, body io.Reader) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, endpoint, body)
	if err != nil {
		t.Fatalf("new POST: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := service.GetHttpClient().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	return readAll(t, resp.Body)
}

func readAll(t *testing.T, reader io.Reader) []byte {
	t.Helper()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return data
}
