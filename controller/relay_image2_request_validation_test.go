package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func relayImage2ValidationResponse(t *testing.T, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	Relay(context, types.RelayFormatOpenAIImage)
	common.CleanupBodyStorage(context)
	if request.MultipartForm != nil {
		_ = request.MultipartForm.RemoveAll()
	}
	require.False(t, common.GetContextKeyBool(context, constant.ContextKeyUpstreamCalled))
	return recorder
}

func responseErrorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code any `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload), recorder.Body.String())
	return payload.Error.Code.(string)
}

func TestRelayImage2MalformedMultipartFails400BeforeUpstream(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{name: "missing-boundary", contentType: "multipart/form-data", body: []byte("not-a-form")},
		{name: "truncated-body", contentType: "multipart/form-data; boundary=cut", body: []byte("--cut\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\ngpt-image-2\r\n--cut\r\nContent-Disposition: form-data; name=\"image\"; filename=\"x.png\"\r\nContent-Type: image/png\r\n\r\n\x89PNG")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			recorder := relayImage2ValidationResponse(t, request)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			require.Equal(t, string(types.ErrorCodeInvalidRequest), responseErrorCode(t, recorder))
		})
	}
}

func TestRelayImage2OversizedInputFails413BeforeUpstream(t *testing.T) {
	previousLimit := constant.MaxImage2InputMB
	constant.MaxImage2InputMB = 1
	t.Cleanup(func() { constant.MaxImage2InputMB = previousLimit })

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	part, err := writer.CreateFormFile("image", "large.png")
	require.NoError(t, err)
	_, err = part.Write(bytes.Repeat([]byte{'x'}, 1<<20+1))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())

	recorder := relayImage2ValidationResponse(t, request)
	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code, recorder.Body.String())
	require.Equal(t, string(types.ErrorCodeReadRequestBodyFailed), responseErrorCode(t, recorder))
}

func TestRelayImage2EditsFullChainCallsFakeUpstreamAndBillsOnce(t *testing.T) {
	db := openSecureSkillControllerIntegrationDB(t)
	oldPrices := ratio_setting.ModelPrice2JSONString()
	prices := ratio_setting.GetModelPriceMap()
	prices["gpt-image-2"] = 0.001
	priceJSON, err := common.Marshal(prices)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(priceJSON)))
	oldSmartRouting, oldRouteMode := common.Image2SmartRoutingEnabled, common.Image2RouteMode
	oldSafeFailover, oldRetryTimes := common.SafeFailoverV1Enabled, common.RetryTimes
	common.Image2SmartRoutingEnabled = false
	common.Image2RouteMode = common.Image2RouteModeLegacy
	common.SafeFailoverV1Enabled = false
	common.RetryTimes = 0
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrices))
		common.Image2SmartRoutingEnabled, common.Image2RouteMode = oldSmartRouting, oldRouteMode
		common.SafeFailoverV1Enabled, common.RetryTimes = oldSafeFailover, oldRetryTimes
	})

	const userID, tokenID, channelID = 4701, 4702, 4703
	const initialUserQuota, initialTokenQuota = 10000, 9000
	seedSecureSkillControllerUser(t, db, userID, initialUserQuota)
	seedSecureSkillControllerToken(t, db, tokenID, userID, "isolated-image2-token", initialTokenQuota)

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		upstreamCalls++
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "/v1/images/edits", request.URL.Path)
		require.NoError(t, request.ParseMultipartForm(2<<20))
		require.Equal(t, "gpt-image-2", request.PostForm.Get("model"))
		require.Equal(t, "turn the image purple", request.PostForm.Get("prompt"))
		require.Len(t, request.MultipartForm.File["image"], 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`)
	}))
	t.Cleanup(upstream.Close)
	baseURL := upstream.URL
	require.NoError(t, db.Create(&model.Channel{
		Id: channelID, Type: constant.ChannelTypeOpenAI, Key: "isolated-upstream-key",
		Status: common.ChannelStatusEnabled, Name: "image2-edits-full-chain-fake",
		BaseURL: &baseURL, Group: "default", Models: "gpt-image-2",
	}).Error)

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

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	context.Request.Header.Set("Content-Type", writer.FormDataContentType())
	requestID := "image2-edits-full-chain-isolated"
	context.Set(common.RequestIdKey, requestID)
	common.SetContextKey(context, constant.ContextKeyUserId, userID)
	common.SetContextKey(context, constant.ContextKeyUserQuota, initialUserQuota)
	common.SetContextKey(context, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(context, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(context, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
	common.SetContextKey(context, constant.ContextKeyTokenId, tokenID)
	common.SetContextKey(context, constant.ContextKeyTokenKey, "isolated-image2-token")
	common.SetContextKey(context, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(context, constant.ContextKeyTokenUnlimited, false)
	common.SetContextKey(context, constant.ContextKeyOriginalModel, "gpt-image-2")
	common.SetContextKey(context, constant.ContextKeyChannelId, channelID)
	common.SetContextKey(context, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	common.SetContextKey(context, constant.ContextKeyChannelName, "image2-edits-full-chain-fake")
	common.SetContextKey(context, constant.ContextKeyChannelKey, "isolated-upstream-key")
	common.SetContextKey(context, constant.ContextKeyChannelBaseUrl, baseURL)
	common.SetContextKey(context, constant.ContextKeyChannelSetting, dto.ChannelSettings{})
	common.SetContextKey(context, constant.ContextKeyChannelOtherSetting, dto.ChannelOtherSettings{})
	common.SetContextKey(context, constant.ContextKeyChannelParamOverride, map[string]interface{}{})
	common.SetContextKey(context, constant.ContextKeyChannelHeaderOverride, map[string]interface{}{})
	common.SetContextKey(context, constant.ContextKeyChannelIsMultiKey, false)
	common.SetContextKey(context, constant.ContextKeyChannelAutoBan, false)
	common.SetContextKey(context, constant.ContextKeyChannelStatusCodeMapping, "")
	common.SetContextKey(context, constant.ContextKeyRequestStartTime, time.Now())
	context.Set("auto_ban", false)
	context.Set("token_name", "image2-edits-integration-token")
	context.Set("use_channel", []string{})

	Relay(context, types.RelayFormatOpenAIImage)
	common.CleanupBodyStorage(context)
	if context.Request.MultipartForm != nil {
		_ = context.Request.MultipartForm.RemoveAll()
	}
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, 1, upstreamCalls)
	require.Contains(t, recorder.Body.String(), `"b64_json":"aGVsbG8="`)

	var user model.User
	require.NoError(t, db.First(&user, userID).Error)
	require.Equal(t, initialUserQuota-500, user.Quota)
	require.Equal(t, 500, user.UsedQuota)
	require.Equal(t, 1, user.RequestCount)
	var token model.Token
	require.NoError(t, db.First(&token, tokenID).Error)
	require.Equal(t, initialTokenQuota-500, token.RemainQuota)
	require.Equal(t, 500, token.UsedQuota)
	var consumeCount int64
	require.NoError(t, db.Model(&model.Log{}).Where("type = ? and request_id = ?", model.LogTypeConsume, requestID).Count(&consumeCount).Error)
	require.Equal(t, int64(1), consumeCount)

	service.CleanupFileSources(context)
}
