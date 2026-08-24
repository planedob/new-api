package middleware

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	projecti18n "github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/andybalholm/brotli"
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

func image2EditsMultipartRequestWithFiles(t *testing.T, path, fieldName string, images [][]byte, mask []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "turn the image purple"))
	require.NoError(t, writer.WriteField("size", "1024x1024"))
	for index, image := range images {
		part, err := writer.CreateFormFile(fieldName, fmt.Sprintf("reference-%d.bin", index))
		require.NoError(t, err)
		_, err = part.Write(image)
		require.NoError(t, err)
	}
	if mask != nil {
		part, err := writer.CreateFormFile("mask", "mask.png")
		require.NoError(t, err)
		_, err = part.Write(mask)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func image2EditsMultipartRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	return image2EditsMultipartRequestWithFiles(t, path, "image", [][]byte{{
		0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, '\r', 'I', 'H', 'D', 'R',
	}}, nil)
}

func serveImage2EditsChain(t *testing.T, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
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
		request.URL.Path,
		func(c *gin.Context) {
			common.SetContextKey(c, constant.ContextKeyTokenSpecificChannelId, "9101")
			common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
			c.Next()
		},
		ModelRequestRateLimit(),
		Distribute(),
		func(c *gin.Context) {
			parsed, err := relayhelper.GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesEdits)
			if err != nil {
				status := http.StatusBadRequest
				if common.IsRequestBodyTooLargeError(err) {
					status = http.StatusRequestEntityTooLarge
				}
				c.String(status, err.Error())
				return
			}
			c.JSON(http.StatusOK, gin.H{"model": parsed.Model, "prompt": parsed.Prompt})
		},
	)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if request.MultipartForm != nil {
		t.Cleanup(func() { _ = request.MultipartForm.RemoveAll() })
	}
	return recorder
}

// This is the smallest real-engine reproduction of the customer path that
// crossed the broken boundary: Distribute reads multipart model metadata and
// the Relay image validator consumes the same request next. It intentionally
// uses an enabled fixed channel so channel selection cannot hide a body error.
func TestImage2EditsDistributeThenRelayReusesMultipartBody(t *testing.T) {
	request := image2EditsMultipartRequest(t, "/v1/images/edits")
	recorder := serveImage2EditsChain(t, request)

	require.Equal(t, http.StatusOK, recorder.Code, "full chain response: %s", recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `"model":"gpt-image-2"`)
	require.Contains(t, recorder.Body.String(), `"prompt":"turn the image purple"`)
}

func TestImage2EditsMultipartContentTypeCaseVariantUsesSameChain(t *testing.T) {
	request := image2EditsMultipartRequest(t, "/v1/images/edits")
	request.Header.Set("Content-Type", strings.Replace(request.Header.Get("Content-Type"), "multipart/form-data", "Multipart/Form-Data", 1))
	recorder := serveImage2EditsChain(t, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

func TestImage2EditsCompressedMultipartUsesSameChain(t *testing.T) {
	for _, encoding := range []string{"gzip", "br"} {
		t.Run(encoding, func(t *testing.T) {
			original := image2EditsMultipartRequest(t, "/v1/images/edits")
			contentType := original.Header.Get("Content-Type")
			plain, err := io.ReadAll(original.Body)
			require.NoError(t, err)
			var encoded bytes.Buffer
			switch encoding {
			case "gzip":
				writer := gzip.NewWriter(&encoded)
				_, err = writer.Write(plain)
				require.NoError(t, err)
				require.NoError(t, writer.Close())
			case "br":
				writer := brotli.NewWriter(&encoded)
				_, err = writer.Write(plain)
				require.NoError(t, err)
				require.NoError(t, writer.Close())
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(encoded.Bytes()))
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("Content-Encoding", encoding)
			recorder := serveImage2EditsChain(t, request)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		})
	}
}

func TestBodyStorageCleanupRemovesMultipartSpillFileAfterHandler(t *testing.T) {
	previousLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = previousLimit })

	png := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, bytes.Repeat([]byte{0}, 2<<20)...)
	request := image2EditsMultipartRequestWithFiles(t, "/v1/images/edits", "image", [][]byte{png}, nil)
	var spillPath string
	router := gin.New()
	router.Use(BodyStorageCleanup())
	router.POST("/v1/images/edits", func(c *gin.Context) {
		form, err := common.ParseMultipartFormReusable(c)
		require.NoError(t, err)
		file, err := form.File["image"][0].Open()
		require.NoError(t, err)
		if diskFile, ok := file.(*os.File); ok {
			spillPath = diskFile.Name()
		}
		require.NoError(t, file.Close())
		c.Status(http.StatusNoContent)
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.NotEmpty(t, spillPath, "multipart fixture must spill to disk")
	_, err := os.Stat(spillPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestImage2EditsMultipartFieldAndImageVariantsUseSameChain(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, '\r', 'I', 'H', 'D', 'R'}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0}
	webp := []byte{'R', 'I', 'F', 'F', 12, 0, 0, 0, 'W', 'E', 'B', 'P', 'V', 'P', '8', ' '}
	tests := []struct {
		name      string
		fieldName string
		images    [][]byte
		mask      []byte
	}{
		{name: "image-png", fieldName: "image", images: [][]byte{png}},
		{name: "image-array-jpeg", fieldName: "image[]", images: [][]byte{jpeg}},
		{name: "indexed-webp", fieldName: "image[0]", images: [][]byte{webp}},
		{name: "multiple-with-mask", fieldName: "image[]", images: [][]byte{png, jpeg}, mask: png},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := image2EditsMultipartRequestWithFiles(t, "/v1/images/edits", test.fieldName, test.images, test.mask)
			recorder := serveImage2EditsChain(t, request)
			require.Equal(t, http.StatusOK, recorder.Code, "full chain response: %s", recorder.Body.String())
		})
	}
}

