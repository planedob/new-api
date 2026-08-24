package controller

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImage2ControllerNoCandidateFailsBeforeUpstreamAndBilling(t *testing.T) {
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldMemoryCache := common.MemoryCacheEnabled
	oldRedis := common.RedisEnabled
	oldBatchUpdate := common.BatchUpdateEnabled
	oldLogConsume := common.LogConsumeEnabled
	oldQuotaReminder := common.QuotaRemindThreshold
	oldUsingSQLite, oldUsingPostgreSQL := common.UsingSQLite, common.UsingPostgreSQL
	oldUsingMySQL, oldLogSQLType := common.UsingMySQL, common.LogSqlType
	oldSQLitePath := common.SQLitePath
	oldMasterNode := common.IsMasterNode
	oldSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")
	oldSmartRouting, oldRouteMode := common.Image2SmartRoutingEnabled, common.Image2RouteMode
	oldPassive, oldErrorLog := constant.Image2PassiveMonitorEnabled, constant.ErrorLogEnabled
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.MemoryCacheEnabled, common.RedisEnabled = oldMemoryCache, oldRedis
		common.BatchUpdateEnabled, common.LogConsumeEnabled = oldBatchUpdate, oldLogConsume
		common.QuotaRemindThreshold = oldQuotaReminder
		common.UsingSQLite, common.UsingPostgreSQL = oldUsingSQLite, oldUsingPostgreSQL
		common.UsingMySQL, common.LogSqlType = oldUsingMySQL, oldLogSQLType
		common.SQLitePath = oldSQLitePath
		common.IsMasterNode = oldMasterNode
		if hadSQLDSN {
			_ = os.Setenv("SQL_DSN", oldSQLDSN)
		} else {
			_ = os.Unsetenv("SQL_DSN")
		}
		common.Image2SmartRoutingEnabled, common.Image2RouteMode = oldSmartRouting, oldRouteMode
		constant.Image2PassiveMonitorEnabled, constant.ErrorLogEnabled = oldPassive, oldErrorLog
	})

	common.SQLitePath = "file:image2_controller_no_candidate_aadd?mode=memory&cache=shared"
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	common.UsingSQLite, common.UsingPostgreSQL, common.UsingMySQL = false, false, false
	common.LogSqlType = common.DatabaseTypeSQLite
	common.IsMasterNode = true
	require.NoError(t, model.InitDB())
	db := model.DB
	require.NotNil(t, db)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}, &model.Ability{}, &model.Log{}))
	require.NoError(t, db.Create(&model.User{Id: 4401, Username: "image2-no-candidate-test", Quota: 1000000}).Error)

	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)
	baseURL := upstream.URL
	priority := int64(10)
	autoBan := 0
	channel := &model.Channel{
		Id: 4402, Type: constant.ChannelTypeOpenAI, Key: "isolated-no-candidate-key",
		Status: common.ChannelStatusEnabled, Name: "image2-incompatible-loopback",
		BaseURL: &baseURL, Group: "default", Models: "gpt-image-2",
		Priority: &priority, AutoBan: &autoBan,
	}
	capability := &dto.Image2ChannelCapability{
		Enabled: true, Operations: []string{"generations"}, Resolutions: []string{"uhd"}, MaxN: 1, RoutePriority: 10,
	}
	capabilityDigest, err := dto.Image2CapabilitySHA256(capability)
	require.NoError(t, err)
	verificationNow := time.Now().UTC().Truncate(time.Second)
	testedAt := verificationNow.Add(-time.Hour)
	evidence := dto.Image2FixedChannelTestEvidence{
		ChannelID: channel.Id, Operation: "generations", Endpoint: "/v1/images/generations",
		TestedAt: testedAt.Format(time.RFC3339), Status: "passed", StatusCode: 200, RequestCount: 1,
		RequestSHA256:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ResponseSHA256:   "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		CapabilitySHA256: capabilityDigest,
	}
	evidenceDigest, err := dto.Image2FixedChannelTestEvidenceSHA256(evidence)
	require.NoError(t, err)
	channel.SetSetting(dto.ChannelSettings{
		Image2Capability: capability,
		Image2CapabilityVerification: &dto.Image2CapabilityVerification{
			Status:           "passed",
			Source:           "fixed_channel_test",
			VerifiedAt:       testedAt.Format(time.RFC3339),
			ValidUntil:       verificationNow.Add(time.Hour).Format(time.RFC3339),
			CapabilitySHA256: capabilityDigest,
			Evidence:         []dto.Image2FixedChannelTestEvidence{evidence},
			EvidenceSHA256:   []string{evidenceDigest},
		},
	})
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group: "default", Model: "gpt-image-2", ChannelId: channel.Id, Enabled: true, Priority: &priority,
	}).Error)
	model.DB, model.LOG_DB = db, db
	common.MemoryCacheEnabled = false
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = false
	common.QuotaRemindThreshold = 0
	common.Image2SmartRoutingEnabled = true
	common.Image2RouteMode = common.Image2RouteModeAdvanced
	constant.Image2PassiveMonitorEnabled = true
	constant.ErrorLogEnabled = true
	service.InitHttpClient()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestID := "image2-controller-no-candidate-aadd"
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"no compatible candidate","size":"1024x1024"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Request-ID", requestID)
	c.Set(common.RequestIdKey, requestID)
	c.Set(string(constant.ContextKeyUserId), 4401)
	c.Set(string(constant.ContextKeyUserQuota), 1000000)
	c.Set(string(constant.ContextKeyUserGroup), "default")
	c.Set(string(constant.ContextKeyUsingGroup), "default")
	c.Set(string(constant.ContextKeyUserSetting), dto.UserSetting{BillingPreference: "wallet_only", AcceptUnsetRatioModel: true})
	c.Set(string(constant.ContextKeyOriginalModel), "gpt-image-2")
	c.Set(string(constant.ContextKeyChannelType), constant.ChannelTypeOpenAI)
	c.Set(string(constant.ContextKeyChannelBaseUrl), baseURL)
	c.Set(string(constant.ContextKeyChannelKey), "isolated-no-candidate-key")
	c.Set(string(constant.ContextKeyChannelId), channel.Id)
	c.Set(string(constant.ContextKeyChannelName), channel.Name)
	c.Set(string(constant.ContextKeyChannelSetting), channel.GetSetting())
	c.Set(string(constant.ContextKeyChannelOtherSetting), dto.ChannelOtherSettings{})
	c.Set(string(constant.ContextKeyChannelParamOverride), map[string]interface{}{})
	c.Set(string(constant.ContextKeyChannelHeaderOverride), map[string]interface{}{})
	c.Set(string(constant.ContextKeyChannelIsMultiKey), false)
	c.Set(string(constant.ContextKeyChannelAutoBan), false)
	c.Set(string(constant.ContextKeyChannelStatusCodeMapping), "")
	c.Set(string(constant.ContextKeyTokenUnlimited), true)
	c.Set(string(constant.ContextKeyTokenKey), "isolated-no-candidate-token")
	c.Set(string(constant.ContextKeyTokenGroup), "default")
	c.Set(string(constant.ContextKeyRequestStartTime), time.Now())

	Relay(c, types.RelayFormatOpenAIImage)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, recorder.Body.String(), "no compatible Image2 channel")
	require.Empty(t, c.GetStringSlice("use_channel"))
	require.Zero(t, upstreamCalls, "no-candidate rejection must not call an upstream")

	var user model.User
	require.NoError(t, db.First(&user, 4401).Error)
	require.Equal(t, 1000000, user.Quota)
	require.Equal(t, 0, user.UsedQuota)
	require.Equal(t, 0, user.RequestCount)
	var consumeCount int64
	require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&consumeCount).Error)
	require.Zero(t, consumeCount, "no-candidate rejection must not create a consume log")
	var errorCount int64
	require.NoError(t, db.Model(&model.Log{}).Where("type = ? and request_id = ?", model.LogTypeError, requestID).Count(&errorCount).Error)
	require.Equal(t, int64(1), errorCount, "AADD keeps one safe no-candidate event when error logging is enabled")
}
