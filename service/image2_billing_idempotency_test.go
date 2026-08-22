package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// image2IdempotencyFunding is a local accounting double. It records every
// billing lifecycle operation so failover tests can prove that retrying an
// upstream request does not retry customer billing.
type image2IdempotencyFunding struct {
	mu           sync.Mutex
	preConsumed  []int
	settleDeltas []int
	refunds      int
}

func (f *image2IdempotencyFunding) Source() string { return BillingSourceWallet }

func (f *image2IdempotencyFunding) PreConsume(amount int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preConsumed = append(f.preConsumed, amount)
	return nil
}

func (f *image2IdempotencyFunding) Settle(delta int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settleDeltas = append(f.settleDeltas, delta)
	return nil
}

func (f *image2IdempotencyFunding) Refund() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refunds++
	return nil
}

func (f *image2IdempotencyFunding) snapshot() (preConsumed, settleDeltas []int, refunds int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	preConsumed = append([]int(nil), f.preConsumed...)
	settleDeltas = append([]int(nil), f.settleDeltas...)
	return preConsumed, settleDeltas, f.refunds
}

func image2IdempotencyContext(requestID string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	c.Set(common.RequestIdKey, requestID)
	return c
}

type image2FakeUpstream struct {
	server *httptest.Server
	calls  atomic.Int32
}

func newImage2FakeUpstream(t *testing.T, requestID string, status int, body string) *image2FakeUpstream {
	t.Helper()
	fake := &image2FakeUpstream{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.calls.Add(1)
		if got := r.Header.Get("X-Request-ID"); got != requestID {
			t.Errorf("upstream request id = %q, want %q", got, requestID)
		}
		if got := r.URL.Path; got != "/v1/images/generations" {
			t.Errorf("upstream path = %q, want /v1/images/generations", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func image2DoFakeAttempt(t *testing.T, endpoint *image2FakeUpstream, requestID string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, endpoint.server.URL+"/v1/images/generations", nil)
	require.NoError(t, err)
	req.Header.Set("X-Request-ID", requestID)
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, response.Body.Close())
	require.NoError(t, err)
	return response.StatusCode, string(body)
}

func image2RequireRefundOnce(t *testing.T, funding *image2IdempotencyFunding) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, _, refunds := funding.snapshot()
		return refunds == 1
	}, time.Second, 10*time.Millisecond, "failed Image2 request must refund once")
	preConsumed, settleDeltas, refunds := funding.snapshot()
	require.Equal(t, []int{100}, preConsumed)
	require.Empty(t, settleDeltas)
	require.Equal(t, 1, refunds)
}

// TestImage2Failover503503200ChargesAndSettlesOnce proves the complete local
// lifecycle: one customer pre-consume, one attempt per ordered channel, two
// safely replayable 503s, then one successful settlement. Calling Settle a
// second time is an explicit idempotency check.
func TestImage2Failover503503200ChargesAndSettlesOnce(t *testing.T) {
	requestID := "image2-idempotency-503-503-200"
	funding := &image2IdempotencyFunding{}
	info := &relaycommon.RelayInfo{
		RequestId:       requestID,
		IsPlayground:    true,
		ForcePreConsume: true,
		OriginModelName: "gpt-image-2",
		RelayMode:       relayconstant.RelayModeImagesGenerations,
	}
	session := &BillingSession{relayInfo: info, funding: funding}
	ctx := image2IdempotencyContext(requestID)
	require.Nil(t, session.preConsume(ctx, 100))

	channels := []*image2FakeUpstream{
		newImage2FakeUpstream(t, requestID, http.StatusServiceUnavailable, `{"error":{"message":"temporary unavailable"}}`),
		newImage2FakeUpstream(t, requestID, http.StatusServiceUnavailable, `{"error":{"message":"temporary unavailable"}}`),
		newImage2FakeUpstream(t, requestID, http.StatusOK, `{"created":1,"data":[{"b64_json":"ZmFrZS1pbWFnZQ=="}]}`),
	}
	router := newImage2SmartRouter(Image2RequestCapability{
		Operation:  "generations",
		Resolution: "1024",
		N:          1,
	}, []*model.Channel{
		image2TestChannel(101, 10, []string{"generations"}, []string{"1024"}, false),
		image2TestChannel(102, 20, []string{"generations"}, []string{"1024"}, false),
		image2TestChannel(103, 30, []string{"generations"}, []string{"1024"}, false),
	})

	for attempt := 0; attempt < len(channels); attempt++ {
		channel, err := router.Next()
		require.Nil(t, err, "attempt=%d decisions=%s", attempt, router.DecisionSummary())
		require.Equal(t, 101+attempt, channel.Id, "attempts must follow the capability-ordered chain")
		status, body := image2DoFakeAttempt(t, channels[attempt], requestID)
		if status == http.StatusOK {
			require.Contains(t, body, "ZmFrZS1pbWFnZQ==")
			require.NoError(t, session.Settle(120))
			require.NoError(t, session.Settle(120))
			require.Equal(t, 103, channel.Id)
			break
		}
		decision := EvaluateSafeFailover(SafeFailoverInput{
			RetryIndex:  attempt,
			MaxAttempts: 0,
			RelayMode:   info.RelayMode,
			ModelName:   info.OriginModelName,
			Error: types.NewErrorWithStatusCode(
				fmt.Errorf("fake upstream status %d: %s", status, body),
				types.ErrorCodeBadResponseStatusCode,
				status,
			),
		})
		require.True(t, decision.Retry, "503 must safely move to the next eligible channel")
	}

	for _, endpoint := range channels {
		require.Equal(t, int32(1), endpoint.calls.Load(), "same Request ID must call each channel at most once")
	}
	preConsumed, settleDeltas, refunds := funding.snapshot()
	require.Equal(t, []int{100}, preConsumed, "failover must pre-consume customer quota once")
	require.Equal(t, []int{20}, settleDeltas, "settlement delta must be applied once")
	require.Equal(t, 0, refunds, "successful request must not be refunded")
	_, err := router.Next()
	require.Error(t, err, "the exhausted route must not reuse a channel")
}

