package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func image2MultipartRequest(t *testing.T, path string, quality string, size string) (*gin.Context, *dto.ImageRequest) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "edit this image"))
	require.NoError(t, writer.WriteField("n", "1"))
	if quality != "" {
		require.NoError(t, writer.WriteField("quality", quality))
	}
	if size != "" {
		require.NoError(t, writer.WriteField("size", size))
	}
	part, err := writer.CreateFormFile("image", "reference.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("synthetic-reference-image"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, path, &body)
	ctx.Request.Header.Set("Content-Type", writer.FormDataContentType())
	request, err := helper.GetAndValidOpenAIImageRequest(ctx, relayconstant.Path2RelayMode(path))
	require.NoError(t, err)
	require.NotNil(t, request)
	require.Equal(t, "gpt-image-2", request.Model)
	require.Equal(t, uint(1), *request.N)
	require.NotEmpty(t, ctx.Request.MultipartForm.File["image"])
	return ctx, request
}

func TestImage2EditsMultipartProtocolMatrixSelectsOnlyAcceptedCapability(t *testing.T) {
	image2SmartRoutingFlag(t, true)
	image2EditsSmartRoutingFlag(t, true)
	accepted := image2ProfileChannel(44, "accepted-edits", 10,
		[]string{"generations", "edits"}, []string{"1024", "2048", "uhd"}, []string{"standard"}, 1, true)
	incompatible := image2ProfileChannel(23, "generation-only-history", 1,
		[]string{"generations"}, []string{"1024", "2048"}, []string{"standard"}, 1, false)

	for _, test := range []struct {
		name, path, quality, size string
	}{
		{name: "v1 omitted auto-size", path: "/v1/images/edits", size: "auto"},
		{name: "v1 explicit auto UHD", path: "/v1/images/edits", quality: "auto", size: "uhd"},
		{name: "v1 explicit standard", path: "/v1/images/edits", quality: "standard", size: "1024x1024"},
		{name: "pg omitted auto-size", path: "/pg/images/edits", size: "auto"},
		{name: "pg explicit standard", path: "/pg/images/edits", quality: "standard", size: "2048x2048"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, request := image2MultipartRequest(t, test.path, test.quality, test.size)
			info := &relaycommon.RelayInfo{
				OriginModelName: "gpt-image-2",
				RelayMode:       relayconstant.RelayModeImagesEdits,
			}
			reqCapability, err := ParseImage2RequestCapability(info, request)
			require.NoError(t, err)
			router := newImage2SmartRouter(reqCapability, []*model.Channel{incompatible, accepted})
			selected, routeErr := router.Next()
			require.Nil(t, routeErr)
			require.Equal(t, accepted.Id, selected.Id)
			require.Contains(t, router.DecisionSummary(), "23:operation_unsupported")

			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"created":1,"data":[{"url":"https://fake.invalid/edited.png"}]}`)
			}))
			defer fake.Close()
			response, err := http.Post(fake.URL, "application/json", bytes.NewReader([]byte(`{"model":"gpt-image-2"}`)))
			require.NoError(t, err)
			defer response.Body.Close()
			var imageResponse dto.ImageResponse
			require.NoError(t, json.NewDecoder(response.Body).Decode(&imageResponse))
			require.Len(t, imageResponse.Data, 1)
			require.NotEmpty(t, imageResponse.Data[0].Url)
			require.NotNil(t, ctx)
		})
	}
}

func TestImage2ProductionHistoryShapeEditsGateIsFailClosedUntilMigration(t *testing.T) {
	image2SmartRoutingFlag(t, true)
	image2EditsSmartRoutingFlag(t, true)
	history44 := image2ProfileChannel(44, "history-44", 10,
		[]string{"generations"}, []string{"1024", "2048"}, []string{"standard"}, 1, false)
	history23 := image2ProfileChannel(23, "history-23", 20,
		[]string{"generations"}, []string{"1024", "2048"}, []string{"standard"}, 1, false)
	history32 := image2ProfileChannel(32, "history-32", 30,
		[]string{"generations"}, []string{"uhd"}, nil, 1, false)
	history47 := image2ProfileChannel(47, "history-47", 40,
		[]string{"generations"}, []string{"uhd"}, []string{"high"}, 1, false)
	_, request := image2MultipartRequest(t, "/v1/images/edits", "auto", "auto")
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-image-2", RelayMode: relayconstant.RelayModeImagesEdits}
	reqCapability, err := ParseImage2RequestCapability(info, request)
	require.NoError(t, err)
	router := newImage2SmartRouter(reqCapability, []*model.Channel{history44, history23, history32, history47})
	require.Equal(t, 0, router.CandidateCount(), "r3 history shape must not guess edits support")
	require.Contains(t, router.DecisionSummary(), "44:operation_unsupported")
	require.Contains(t, router.DecisionSummary(), "23:operation_unsupported")
	require.Contains(t, router.DecisionSummary(), "32:operation_unsupported")
	require.Contains(t, router.DecisionSummary(), "47:operation_unsupported")
}

func TestImage2GenerationsRemainRoutableWithEditsGateEnabled(t *testing.T) {
	image2SmartRoutingFlag(t, true)
	image2EditsSmartRoutingFlag(t, true)
	generation := image2ProfileChannel(44, "generation", 10,
		[]string{"generations"}, []string{"1024", "2048"}, []string{"standard"}, 1, false)
	_, request := image2MultipartRequest(t, "/v1/images/edits", "standard", "1024")
	request.Image = nil
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-image-2", RelayMode: relayconstant.RelayModeImagesGenerations}
	reqCapability, err := ParseImage2RequestCapability(info, request)
	require.NoError(t, err)
	router := newImage2SmartRouter(reqCapability, []*model.Channel{generation})
	selected, routeErr := router.Next()
	require.Nil(t, routeErr)
	require.Equal(t, generation.Id, selected.Id)
}

func TestImage2GenerationJSONPositiveAndLegacyRegression(t *testing.T) {
	generation := image2ProfileChannel(44, "generation", 10,
		[]string{"generations"}, []string{"1024"}, []string{"standard"}, 1, false)
	requestBody := bytes.NewBufferString(`{"model":"gpt-image-2","prompt":"draw a test image","quality":"standard","size":"1024x1024","n":1}`)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", requestBody)
	ctx.Request.Header.Set("Content-Type", "application/json")
	request, err := helper.GetAndValidOpenAIImageRequest(ctx, relayconstant.Path2RelayMode("/v1/images/generations"))
	require.NoError(t, err)
	require.Equal(t, uint(1), *request.N)
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-image-2", RelayMode: relayconstant.RelayModeImagesGenerations}
	reqCapability, err := ParseImage2RequestCapability(info, request)
	require.NoError(t, err)

	image2SmartRoutingFlag(t, true)
	image2EditsSmartRoutingFlag(t, true)
	smartRouter := newImage2SmartRouter(reqCapability, []*model.Channel{generation})
	selected, routeErr := smartRouter.Next()
	require.Nil(t, routeErr)
	require.Equal(t, generation.Id, selected.Id)

	image2SmartRoutingFlag(t, false)
	legacyContext, _ := gin.CreateTestContext(nil)
	legacyRouter, legacyErr := NewImage2SmartRouter(legacyContext, info, request)
	require.Nil(t, legacyErr)
	require.Nil(t, legacyRouter, "global smart gate off must preserve the legacy generation selector")
}

func TestImage2FakeUpstreamEditsUsesEachChannelOnceAndReturnsNonEmptyImage(t *testing.T) {
	acceptedFirst := image2ProfileChannel(44, "accepted-first", 10,
		[]string{"edits"}, []string{"1024"}, []string{"standard"}, 1, true)
	acceptedSecond := image2ProfileChannel(32, "accepted-second", 20,
		[]string{"edits"}, []string{"1024"}, []string{"standard"}, 1, true)
	router := newImage2SmartRouter(Image2RequestCapability{
		Operation: "edits", Resolution: "1024", Quality: "standard", N: 1,
	}, []*model.Channel{acceptedSecond, acceptedFirst, acceptedFirst})

	requestID := "synthetic-edits-request"
	statuses := []int{http.StatusServiceUnavailable, http.StatusOK}
	expectedChannelIDs := []int{44, 32}
	servers := make([]*httptest.Server, 0, len(statuses))
	calls := make(map[int]int)
	for index, status := range statuses {
		channelID := expectedChannelIDs[index]
		fakeStatus := status
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, requestID, r.Header.Get("X-Request-ID"))
			calls[channelID]++
			w.Header().Set("Content-Type", "application/json")
			if fakeStatus != http.StatusOK {
				w.WriteHeader(fakeStatus)
				_, _ = io.WriteString(w, `{"error":{"message":"not accepted before dispatch"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"ZmFrZS1lZGl0"}]}`)
		}))
		servers = append(servers, server)
		t.Cleanup(server.Close)
	}

	var responseBody []byte
	for index, server := range servers {
		channel, routeErr := router.Next()
		require.Nil(t, routeErr)
		require.Equal(t, expectedChannelIDs[index], channel.Id, "each attempt must stay on its fixed capability channel")
		request, err := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader([]byte(`{"model":"gpt-image-2"}`)))
		require.NoError(t, err)
		request.Header.Set("X-Request-ID", requestID)
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		require.NoError(t, readErr)
		if response.StatusCode == http.StatusOK {
			responseBody = body
			break
		}
		decision := EvaluateImage2SafeFailover(SafeFailoverInput{
			RelayMode:      relayconstant.RelayModeImagesEdits,
			ModelName:      "gpt-image-2",
			AttemptElapsed: 100 * time.Millisecond,
			ImageGuard:     time.Minute,
			Error:          types.NewErrorWithStatusCode(fmt.Errorf("fake upstream not accepted"), types.ErrorCodeBadResponseStatusCode, response.StatusCode),
		})
		require.True(t, decision.Retry)
		require.Equal(t, index, 0, "only the first attempt may fail in this fixture")
	}

	var imageResponse dto.ImageResponse
	require.NoError(t, json.Unmarshal(responseBody, &imageResponse))
	require.Len(t, imageResponse.Data, 1)
	require.NotEmpty(t, imageResponse.Data[0].B64Json)
	require.Equal(t, map[int]int{44: 1, 32: 1}, calls)
}
