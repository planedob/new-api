package integration_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestImage2RelayToFakeUpstreamSettlesBillingSessionOnce(t *testing.T) {
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldBatchUpdateEnabled := common.BatchUpdateEnabled
	oldLogConsumeEnabled := common.LogConsumeEnabled
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.BatchUpdateEnabled = oldBatchUpdateEnabled
		common.LogConsumeEnabled = oldLogConsumeEnabled
	})

	db, err := gorm.Open(sqlite.Open("file:image2_relay_billing?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}))
	require.NoError(t, db.Create(&model.User{Id: 41, Username: "image2-test", Quota: 1000}).Error)
	model.DB, model.LOG_DB = db, db
	// Keep Redis disabled for this process: the real relay path schedules
	// asynchronous perf/notification work, and restoring the global while that
	// work is still draining would make the race test itself racy.
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = false
	common.QuotaRemindThreshold = 0
	service.InitHttpClient()

	requestID := "image2-relay-billing-1"
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/v1/images/generations", r.URL.Path)
		require.Equal(t, requestID, r.Header.Get("X-Request-ID"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"ZmFrZS1yZWxheQ=="}]}`)
	}))
	defer server.Close()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	c.Request.Header.Set("X-Request-ID", requestID)
	c.Set(common.RequestIdKey, requestID)
	c.Set(string(constant.ContextKeyChannelType), constant.ChannelTypeOpenAI)
	c.Set(string(constant.ContextKeyChannelBaseUrl), server.URL)
	c.Set(string(constant.ContextKeyChannelKey), "fake-upstream-key")
	c.Set(string(constant.ContextKeyChannelId), 91)
	c.Set(string(constant.ContextKeyChannelName), "image2-loopback-fake")
	c.Set(string(constant.ContextKeyChannelSetting), dto.ChannelSettings{})
	c.Set(string(constant.ContextKeyChannelOtherSetting), dto.ChannelOtherSettings{})
	c.Set(string(constant.ContextKeyChannelParamOverride), map[string]interface{}{})
	c.Set(string(constant.ContextKeyChannelHeaderOverride), map[string]interface{}{"X-Request-ID": requestID})
	c.Set(string(constant.ContextKeyChannelIsMultiKey), false)
	c.Set(string(constant.ContextKeyOriginalModel), "gpt-image-2")
	c.Set(string(constant.ContextKeyUsingGroup), "default")
	c.Set(string(constant.ContextKeyUserGroup), "default")
	c.Set(string(constant.ContextKeyRequestStartTime), time.Now())

	info := &relaycommon.RelayInfo{
		Request:         &dto.ImageRequest{Model: "gpt-image-2", Prompt: "fake relay image", Size: "1024x1024"},
		RequestId:       requestID,
		RequestURLPath:  "/v1/images/generations",
		UserId:          41,
		IsPlayground:    true,
		OriginModelName: "gpt-image-2",
		RelayMode:       relayconstant.RelayModeImagesGenerations,
		RelayFormat:     types.RelayFormatOpenAIImage,
		UserQuota:       1000,
		UserSetting:     dto.UserSetting{BillingPreference: "wallet_only"},
		PriceData: types.PriceData{
			ModelRatio: 1,
			UsePrice:   false,
			GroupRatioInfo: types.GroupRatioInfo{
				GroupRatio: 1,
			},
		},
	}

	billing, apiErr := service.NewBillingSession(c, info, 100)
	require.Nil(t, apiErr)
	require.NotNil(t, billing)
	info.Billing = billing

	require.Nil(t, relay.ImageHelper(c, info))
	require.Equal(t, 1, calls)
	require.Contains(t, recorder.Body.String(), "ZmFrZS1yZWxheQ==")

	var user model.User
	require.NoError(t, db.First(&user, 41).Error)
	// The real ImageHelper path computes a one-unit image usage, so the
	// BillingSession must settle the 100-unit precharge down to one unit.
	require.Equal(t, 999, user.Quota)
	require.Equal(t, 100, info.FinalPreConsumedQuota)
}

func TestImage2ControllerRelayToFakeUpstreamSettlesBillingSessionOnce(t *testing.T) {
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldBatchUpdateEnabled := common.BatchUpdateEnabled
	oldLogConsumeEnabled := common.LogConsumeEnabled
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.BatchUpdateEnabled = oldBatchUpdateEnabled
		common.LogConsumeEnabled = oldLogConsumeEnabled
	})

	db, err := gorm.Open(sqlite.Open("file:image2_controller_relay_billing?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}))
	require.NoError(t, db.Create(&model.User{Id: 42, Username: "image2-controller-test", Quota: 1000000}).Error)
	model.DB, model.LOG_DB = db, db
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = false
	common.QuotaRemindThreshold = 0
	service.InitHttpClient()

	requestID := "image2-controller-relay-billing-1"
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/v1/images/generations", r.URL.Path)
		require.Equal(t, requestID, r.Header.Get("X-Request-ID"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"ZmFrZS1jb250cm9sbGVy"}]}`)
	}))
	defer server.Close()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/pg/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"fake controller image","size":"1024x1024"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Request-ID", requestID)
	c.Set(common.RequestIdKey, requestID)
	c.Set(string(constant.ContextKeyUserId), 42)
	c.Set(string(constant.ContextKeyUserQuota), 1000000)
	c.Set(string(constant.ContextKeyUserGroup), "default")
	c.Set(string(constant.ContextKeyUsingGroup), "default")
	c.Set(string(constant.ContextKeyUserSetting), dto.UserSetting{BillingPreference: "wallet_only", AcceptUnsetRatioModel: true})
	c.Set(string(constant.ContextKeyOriginalModel), "gpt-image-2")
	c.Set(string(constant.ContextKeyChannelType), constant.ChannelTypeOpenAI)
	c.Set(string(constant.ContextKeyChannelBaseUrl), server.URL)
	c.Set(string(constant.ContextKeyChannelKey), "fake-upstream-key")
	c.Set(string(constant.ContextKeyChannelId), 92)
	c.Set(string(constant.ContextKeyChannelName), "image2-controller-loopback-fake")
	c.Set(string(constant.ContextKeyChannelSetting), dto.ChannelSettings{})
	c.Set(string(constant.ContextKeyChannelOtherSetting), dto.ChannelOtherSettings{})
	c.Set(string(constant.ContextKeyChannelParamOverride), map[string]interface{}{})
	c.Set(string(constant.ContextKeyChannelHeaderOverride), map[string]interface{}{"X-Request-ID": requestID})
	c.Set(string(constant.ContextKeyChannelIsMultiKey), false)
	c.Set(string(constant.ContextKeyChannelAutoBan), false)
	c.Set(string(constant.ContextKeyChannelStatusCodeMapping), "")
	c.Set(string(constant.ContextKeyTokenUnlimited), true)
	c.Set(string(constant.ContextKeyTokenKey), "playground-token")
	c.Set(string(constant.ContextKeyTokenGroup), "default")
	c.Set(string(constant.ContextKeyRequestStartTime), time.Now())

	controller.Relay(c, types.RelayFormatOpenAIImage)
	require.Equal(t, 1, calls)
	require.Contains(t, recorder.Body.String(), "ZmFrZS1jb250cm9sbGVy")

	var user model.User
	require.NoError(t, db.First(&user, 42).Error)
	// Playground Relay creates the real BillingSession and the real image
	// handler settles its precharge using the usage returned by the adaptor.
	require.Equal(t, user.Quota+user.UsedQuota, 1000000)
	require.Equal(t, 1, user.RequestCount)
}