func TestImage2EditsInvalidFilesStopInValidation(t *testing.T) {
	tests := []struct {
		name   string
		images [][]byte
		want   string
	}{
		{name: "missing-image", images: nil, want: "image is required"},
		{name: "empty-image", images: [][]byte{{}}, want: "empty"},
		{name: "spoofed-image", images: [][]byte{[]byte("not an image")}, want: "unsupported image content"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := image2EditsMultipartRequestWithFiles(t, "/v1/images/edits", "image", test.images, nil)
			recorder := serveImage2EditsChain(t, request)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			require.Contains(t, recorder.Body.String(), test.want)
		})
	}
}

func TestImage2EditsMalformedRequestCreatesOneSafePreUpstreamLog(t *testing.T) {
	db := setupDistributorErrorLogTestDB(t)
	require.NoError(t, projecti18n.Init())
	gin.SetMode(gin.TestMode)
	upstreamCalls := 0
	router := gin.New()
	router.Use(BodyStorageCleanup())
	router.Use(func(c *gin.Context) {
		c.Set("id", 9301)
		c.Set("username", "image2-validation-user")
		c.Set("token_id", 9302)
		c.Set("token_name", "must-not-be-persisted")
		c.Set("group", "default")
		c.Set(common.RequestIdKey, "image2-validation-request-id")
		c.Set("use_channel", []string{})
		c.Next()
	})
	router.POST("/v1/images/edits", Distribute(), func(c *gin.Context) {
		upstreamCalls++
		c.Status(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader("not-a-multipart-form"))
	request.Header.Set("Content-Type", "multipart/form-data")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	require.Zero(t, upstreamCalls)
	var rows []model.Log
	require.NoError(t, db.Where("request_id = ?", "image2-validation-request-id").Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, model.LogTypeError, rows[0].Type)
	require.Zero(t, rows[0].ChannelId)
	require.Zero(t, rows[0].Quota)
	require.Empty(t, rows[0].TokenName)
	require.NotContains(t, rows[0].Other, "must-not-be-persisted")
	other := map[string]interface{}{}
	require.NoError(t, common.UnmarshalJsonStr(rows[0].Other, &other))
	require.Equal(t, "request_validation", other["error_stage"])
	require.Equal(t, false, other["upstream_called"])
	require.Equal(t, "not_started", other["billing_state"])
	require.Equal(t, true, other["charge_known"])
	require.Equal(t, false, other["charged"])
	require.Equal(t, common.Image2ValidationMissingBoundary, other["error_code"])
	require.NotContains(t, rows[0].Content, "boundary not found")
	require.NotContains(t, rows[0].Content, "unexpected EOF")
}
