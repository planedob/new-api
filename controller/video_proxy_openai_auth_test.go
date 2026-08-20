package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestVideoProxyKeepsOpenAIAndSoraAuthorizationHeader guards the pre-existing
// Sora/OpenAI video content contract: /content is proxied with the channel key
// as a bearer token.  The SecureSkill H3 work must not drop that header.
func TestVideoProxyKeepsOpenAIAndSoraAuthorizationHeader(t *testing.T) {
	db := openSecureSkillControllerIntegrationDB(t)
	fetchSetting := system_setting.GetFetchSetting()
	oldFetchSetting := *fetchSetting
	fetchSetting.EnableSSRFProtection = false
	fetchSetting.AllowPrivateIp = true
	t.Cleanup(func() { *fetchSetting = oldFetchSetting })

	for _, tt := range []struct {
		name        string
		channelType int
		taskID      string
		upstreamID  string
		userID      int
		channelID   int
		channelKey  string
	}{
		{name: "sora", channelType: constant.ChannelTypeSora, taskID: "task_sora_content", upstreamID: "provider-sora", userID: 800, channelID: 801, channelKey: "sora-channel-key"},
		{name: "openai", channelType: constant.ChannelTypeOpenAI, taskID: "task_openai_content", upstreamID: "provider-openai", userID: 802, channelID: 803, channelKey: "openai-channel-key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			gotAuthorization, gotPath := "", ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				gotAuthorization = r.Header.Get("Authorization")
				gotPath = r.URL.Path
				mu.Unlock()
				w.Header().Set("Content-Type", "video/mp4")
				_, _ = io.WriteString(w, "fake-video-bytes")
			}))
			defer server.Close()

			baseURL := server.URL
			require.NoError(t, db.Create(&model.Channel{
				Id: tt.channelID, Type: tt.channelType, Key: tt.channelKey,
				BaseURL: &baseURL, Status: common.ChannelStatusEnabled, Name: "video content fake",
			}).Error)
			require.NoError(t, db.Create(&model.Task{
				TaskID: tt.taskID, UserId: tt.userID, ChannelId: tt.channelID,
				Platform: constant.TaskPlatform(strconv.Itoa(tt.channelType)),
				Status:   model.TaskStatus(model.TaskStatusSuccess), Progress: "100%",
				PrivateData: model.TaskPrivateData{UpstreamTaskID: tt.upstreamID},
			}).Error)

			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/"+tt.taskID+"/content", nil)
			ctx.Params = gin.Params{{Key: "task_id", Value: tt.taskID}}
			ctx.Set("id", tt.userID)
			VideoProxy(ctx)

			require.Equal(t, http.StatusOK, recorder.Code)
			mu.Lock()
			gotAuth, path := gotAuthorization, gotPath
			mu.Unlock()
			require.Equal(t, "/v1/videos/"+tt.upstreamID+"/content", path)
			require.Equal(t, "Bearer "+tt.channelKey, gotAuth, "OpenAI/Sora video content must stay authorized with the channel key")
		})
	}
}
