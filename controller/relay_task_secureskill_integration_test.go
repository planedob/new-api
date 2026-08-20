package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/channel/task/secureskill"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSecureSkillRelayTaskUsesRealBillingSessionAndNeverRetriesCreate(t *testing.T) {
	db := openSecureSkillControllerIntegrationDB(t)
	oldPrices := ratio_setting.ModelPrice2JSONString()
	prices := ratio_setting.GetModelPriceMap()
	prices[secureskill.ModelName] = 0.001
	priceJSON, err := common.Marshal(prices)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(priceJSON)))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrices)) })

	for _, statusCode := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusGatewayTimeout} {
		t.Run(strconv.Itoa(statusCode), func(t *testing.T) {
			const initialUserQuota, initialTokenQuota = 10000, 9000
			const expectedPreConsumed = 500
			userID := 1000 + statusCode
			tokenID := 2000 + statusCode
			channelID := 3000 + statusCode
			tokenKey := fmt.Sprintf("fake-token-%d", statusCode)
			seedSecureSkillControllerUser(t, db, userID, initialUserQuota)
			seedSecureSkillControllerToken(t, db, tokenID, userID, tokenKey, initialTokenQuota)

			var mu sync.Mutex
			postCount := 0
			observedUserQuota, observedTokenQuota := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/videos" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				mu.Lock()
				postCount++
				var user model.User
				var token model.Token
				if err := db.Select("quota").Where("id = ?", userID).First(&user).Error; err == nil {
					observedUserQuota = user.Quota
				}
				if err := db.Select("remain_quota").Where("id = ?", tokenID).First(&token).Error; err == nil {
					observedTokenQuota = token.RemainQuota
				}
				mu.Unlock()
				w.WriteHeader(statusCode)
				_, _ = io.WriteString(w, `{"error":"fake SecureSkill create failure"}`)
			}))
			defer server.Close()

			ctx, recorder := secureSkillRelayTaskContext(t, userID, tokenID, tokenKey, channelID, server.URL)
			RelayTask(ctx)

			mu.Lock()
			posts := postCount
			seenUserQuota, seenTokenQuota := observedUserQuota, observedTokenQuota
			mu.Unlock()
			require.Equal(t, 1, posts, "a non-idempotent H3 creation must be attempted once")
			require.Equal(t, initialUserQuota-expectedPreConsumed, seenUserQuota, "real BillingSession must pre-consume wallet quota before POST")
			require.Equal(t, initialTokenQuota-expectedPreConsumed, seenTokenQuota, "real BillingSession must pre-consume token quota before POST")
			require.Equal(t, statusCode, recorder.Code)

			waitForSecureSkillQuotaRestore(t, db, userID, tokenID, initialUserQuota, initialTokenQuota)
			require.Equal(t, 0, countSecureSkillTasks(t, db, userID), "failed creation must not insert a task")
			// A second refund would make either quota exceed its original value.
			require.Equal(t, initialUserQuota, getSecureSkillUserQuota(t, db, userID))
			require.Equal(t, initialTokenQuota, getSecureSkillTokenQuota(t, db, tokenID))
		})
	}
}

