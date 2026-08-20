package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/task/secureskill"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

func TestSecureSkillInvalidBoundariesStopBeforeTaskBillingOrUpstream(t *testing.T) {
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

	gin.SetMode(gin.TestMode)
	var mu sync.Mutex
	upstreamPosts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			upstreamPosts++
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"unexpected","status":"queued"}`)
	}))
	defer server.Close()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := secureSkillTaskContext(tt.body, server.URL)
			info := &relaycommon.RelayInfo{
				UserId:          901,
				OriginModelName: secureskill.ModelName,
				UsingGroup:      "default",
				UserGroup:       "default",
				TokenGroup:      "default",
				TaskRelayInfo:   &relaycommon.TaskRelayInfo{},
			}
			result, taskErr := RelayTaskSubmit(ctx, info)
			if taskErr == nil {
				t.Fatal("invalid boundary unexpectedly passed RelayTaskSubmit")
			}
			if result != nil {
				t.Fatalf("invalid boundary returned task result: %#v", result)
			}
			if info.Billing != nil {
				t.Fatal("invalid boundary created a BillingSession")
			}
			if info.FinalPreConsumedQuota != 0 {
				t.Fatalf("invalid boundary pre-consumed quota = %d, want 0", info.FinalPreConsumedQuota)
			}
			if info.PublicTaskID != "" {
				t.Fatalf("invalid boundary allocated public task ID %q", info.PublicTaskID)
			}
		})
	}

	mu.Lock()
	defer mu.Unlock()
	if upstreamPosts != 0 {
		t.Fatalf("invalid boundaries issued %d upstream POST requests, want zero", upstreamPosts)
	}
}

func secureSkillTaskContext(body, baseURL string) *gin.Context {
	req := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = req
	ctx.Set("channel_type", constant.ChannelTypeSecureSkill)
	ctx.Set("platform", strconv.Itoa(constant.ChannelTypeSecureSkill))
	common.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeSecureSkill)
	common.SetContextKey(ctx, constant.ContextKeyChannelBaseUrl, baseURL)
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "test-key")
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 901)
	common.SetContextKey(ctx, constant.ContextKeyOriginalModel, secureskill.ModelName)
	return ctx
}
