package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUpdateOptionImage2SmartRoutingRecordsToggleAudit(t *testing.T) {
	originalDB, originalLogDB := model.DB, model.LOG_DB
	originalEnabled := common.GetImage2SmartRoutingEnabled()
	originalRedis := common.RedisEnabled
	originalOptions := common.OptionMap
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Option{}, &model.Log{}))
	operator := &model.User{Id: 9101, Username: "image2-root", Password: "test-password", Role: common.RoleRootUser, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(operator).Error)
	model.DB, model.LOG_DB = db, db
	common.RedisEnabled = false
	common.OptionMap = map[string]string{}
	common.SetImage2SmartRoutingEnabled(false)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = originalDB, originalLogDB
		common.SetImage2SmartRoutingEnabled(originalEnabled)
		common.RedisEnabled = originalRedis
		common.OptionMap = originalOptions
	})

	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(`{"key":"image2.smart_routing.enabled","value":true}`))
	c.Set("id", operator.Id)
	c.Set("username", operator.Username)
	c.Set("role", operator.Role)

	UpdateOption(c)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.True(t, common.GetImage2SmartRoutingEnabled())
	var logs []model.Log
	require.NoError(t, db.Where("type = ?", model.LogTypeManage).Find(&logs).Error)
	require.Len(t, logs, 1)
	assert.Contains(t, logs[0].Other, `"action":"image2.smart_routing.toggle"`)
	assert.Contains(t, logs[0].Other, `"before":"false"`)
	assert.Contains(t, logs[0].Other, `"after":"true"`)
}