func TestImage2FailoverStopsWithoutReplayAndRefundsOnce(t *testing.T) {
	tests := []struct {
		name              string
		status            int
		body              string
		requestContextErr error
		responseCount     int
		wantReason        string
	}{
		{
			name:       "accepted upstream task",
			status:     http.StatusBadGateway,
			body:       `{"error":{"message":"job_id=fake-job queued"}}`,
			wantReason: "upstream_accepted",
		},
		{
			name:          "response already started",
			status:        http.StatusBadGateway,
			body:          `{"error":{"message":"stream interrupted"}}`,
			responseCount: 1,
			wantReason:    "response_started",
		},
		{
			name:              "client canceled",
			status:            http.StatusServiceUnavailable,
			body:              `{"error":{"message":"connection closed"}}`,
			requestContextErr: context.Canceled,
			wantReason:        "client_canceled",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestID := "image2-idempotency-stop-" + test.name
			funding := &image2IdempotencyFunding{}
			info := &relaycommon.RelayInfo{
				RequestId:       requestID,
				IsPlayground:    true,
				ForcePreConsume: true,
				OriginModelName: "gpt-image-2",
				RelayMode:       relayconstant.RelayModeImagesGenerations,
			}
			session := &BillingSession{relayInfo: info, funding: funding}
			ctx := image2IdempotencyContext(requestID)
			require.Nil(t, session.preConsume(ctx, 100))
			first := newImage2FakeUpstream(t, requestID, test.status, test.body)
			second := newImage2FakeUpstream(t, requestID, http.StatusOK, `{"created":1,"data":[{"b64_json":"bm90LXNlbGVjdGVk"}]}`)

			status, body := image2DoFakeAttempt(t, first, requestID)
			decision := EvaluateSafeFailover(SafeFailoverInput{
				RelayMode:             info.RelayMode,
				ModelName:             info.OriginModelName,
				ReceivedResponseCount: test.responseCount,
				RequestContextErr:     test.requestContextErr,
				Error: types.NewErrorWithStatusCode(
					fmt.Errorf("fake upstream status %d: %s", status, body),
					types.ErrorCodeBadResponseStatusCode,
					status,
				),
			})
			require.False(t, decision.Retry)
			require.Equal(t, test.wantReason, decision.Reason)

			// The second channel exists solely to prove the stop decision prevents
			// a second upstream POST. It must remain untouched in every case.
			if decision.Retry {
				_, _ = image2DoFakeAttempt(t, second, requestID)
			}
			session.Refund(ctx)
			session.Refund(ctx)
			require.Equal(t, int32(1), first.calls.Load())
			require.Equal(t, int32(0), second.calls.Load(), "stop conditions must not fail over")
			image2RequireRefundOnce(t, funding)
		})
	}
}
