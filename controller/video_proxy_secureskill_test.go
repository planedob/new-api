package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/secureskill"
)

func TestSecureSkillContentAuthorizationUsesTaskKeyAndLegacyFallback(t *testing.T) {
	tests := []struct {
		name       string
		taskKey    string
		channelKey string
		want       string
	}{
		{name: "new task selected key", taskKey: "selected-runtime-key", channelKey: "channel-key", want: "selected-runtime-key"},
		{name: "legacy task fallback", channelKey: "legacy-channel-key", want: "legacy-channel-key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "https://token.secure-skill.test/v1/videos/h3/content", nil)
			task := &model.Task{
				TaskID: "task_h3_public",
				PrivateData: model.TaskPrivateData{
					Key: tt.taskKey,
				},
			}
			channel := &model.Channel{
				Type: constant.ChannelTypeSecureSkill,
				Key:  tt.channelKey,
			}
			if err := setSecureSkillContentAuthorization(req, task, channel); err != nil {
				t.Fatalf("setSecureSkillContentAuthorization() error = %v", err)
			}
			if got := req.Header.Get("Authorization"); got != "Bearer "+tt.want {
				t.Fatalf("Authorization = %q, want Bearer %q", got, tt.want)
			}

			publicBody, err := (&secureskill.TaskAdaptor{}).ConvertToOpenAIVideo(task)
			if err != nil {
				t.Fatalf("ConvertToOpenAIVideo() error = %v", err)
			}
			if (tt.taskKey != "" && strings.Contains(string(publicBody), tt.taskKey)) ||
				(tt.channelKey != "" && strings.Contains(string(publicBody), tt.channelKey)) {
				t.Fatalf("public response leaked a channel key: %s", publicBody)
			}
		})
	}
}
