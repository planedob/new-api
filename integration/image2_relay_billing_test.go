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