func TestImage2ControllerRelayDoesNotReplayAcceptedUpstreamResponse(t *testing.T) {
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldBatchUpdateEnabled := common.BatchUpdateEnabled
	oldLogConsumeEnabled := common.LogConsumeEnabled
	oldSafeFailover := common.SafeFailoverV1Enabled
	oldMaxAttempts := common.SafeFailoverMaxAttempts
	oldImageGuard := common.SafeFailoverImageGuardSeconds
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.BatchUpdateEnabled = oldBatchUpdateEnabled
		common.LogConsumeEnabled = oldLogConsumeEnabled
		common.SafeFailoverV1Enabled = oldSafeFailover
		common.SafeFailoverMaxAttempts = oldMaxAttempts
		common.SafeFailoverImageGuardSeconds = oldImageGuard
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
	})

	db, err := gorm.Open(sqlite.Open("file:image2_controller_replay_guard?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}, &model.Ability{}))
	require.NoError(t, db.Create(&model.User{Id: 43, Username: "image2-replay-test", Quota: 1000000}).Error)
	model.DB, model.LOG_DB = db, db
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = false
	common.QuotaRemindThreshold = 0
	common.SafeFailoverV1Enabled = true
	common.SafeFailoverMaxAttempts = 2
	common.SafeFailoverImageGuardSeconds = 60
	common.MemoryCacheEnabled = false
	service.InitHttpClient()

	requestID := "image2-controller-replay-guard-1"
	requestID2 := requestID + "-written"
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/v1/images/generations", r.URL.Path)
		require.Contains(t, []string{requestID, requestID2}, r.Header.Get("X-Request-ID"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"job_id=fake-job-1 queued"}}`)
	}))
	defer server.Close()
	var secondCalls int
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"c2Vjb25kLWNoYW5uZWw"}]}`)
	}))
	defer secondServer.Close()
	secondBaseURL := secondServer.URL
	secondPriority := int64(0)
	secondAutoBan := 0
	require.NoError(t, db.Create(&model.Channel{
		Id:       94,
		Type:     constant.ChannelTypeOpenAI,
		Key:      "fake-second-channel-key",
		Status:   common.ChannelStatusEnabled,
		Name:     "image2-second-loopback-fake",
		BaseURL:  &secondBaseURL,
		Group:    "default",
		Models:   "gpt-image-2",
		Priority: &secondPriority,
		AutoBan:  &secondAutoBan,
	}).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group:     "default",
		Model:     "gpt-image-2",
		ChannelId: 94,
		Enabled:   true,
		Priority:  &secondPriority,
	}).Error)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/pg/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"accepted fake controller image","size":"1024x1024"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Request-ID", requestID)
	c.Set(common.RequestIdKey, requestID)
	c.Set(string(constant.ContextKeyUserId), 43)
	c.Set(string(constant.ContextKeyUserQuota), 1000000)
	c.Set(string(constant.ContextKeyUserGroup), "default")
	c.Set(string(constant.ContextKeyUsingGroup), "default")
	c.Set(string(constant.ContextKeyUserSetting), dto.UserSetting{BillingPreference: "wallet_only", AcceptUnsetRatioModel: true})
	c.Set(string(constant.ContextKeyOriginalModel), "gpt-image-2")
	c.Set(string(constant.ContextKeyChannelType), constant.ChannelTypeOpenAI)
	c.Set(string(constant.ContextKeyChannelBaseUrl), server.URL)
	c.Set(string(constant.ContextKeyChannelKey), "fake-upstream-key")
	c.Set(string(constant.ContextKeyChannelId), 93)
	c.Set(string(constant.ContextKeyChannelName), "image2-controller-replay-fake")
	c.Set(string(constant.ContextKeyChannelSetting), dto.ChannelSettings{})
	c.Set(string(constant.ContextKeyChannelOtherSetting), dto.ChannelOtherSettings{})
	c.Set(string(constant.ContextKeyChannelParamOverride), map[string]interface{}{})
	c.Set(string(constant.ContextKeyChannelHeaderOverride), map[string]interface{}{"X-Request-ID": requestID})
	c.Set(string(constant.ContextKeyChannelIsMultiKey), false)
	c.Set(string(constant.ContextKeyChannelAutoBan), false)
	c.Set(string(constant.ContextKeyChannelStatusCodeMapping), "")
	c.Set(string(constant.ContextKeyTokenUnlimited), true)
	c.Set(string(constant.ContextKeyTokenKey), "playground-token")
	c.Set(string(constant.ContextKeyTokenGroup), "default")
	c.Set(string(constant.ContextKeyRequestStartTime), time.Now())

	controller.Relay(c, types.RelayFormatOpenAIImage)
	require.Equal(t, 1, calls, "queued upstream acceptance must not enter a second controller attempt")
	require.Equal(t, 0, secondCalls, "a second eligible channel must remain unused after upstream acceptance")
	require.Eventually(t, func() bool {
		var user model.User
		if db.First(&user, 43).Error != nil {
			return false
		}
		return user.Quota == 1000000
	}, time.Second, 10*time.Millisecond, "failed accepted request must refund its precharge once")

	secondRecorder := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondRecorder)
	secondContext.Request = httptest.NewRequest(http.MethodPost, "/pg/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"written fake controller image","size":"1024x1024"}`))
	secondContext.Request.Header.Set("Content-Type", "application/json")
	secondContext.Request.Header.Set("X-Request-ID", requestID2)
	secondContext.Set(common.RequestIdKey, requestID2)
	secondContext.Set(string(constant.ContextKeyUserId), 43)
	secondContext.Set(string(constant.ContextKeyUserQuota), 1000000)
	secondContext.Set(string(constant.ContextKeyUserGroup), "default")
	secondContext.Set(string(constant.ContextKeyUsingGroup), "default")
	secondContext.Set(string(constant.ContextKeyUserSetting), dto.UserSetting{BillingPreference: "wallet_only", AcceptUnsetRatioModel: true})
	secondContext.Set(string(constant.ContextKeyOriginalModel), "gpt-image-2")
	secondContext.Set(string(constant.ContextKeyChannelType), constant.ChannelTypeOpenAI)
	secondContext.Set(string(constant.ContextKeyChannelBaseUrl), server.URL)
	secondContext.Set(string(constant.ContextKeyChannelKey), "fake-upstream-key")
	secondContext.Set(string(constant.ContextKeyChannelId), 93)
	secondContext.Set(string(constant.ContextKeyChannelName), "image2-controller-replay-fake")
	secondContext.Set(string(constant.ContextKeyChannelSetting), dto.ChannelSettings{})
	secondContext.Set(string(constant.ContextKeyChannelOtherSetting), dto.ChannelOtherSettings{})
	secondContext.Set(string(constant.ContextKeyChannelParamOverride), map[string]interface{}{})
	secondContext.Set(string(constant.ContextKeyChannelHeaderOverride), map[string]interface{}{"X-Request-ID": requestID2})
	secondContext.Set(string(constant.ContextKeyChannelIsMultiKey), false)
	secondContext.Set(string(constant.ContextKeyChannelAutoBan), false)
	secondContext.Set(string(constant.ContextKeyChannelStatusCodeMapping), "")
	secondContext.Set(string(constant.ContextKeyTokenUnlimited), true)
	secondContext.Set(string(constant.ContextKeyTokenKey), "playground-token")
	secondContext.Set(string(constant.ContextKeyTokenGroup), "default")
	secondContext.Set(string(constant.ContextKeyRequestStartTime), time.Now())
	_, _ = secondRecorder.WriteString("already-written")

	controller.Relay(secondContext, types.RelayFormatOpenAIImage)
	require.Equal(t, 2, calls, "written response case should make exactly one new upstream attempt")
	require.Equal(t, 0, secondCalls, "written response must not fail over to the second channel")
	require.Eventually(t, func() bool {
		var user model.User
		if db.First(&user, 43).Error != nil {
			return false
		}
		return user.Quota == 1000000
	}, time.Second, 10*time.Millisecond, "written response failure must refund its precharge once")
}