func TestSecureSkillSuccessfulCreateThenFailedPollRefundsOnceThroughRealControllerPath(t *testing.T) {
	db := openSecureSkillControllerIntegrationDB(t)
	oldPrices := ratio_setting.ModelPrice2JSONString()
	prices := ratio_setting.GetModelPriceMap()
	prices[secureskill.ModelName] = 0.001
	priceJSON, err := common.Marshal(prices)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(priceJSON)))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrices)) })

	const userID, tokenID, channelID = 1600, 2600, 3600
	const initialUserQuota, initialTokenQuota = 10000, 9000
	const expectedPreConsumed = 500
	tokenKey := "fake-token-task-failed"
	seedSecureSkillControllerUser(t, db, userID, initialUserQuota)
	seedSecureSkillControllerToken(t, db, tokenID, userID, tokenKey, initialTokenQuota)

	var mu sync.Mutex
	postCount, statusGets := 0, 0
	observedUserQuota, observedTokenQuota := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos":
			postCount++
			var user model.User
			var token model.Token
			if err := db.Select("quota").Where("id = ?", userID).First(&user).Error; err == nil {
				observedUserQuota = user.Quota
			}
			if err := db.Select("remain_quota").Where("id = ?", tokenID).First(&token).Error; err == nil {
				observedTokenQuota = token.RemainQuota
			}
			_, _ = io.WriteString(w, `{"id":"provider-task-failed","status":"queued"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/provider-task-failed":
			statusGets++
			_, _ = io.WriteString(w, `{"id":"provider-task-failed","status":"failed","error":{"message":"provider rendered failure"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	baseURL := server.URL
	require.NoError(t, db.Create(&model.Channel{
		Id: channelID, Type: constant.ChannelTypeSecureSkill, Key: "channel-fallback-key",
		BaseURL: &baseURL, Status: common.ChannelStatusEnabled, Name: "SecureSkill task failed fake",
		Models: secureskill.ModelName, Group: "default",
	}).Error)
	ctx, recorder := secureSkillRelayTaskContext(t, userID, tokenID, tokenKey, channelID, server.URL)
	RelayTask(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)

	mu.Lock()
	require.Equal(t, 1, postCount)
	require.Equal(t, initialUserQuota-expectedPreConsumed, observedUserQuota)
	require.Equal(t, initialTokenQuota-expectedPreConsumed, observedTokenQuota)
	mu.Unlock()
	require.Equal(t, initialUserQuota-expectedPreConsumed, getSecureSkillUserQuota(t, db, userID), "successful async submission remains pre-consumed until polling terminal state")
	require.Equal(t, initialTokenQuota-expectedPreConsumed, getSecureSkillTokenQuota(t, db, tokenID))

	var task model.Task
	require.NoError(t, db.Where("user_id = ?", userID).First(&task).Error)
	require.Equal(t, "upstream-fake-key", task.PrivateData.Key, "H3 task must snapshot the selected upstream key for later polling/content")
	taskM := map[string]*model.Task{task.GetUpstreamTaskID(): &task}
	oldAdaptor := service.GetTaskAdaptorFunc
	service.GetTaskAdaptorFunc = func(platform constant.TaskPlatform) service.TaskPollingAdaptor {
		return relay.GetTaskAdaptor(platform).(service.TaskPollingAdaptor)
	}
	t.Cleanup(func() { service.GetTaskAdaptorFunc = oldAdaptor })

	taskChannelM := map[int][]string{channelID: {task.GetUpstreamTaskID()}}
	require.NoError(t, service.UpdateVideoTasks(context.Background(), constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeSecureSkill)), taskChannelM, taskM))
	var refundCount int64
	require.NoError(t, db.Model(&model.Log{}).Where("user_id = ? AND type = ?", userID, model.LogTypeRefund).Count(&refundCount).Error)
	require.Equal(t, int64(1), refundCount)
	require.Equal(t, initialUserQuota, getSecureSkillUserQuota(t, db, userID))
	require.Equal(t, initialTokenQuota, getSecureSkillTokenQuota(t, db, tokenID))

	// The terminal guard makes a duplicate poll read-free and billing-free.
	require.NoError(t, service.UpdateVideoTasks(context.Background(), constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeSecureSkill)), taskChannelM, taskM))
	require.NoError(t, db.Model(&model.Log{}).Where("user_id = ? AND type = ?", userID, model.LogTypeRefund).Count(&refundCount).Error)
	require.Equal(t, int64(1), refundCount)
	require.Equal(t, initialUserQuota, getSecureSkillUserQuota(t, db, userID))
	require.Equal(t, initialTokenQuota, getSecureSkillTokenQuota(t, db, tokenID))
	mu.Lock()
	require.Equal(t, 1, postCount)
	require.Equal(t, 1, statusGets)
	mu.Unlock()
	var persisted model.Task
	require.NoError(t, db.First(&persisted, task.ID).Error)
	require.Equal(t, model.TaskStatus(model.TaskStatusFailure), persisted.Status)
}

