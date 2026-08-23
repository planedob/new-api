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
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImageBatchContentProxyUsesOwnerScopeAndTaskKeySnapshot(t *testing.T) {
	db := openSecureSkillControllerIntegrationDB(t)
	const userID, channelID = 7101, 7201
	const publicID, upstreamID = "task_public_batch", "provider_private_batch"
	const snapshotKey = "snapshot-codefox-key"

	var mu sync.Mutex
	gotPath, gotAuthorization := "", ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.WriteString(w, "fake-png")
	}))
	defer upstream.Close()

	baseURL := upstream.URL
	channel := &model.Channel{
		Id: channelID, Type: constant.ChannelTypeCodeFoxAsync, Key: "rotated-channel-key",
		BaseURL: &baseURL, Status: common.ChannelStatusEnabled, Name: "CodeFox async fake",
	}
	require.NoError(t, db.Create(channel).Error)
	task := &model.Task{
		TaskID: publicID, UserId: userID, ChannelId: channelID,
		Platform: constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeCodeFoxAsync)),
		Status:   model.TaskStatusSuccess, Progress: "100%",
		PrivateData: model.TaskPrivateData{UpstreamTaskID: upstreamID, Key: snapshotKey},
		Data:        []byte(`{"success":true,"data":{"status":"COMPLETED","progress":{"total":1,"success":1,"failed":0,"pending":0},"results":[{"item_index":0,"image_url":"https://provider.invalid/0","status":"SUCCESS"}]}}`),
	}
	require.NoError(t, db.Create(task).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/images/batches/"+publicID+"/items/0/content", nil)
	ctx.Params = gin.Params{{Key: "task_id", Value: publicID}, {Key: "item_index", Value: "0"}}
	ctx.Set("id", userID)
	ImageBatchContentProxy(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "fake-png", recorder.Body.String())
	mu.Lock()
	path, authorization := gotPath, gotAuthorization
	mu.Unlock()
	require.Equal(t, "/api/ecommerce/images/"+upstreamID+"/0", path)
	require.Equal(t, "Bearer "+snapshotKey, authorization)
	require.NotContains(t, recorder.Body.String(), upstreamID)
	require.NotContains(t, recorder.Body.String(), snapshotKey)

	wrongUserRecorder := httptest.NewRecorder()
	wrongUser, _ := gin.CreateTestContext(wrongUserRecorder)
	wrongUser.Request = httptest.NewRequest(http.MethodGet, "/v1/images/batches/"+publicID+"/items/0/content", nil)
	wrongUser.Params = gin.Params{{Key: "task_id", Value: publicID}, {Key: "item_index", Value: "0"}}
	wrongUser.Set("id", userID+1)
	ImageBatchContentProxy(wrongUser)
	require.Equal(t, http.StatusNotFound, wrongUserRecorder.Code)
}
