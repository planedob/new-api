package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRootAuthRestrictsImage2SmartRoutingToggleToRoot(t *testing.T) {
	originalDB := model.DB
	originalUsingSQLite := common.UsingSQLite
	originalUsingPostgreSQL := common.UsingPostgreSQL
	originalUsingMySQL := common.UsingMySQL
	originalRedis := common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	model.DB = db
	common.UsingSQLite, common.UsingPostgreSQL, common.UsingMySQL = true, false, false
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = originalDB
		common.UsingSQLite = originalUsingSQLite
		common.UsingPostgreSQL = originalUsingPostgreSQL
		common.UsingMySQL = originalUsingMySQL
		common.RedisEnabled = originalRedis
	})

	rootToken := "root-image2-toggle-pat"
	adminToken := "admin-image2-toggle-pat"
	require.NoError(t, db.Create(&model.User{
		Username: "image2-root", Password: "test-password", Role: common.RoleRootUser,
		Status: common.UserStatusEnabled, AccessToken: &rootToken, AffCode: "image2-root-aff",
	}).Error)
	require.NoError(t, db.Create(&model.User{
		Username: "image2-admin", Password: "test-password", Role: common.RoleAdminUser,
		Status: common.UserStatusEnabled, AccessToken: &adminToken, AffCode: "image2-admin-aff",
	}).Error)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("image2-root-auth-test", cookie.NewStore([]byte("image2-root-auth-test-secret"))))
	router.PUT("/api/option/", RootAuth(), func(c *gin.Context) {
		c.Header("X-Image2-Test-Handler", "reached")
		c.Status(http.StatusNoContent)
	})

	tests := []struct {
		name        string
		token       string
		wantStatus  int
		wantHandler bool
	}{
		{name: "root allowed", token: rootToken, wantStatus: http.StatusNoContent, wantHandler: true},
		// This production baseline reports authorization failures as a 200 JSON
		// envelope; the security invariant is that the protected handler is not run.
		{name: "admin rejected", token: adminToken, wantStatus: http.StatusOK, wantHandler: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, "/api/option/", nil)
			request.Header.Set("Authorization", "Bearer "+test.token)
			if test.token == rootToken {
				request.Header.Set("New-Api-User", "1")
			} else {
				request.Header.Set("New-Api-User", "2")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assert.Equal(t, test.wantStatus, response.Code)
			assert.Equal(t, test.wantHandler, response.Header().Get("X-Image2-Test-Handler") == "reached")
		})
	}
}
