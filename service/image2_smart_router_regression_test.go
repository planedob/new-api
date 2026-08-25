package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

type image2RoundTripFunc func(*http.Request) (*http.Response, error)

func (f image2RoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func image2FakeHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func TestImage2RegressionReplaysSameRequestAcrossDistinctChannelsOnce(t *testing.T) {
	const requestID = "image2-regression-request-1"
	requestBody := []byte(`{"model":"gpt-image-2","prompt":"a blue square"}`)
	statuses := []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusOK}
	requestCounts := make([]int, len(statuses))
	receivedBodies := make([][]byte, len(statuses))
	receivedRequestIDs := make([]string, len(statuses))
	attemptIndex := 0
	client := &http.Client{Transport: image2RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		index := attemptIndex
		attemptIndex++
		requestCounts[index]++
		receivedBodies[index], _ = io.ReadAll(request.Body)
		receivedRequestIDs[index] = request.Header.Get(common.RequestIdKey)
		return image2FakeHTTPResponse(statuses[index], ""), nil
	})}

	router := newImage2SmartRouter(Image2RequestCapability{Operation: "generations", Resolution: "1024", N: 1}, []*model.Channel{
		image2TestChannel(1, 10, []string{"generations"}, []string{"1024"}, false),
		image2TestChannel(2, 20, []string{"generations"}, []string{"1024"}, false),
		image2TestChannel(3, 30, []string{"generations"}, []string{"1024"}, false),
	})
	retryParam := &RetryParam{Retry: common.GetPointer(0), Image2Router: router, ExhaustiveSafeFailover: true}
	require.True(t, router.HasCandidates())
	usedChannels := make([]int, 0, len(statuses))

	for range statuses {
		channel, routeErr := retryParam.Image2Router.Next()
		require.Nil(t, routeErr)
		require.False(t, retryParam.IsChannelExcluded(channel.Id))
		retryParam.ExcludeChannel(channel.Id)
		usedChannels = append(usedChannels, channel.Id)

		request, requestErr := http.NewRequest(http.MethodPost, "https://fake-upstream.invalid/attempt", bytes.NewReader(requestBody))
		require.NoError(t, requestErr)
		request.Header.Set(common.RequestIdKey, requestID)
		response, requestErr := client.Do(request)
		require.NoError(t, requestErr)
		_, _ = io.Copy(io.Discard, response.Body)
		require.NoError(t, response.Body.Close())

		if response.StatusCode == http.StatusOK {
			break
		}
		decision := EvaluateSafeFailover(SafeFailoverInput{
			RelayMode: relayconstant.RelayModeImagesGenerations, ModelName: "gpt-image-2",
			Image2SmartRouting: true,
			Error:              types.NewErrorWithStatusCode(errors.New("fake upstream unavailable"), types.ErrorCodeBadResponseStatusCode, response.StatusCode),
		})
		require.True(t, decision.Retry)
		retryParam.IncreaseRetry()
	}

	require.Equal(t, []int{1, 2, 3}, usedChannels)
	for index := range statuses {
		require.Equal(t, 1, requestCounts[index])
		require.Equal(t, requestBody, receivedBodies[index])
		require.Equal(t, requestID, receivedRequestIDs[index])
	}
}

