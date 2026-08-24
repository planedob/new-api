package middleware

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func image2EditsChainTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousSQLite := common.UsingSQLite
	dsn := fmt.Sprintf("file:image2-edits-chain-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB = db
	common.UsingSQLite = true

	t.Cleanup(func() {
		model.DB = previousDB
		common.UsingSQLite = previousSQLite
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func image2EditsMultipartRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "turn the image purple"))
	require.NoError(t, writer.WriteField("size", "1024x1024"))
	part, err := writer.CreateFormFile("image", "reference.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

// This is the smallest real-engine reproduction of the customer path that
// crossed the broken boundary: Distribute reads multipart model metadata and
// the Relay image validator consumes the same request next. It intentionally
// uses an enabled fixed channel so channel selection cannot hide a body error.
func TestImage2EditsDistributeThenRelayReusesMultipartBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := image2EditsChainTestDB(t)
	channel := &model.Channel{
		Id:     9101,
		Type:   constant.ChannelTypeOpenAI,
		Key:    "isolated-test-key",
		Status: common.ChannelStatusEnabled,
		Name:   "image2-edits-chain-fixture",
		Models: "gpt-image-2",
		Group:  "default",
	}
	require.NoError(t, db.Create(channel).Error)

	router := gin.New()
	router.Use(DecompressRequestMiddleware())
	router.Use(BodyStorageCleanup())
	router.POST(
		"/v1/images/edits",
		func(c *gin.Context) {
			common.SetContextKey(c, constant.ContextKeyTokenSpecificChannelId, "9101")
			common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
			c.Next()
		},
		ModelRequestRateLimit(),
		Distribute(),
		func(c *gin.Context) {
			request, err := relayhelper.GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesEdits)
			if err != nil {
				c.String(http.StatusInternalServerError, err.Error())
				return
			}
			c.JSON(http.StatusOK, gin.H{"model": request.Model, "prompt": request.Prompt})
		},
	)

	recorder := httptest.NewRecorder()
	request := image2EditsMultipartRequest(t, "/v1/images/edits")
	router.ServeHTTP(recorder, request)
	if request.MultipartForm != nil {
		t.Cleanup(func() { _ = request.MultipartForm.RemoveAll() })
	}

	require.Equal(t, http.StatusOK, recorder.Code, "full chain response: %s", recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `"model":"gpt-image-2"`)
	require.Contains(t, recorder.Body.String(), `"prompt":"turn the image purple"`)
}