func TestSecureSkillContentProxyUsesSelectedTaskKeyAndLegacyFallbackOverHTTP(t *testing.T) {
	db := openSecureSkillControllerIntegrationDB(t)
	fetchSetting := system_setting.GetFetchSetting()
	oldFetchSetting := *fetchSetting
	fetchSetting.EnableSSRFProtection = false
	fetchSetting.AllowPrivateIp = true
	t.Cleanup(func() { *fetchSetting = oldFetchSetting })

	for _, tt := range []struct {
		name       string
		taskID     string
		upstreamID string
		userID     int
		channelID  int
		taskKey    string
		channelKey string
		wantKey    string
	}{
		{name: "new task selected key", taskID: "task_h3_content_new", upstreamID: "provider-new", userID: 700, channelID: 701, taskKey: "task-selected-key", channelKey: "channel-fallback-key", wantKey: "task-selected-key"},
		{name: "legacy task channel fallback", taskID: "task_h3_content_legacy", upstreamID: "provider-legacy", userID: 702, channelID: 703, channelKey: "channel-fallback-key", wantKey: "channel-fallback-key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			gotAuthorization := ""
			gotPath := ""
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
			channel := &model.Channel{
				Id: tt.channelID, Type: constant.ChannelTypeSecureSkill, Key: tt.channelKey,
				BaseURL: &baseURL, Status: common.ChannelStatusEnabled, Name: "SecureSkill content fake",
			}
			require.NoError(t, db.Create(channel).Error)
			task := &model.Task{
				TaskID: tt.taskID, UserId: tt.userID, ChannelId: tt.channelID,
				Platform: constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeSecureSkill)),
				Status:   model.TaskStatus(model.TaskStatusSuccess), Progress: "100%",
				Properties:  model.Properties{OriginModelName: secureskill.ModelName},
				PrivateData: model.TaskPrivateData{UpstreamTaskID: tt.upstreamID, Key: tt.taskKey},
			}
			require.NoError(t, db.Create(task).Error)

			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/"+tt.taskID+"/content", nil)
			ctx.Params = gin.Params{{Key: "task_id", Value: tt.taskID}}
			ctx.Set("id", tt.userID)
			VideoProxy(ctx)

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "fake-video-bytes", recorder.Body.String())
			mu.Lock()
			gotAuth, path := gotAuthorization, gotPath
			mu.Unlock()
			require.Equal(t, "Bearer "+tt.wantKey, gotAuth)
			require.Equal(t, "/v1/videos/"+tt.upstreamID+"/content", path)
			require.NotContains(t, recorder.Body.String(), tt.wantKey, "content response must not expose the authorization key")
		})
	}
}

func openSecureSkillControllerIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := fmt.Sprintf("file:secure_skill_controller_%d?mode=memory&cache=shared", time.Now().UnixNano())
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldUsingSQLite, oldUsingMySQL, oldUsingPostgreSQL := common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL
	oldMemoryCache, oldRedis, oldBatchUpdate := common.MemoryCacheEnabled, common.RedisEnabled, common.BatchUpdateEnabled
	oldLogConsume, oldErrorLog := common.LogConsumeEnabled, constant.ErrorLogEnabled
	oldMasterNode, oldSQLitePath := common.IsMasterNode, common.SQLitePath
	oldSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")
	t.Cleanup(func() {
		if hadSQLDSN {
			_ = os.Setenv("SQL_DSN", oldSQLDSN)
		} else {
			_ = os.Unsetenv("SQL_DSN")
		}
	})
	common.SQLitePath = dsn
	common.IsMasterNode = false
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	require.NoError(t, model.InitDB())
	db := model.DB
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Log{}, &model.Channel{}, &model.Task{}))
	service.InitHttpClient()
	model.DB, model.LOG_DB = db, db
	common.MemoryCacheEnabled, common.RedisEnabled, common.BatchUpdateEnabled = false, false, false
	common.LogConsumeEnabled = true
	constant.ErrorLogEnabled = false
	t.Cleanup(func() {
		waitForSecureSkillGlobalGoPool()
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = oldUsingSQLite, oldUsingMySQL, oldUsingPostgreSQL
		common.MemoryCacheEnabled, common.RedisEnabled, common.BatchUpdateEnabled = oldMemoryCache, oldRedis, oldBatchUpdate
		common.LogConsumeEnabled = oldLogConsume
		constant.ErrorLogEnabled = oldErrorLog
		common.IsMasterNode, common.SQLitePath = oldMasterNode, oldSQLitePath
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func waitForSecureSkillGlobalGoPool() {
	deadline := time.Now().Add(2 * time.Second)
	for gopool.WorkerCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

func seedSecureSkillControllerUser(t *testing.T, db *gorm.DB, userID, quota int) {
	t.Helper()
	require.NoError(t, db.Create(&model.User{
		Id: userID, Username: fmt.Sprintf("h3-user-%d", userID), Group: "default",
		Quota: quota, Status: common.UserStatusEnabled, AffCode: fmt.Sprintf("h3-aff-%d", userID),
	}).Error)
}

func seedSecureSkillControllerToken(t *testing.T, db *gorm.DB, tokenID, userID int, key string, quota int) {
	t.Helper()
	require.NoError(t, db.Create(&model.Token{
		Id: tokenID, UserId: userID, Key: key, Name: "h3-integration-token",
		Group: "default", Status: common.TokenStatusEnabled, ExpiredTime: -1,
		RemainQuota: quota,
	}).Error)
}

func secureSkillRelayTaskContext(t *testing.T, userID, tokenID int, tokenKey string, channelID int, baseURL string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"minimax-h3","prompt":"move","images":["https://example.test/input.png"],"duration":5}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(ctx, constant.ContextKeyUserId, userID)
	common.SetContextKey(ctx, constant.ContextKeyUserQuota, 10000)
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenId, tokenID)
	common.SetContextKey(ctx, constant.ContextKeyTokenKey, tokenKey)
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenUnlimited, false)
	common.SetContextKey(ctx, constant.ContextKeyOriginalModel, secureskill.ModelName)
	common.SetContextKey(ctx, constant.ContextKeyChannelId, channelID)
	common.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeSecureSkill)
	common.SetContextKey(ctx, constant.ContextKeyChannelName, "SecureSkill H3 integration fake")
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "upstream-fake-key")
	common.SetContextKey(ctx, constant.ContextKeyChannelBaseUrl, baseURL)
	common.SetContextKey(ctx, constant.ContextKeyChannelAutoBan, false)
	common.SetContextKey(ctx, constant.ContextKeyRequestStartTime, time.Now())
	common.SetContextKey(ctx, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
	ctx.Set("auto_ban", false)
	ctx.Set("token_name", "h3-integration-token")
	ctx.Set("token_quota", 0)
	ctx.Set("platform", strconv.Itoa(constant.ChannelTypeSecureSkill))
	ctx.Set("relay_mode", relayconstant.RelayModeVideoSubmit)
	ctx.Set("use_channel", []string{})
	return ctx, recorder
}

func waitForSecureSkillQuotaRestore(t *testing.T, db *gorm.DB, userID, tokenID, wantUser, wantToken int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if getSecureSkillUserQuota(t, db, userID) == wantUser && getSecureSkillTokenQuota(t, db, tokenID) == wantToken {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, wantUser, getSecureSkillUserQuota(t, db, userID))
	require.Equal(t, wantToken, getSecureSkillTokenQuota(t, db, tokenID))
}

func getSecureSkillUserQuota(t *testing.T, db *gorm.DB, userID int) int {
	t.Helper()
	var user model.User
	require.NoError(t, db.Select("quota").Where("id = ?", userID).First(&user).Error)
	return user.Quota
}

func getSecureSkillTokenQuota(t *testing.T, db *gorm.DB, tokenID int) int {
	t.Helper()
	var token model.Token
	require.NoError(t, db.Select("remain_quota").Where("id = ?", tokenID).First(&token).Error)
	return token.RemainQuota
}

func countSecureSkillTasks(t *testing.T, db *gorm.DB, userID int) int {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&model.Task{}).Where("user_id = ?", userID).Count(&count).Error)
	return int(count)
}
