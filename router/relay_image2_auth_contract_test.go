package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var image2RouteContractDBCounter uint64

func setupImage2RouteContractDB(t *testing.T) (*gorm.DB, *model.Token) {
	t.Helper()
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousSQLite := common.UsingSQLite
	previousMySQL := common.UsingMySQL
	previousPostgreSQL := common.UsingPostgreSQL
	previousRedis := common.RedisEnabled
	previousSQLitePath := common.SQLitePath
	previousIsMasterNode := common.IsMasterNode
	previousSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")

	dsn := fmt.Sprintf(
		"file:image2-route-contract-%d?mode=memory&cache=shared",
		atomic.AddUint64(&image2RouteContractDBCounter, 1),
	)
	common.SQLitePath = dsn
	common.IsMasterNode = false
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	require.NoError(t, model.InitDB())
	db := model.DB
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))

	user := &model.User{
		Id:       1,
		Username: "image2-route-contract-user",
		AffCode:  "image2-route-contract-aff",
		Group:    "default",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
	}
	require.NoError(t, db.Create(user).Error)
	token := &model.Token{
		Id:     1,
		UserId: user.Id,
		// TokenAuth strips the public "sk-" prefix and treats a hyphen as a
		// channel selector delimiter. Keep the fixture key delimiter-free, as
		// production API keys are.
		Key:            "image2routecontracttoken",
		Status:         common.TokenStatusEnabled,
		Group:          "default",
		UnlimitedQuota: true,
		ExpiredTime:    -1,
	}
	require.NoError(t, db.Create(token).Error)

	model.DB = db
	model.LOG_DB = db

	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.UsingSQLite = previousSQLite
		common.UsingMySQL = previousMySQL
		common.UsingPostgreSQL = previousPostgreSQL
		common.RedisEnabled = previousRedis
		common.SQLitePath = previousSQLitePath
		common.IsMasterNode = previousIsMasterNode
		if hadSQLDSN {
			_ = os.Setenv("SQL_DSN", previousSQLDSN)
		} else {
			_ = os.Unsetenv("SQL_DSN")
		}
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	return db, token
}

func newImage2PlaygroundContractRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("image2-route-contract-secret"))))
	SetRelayRouter(router)
	return router
}

func image2RouteContractRequest(t *testing.T, router *gin.Engine, path string, key string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		path,
		bytes.NewBufferString(`{"model":"gpt-image-2","prompt":"route-contract"}`),
	)
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestImage2PlaygroundRejectsAPIKeyBeforeRelay(t *testing.T) {
	_, token := setupImage2RouteContractDB(t)
	router := newImage2PlaygroundContractRouter()

	// The production probe used a valid API token from the tokens table on /pg.
	// /pg is a dashboard playground route and is protected by UserAuth, which
	// validates a user access token (not a tokens table key). The legacy auth
	// envelope is HTTP 200 with success=false, so status alone must not be
	// treated as success.
	for _, path := range []string{"/pg/images/generations", "/pg/images/edits"} {
		t.Run(path, func(t *testing.T) {
			recorder := image2RouteContractRequest(t, router, path, "sk-"+token.Key)
			require.Equal(t, http.StatusOK, recorder.Code)
			var body struct {
				Success *bool  `json:"success"`
				Message string `json:"message"`
				Data    any    `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body), recorder.Body.String())
			require.NotNil(t, body.Success, recorder.Body.String())
			require.False(t, *body.Success, recorder.Body.String())
			require.NotEmpty(t, body.Message, recorder.Body.String())
			require.Nil(t, body.Data, recorder.Body.String())
		})
	}
}

func TestImage2V1APIKeyPassesTokenAuthContract(t *testing.T) {
	_, token := setupImage2RouteContractDB(t)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	relayReached := false
	for _, path := range []string{"/v1/images/generations", "/v1/images/edits"} {
		router.POST(path, middleware.TokenAuth(), func(c *gin.Context) {
			relayReached = true
			c.JSON(http.StatusOK, gin.H{"relay_reached": true})
		})

		recorder := image2RouteContractRequest(t, router, path, "sk-"+token.Key)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		require.True(t, relayReached, recorder.Body.String())
		require.JSONEq(t, `{"relay_reached":true}`, recorder.Body.String())
		relayReached = false
	}
}