func TestImage2RegressionDoesNotReplayGuardedFailures(t *testing.T) {
	tests := []struct {
		name              string
		status            int
		errorCode         types.ErrorCode
		message           string
		responseWritten   bool
		responseCount     int
		requestContextErr error
		reason            string
	}{
		{name: "400 client error", status: http.StatusBadRequest, errorCode: types.ErrorCodeBadResponseStatusCode, message: "invalid argument", reason: "client_or_protocol_400"},
		{name: "content safety", status: http.StatusForbidden, errorCode: types.ErrorCodePromptBlocked, message: "prompt blocked by content safety", reason: "content_safety"},
		{name: "upstream accepted task", status: http.StatusServiceUnavailable, errorCode: types.ErrorCodeBadResponseStatusCode, message: "request accepted task_id=img_123", reason: "upstream_accepted"},
		{name: "response already started", status: http.StatusBadGateway, errorCode: types.ErrorCodeBadResponseStatusCode, message: "stream interrupted", responseCount: 1, reason: "response_started"},
		{name: "client canceled", status: http.StatusBadGateway, errorCode: types.ErrorCodeBadResponseStatusCode, message: "connection closed", requestContextErr: context.Canceled, reason: "client_canceled"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			firstCalls, fallbackCalls := 0, 0
			client := &http.Client{Transport: image2RoundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Host {
				case "first.invalid":
					firstCalls++
					return image2FakeHTTPResponse(test.status, ""), nil
				case "fallback.invalid":
					fallbackCalls++
					return image2FakeHTTPResponse(http.StatusOK, ""), nil
				default:
					return nil, errors.New("unexpected fake upstream")
				}
			})}

			response, requestErr := client.Get("https://first.invalid/image")
			require.NoError(t, requestErr)
			require.NoError(t, response.Body.Close())
			decision := EvaluateSafeFailover(SafeFailoverInput{
				RelayMode: relayconstant.RelayModeImagesGenerations, ModelName: "gpt-image-2", Image2SmartRouting: true,
				ResponseWritten: test.responseWritten, ReceivedResponseCount: test.responseCount, RequestContextErr: test.requestContextErr,
				Error: types.NewErrorWithStatusCode(errors.New(test.message), test.errorCode, test.status),
			})
			if decision.Retry {
				_, requestErr = client.Get("https://fallback.invalid/image")
				require.NoError(t, requestErr)
			}
			require.False(t, decision.Retry)
			require.Equal(t, test.reason, decision.Reason)
			require.Equal(t, 1, firstCalls)
			require.Zero(t, fallbackCalls)
		})
	}
}

func TestImage2PassiveTimeoutObservationKeepsFailoverState(t *testing.T) {
	oldEnabled := constant.Image2PassiveMonitorEnabled
	constant.Image2PassiveMonitorEnabled = true
	t.Cleanup(func() {
		constant.Image2PassiveMonitorEnabled = oldEnabled
		resetRelayPassiveMonitorForTest()
	})

	for _, test := range []struct {
		name   string
		status int
	}{{name: "504 gateway timeout", status: http.StatusGatewayTimeout}, {name: "524 cloudflare timeout", status: 524}} {
		t.Run(test.name, func(t *testing.T) {
			status := test.status
			resetRelayPassiveMonitorForTest()
			response := image2FakeHTTPResponse(status, `{"error":{"message":"gateway timeout"}}`)
			response.Header.Set("Content-Type", "application/json")
			apiErr := RelayErrorHandler(context.Background(), response, false)
			require.Equal(t, status, apiErr.StatusCode)

			c := relayObservabilityTestContext("/v1/images/generations")
			c.Set("use_channel", []string{"44"})
			SetImage2PassiveRequestCapability(c, Image2RequestCapability{Operation: "generations", Resolution: "1024", Quality: "standard", N: 1})
			before := EvaluateSafeFailover(SafeFailoverInput{RelayMode: relayconstant.RelayModeImagesGenerations, ModelName: "gpt-image-2", Image2SmartRouting: true, Error: apiErr})
			ObserveImage2UpstreamError(c, apiErr, 44)
			after := EvaluateSafeFailover(SafeFailoverInput{RelayMode: relayconstant.RelayModeImagesGenerations, ModelName: "gpt-image-2", Image2SmartRouting: true, Error: apiErr})
			require.Equal(t, before, after)
			require.Equal(t, []string{"44"}, c.GetStringSlice("use_channel"))
			router := newImage2SmartRouter(Image2RequestCapability{Operation: "generations", Resolution: "1024", N: 1}, []*model.Channel{
				image2TestChannel(44, 10, []string{"generations"}, []string{"1024"}, false),
				image2TestChannel(75, 20, []string{"generations"}, []string{"1024"}, false),
			})
			firstCandidate, routeErr := router.Next()
			require.Nil(t, routeErr)
			require.Equal(t, 44, firstCandidate.Id, "timeout observation must not advance the candidate chain")

			snapshot := RelayPassiveMonitorSnapshot()
			require.Len(t, snapshot.Series, 1)
			require.Equal(t, status, snapshot.Series[0].StatusCode)
			require.True(t, snapshot.Series[0].UpstreamCalled)
		})
	}
}
