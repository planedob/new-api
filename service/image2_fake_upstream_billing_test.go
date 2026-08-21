package service

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type image2FakeFunding struct {
	mu           sync.Mutex
	preConsume   []int
	settleDeltas []int
	refunds      int
}

func (f *image2FakeFunding) Source() string { return BillingSourceWallet }

func (f *image2FakeFunding) PreConsume(amount int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preConsume = append(f.preConsume, amount)
	return nil
}

func (f *image2FakeFunding) Settle(delta int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settleDeltas = append(f.settleDeltas, delta)
	return nil
}

func (f *image2FakeFunding) Refund() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refunds++
	return nil
}

func image2BillingTestContext(requestID string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
	c.Set("X-Oneapi-Request-Id", requestID)
	return c
}

func TestImage2BillingFakeUpstreamSuccessPrechargesAndSettlesOnce(t *testing.T) {
	requestID := "image2-billing-success-1"
	funding := &image2FakeFunding{}
	info := &relaycommon.RelayInfo{
		RequestId:       requestID,
		IsPlayground:    true,
		ForcePreConsume: true,
		OriginModelName: "gpt-image-2",
	}
	session := &BillingSession{relayInfo: info, funding: funding}
	ctx := image2BillingTestContext(requestID)

	require.Nil(t, session.preConsume(ctx, 100))
	require.NoError(t, session.Settle(80))
	require.NoError(t, session.Settle(80))
	session.Refund(ctx)

	funding.mu.Lock()
	defer funding.mu.Unlock()
	require.Equal(t, requestID, info.RequestId)
	require.Equal(t, []int{100}, funding.preConsume)
	require.Equal(t, []int{-20}, funding.settleDeltas)
	require.Equal(t, 0, funding.refunds, "a settled request must not be refunded")
}

func TestImage2BillingFakeUpstreamFailureRefundsOnce(t *testing.T) {
	requestID := "image2-billing-failure-1"
	funding := &image2FakeFunding{}
	info := &relaycommon.RelayInfo{
		RequestId:       requestID,
		IsPlayground:    true,
		ForcePreConsume: true,
		OriginModelName: "gpt-image-2",
	}
	session := &BillingSession{relayInfo: info, funding: funding}
	ctx := image2BillingTestContext(requestID)

	require.Nil(t, session.preConsume(ctx, 100))
	session.Refund(ctx)
	session.Refund(ctx)

	require.Eventually(t, func() bool {
		funding.mu.Lock()
		defer funding.mu.Unlock()
		return funding.refunds == 1
	}, time.Second, 10*time.Millisecond)
	funding.mu.Lock()
	defer funding.mu.Unlock()
	require.Equal(t, []int{100}, funding.preConsume)
	require.Empty(t, funding.settleDeltas)
	require.Equal(t, 1, funding.refunds)
}

func TestImage2BillingFakeUpstreamLateChargeSettlesDeltaOnce(t *testing.T) {
	requestID := "image2-billing-late-charge-1"
	funding := &image2FakeFunding{}
	info := &relaycommon.RelayInfo{
		RequestId:       requestID,
		IsPlayground:    true,
		ForcePreConsume: true,
		OriginModelName: "gpt-image-2",
	}
	session := &BillingSession{relayInfo: info, funding: funding}
	ctx := image2BillingTestContext(requestID)

	require.Nil(t, session.preConsume(ctx, 100))
	require.NoError(t, session.Settle(140))
	require.NoError(t, session.Settle(140))
	session.Refund(ctx)

	funding.mu.Lock()
	defer funding.mu.Unlock()
	require.Equal(t, []int{100}, funding.preConsume)
	require.Equal(t, []int{40}, funding.settleDeltas, "late charge is the one actual-minus-precharged delta")
	require.Equal(t, 0, funding.refunds)
}

func TestImage2FakeUpstreamAndBillingStayOneRequest(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		mode       int
		status     int
		body       string
		actual     int
		wantRefund bool
		accepted   bool
	}{
		{
			name:   "generation success settles once",
			path:   "/v1/images/generations",
			mode:   relayconstant.RelayModeImagesGenerations,
			status: http.StatusOK,
			body:   `{"created":1,"data":[{"b64_json":"ZmFrZS1nZW4="}]}`,
			actual: 80,
		},
		{
			name:   "edit success settles once",
			path:   "/v1/images/edits",
			mode:   relayconstant.RelayModeImagesEdits,
			status: http.StatusOK,
			body:   `{"created":1,"data":[{"url":"https://fake.invalid/edit.png"}]}`,
			actual: 80,
		},
		{
			name:       "generation rejection refunds once",
			path:       "/v1/images/generations",
			mode:       relayconstant.RelayModeImagesGenerations,
			status:     http.StatusServiceUnavailable,
			body:       `{"error":{"message":"not accepted before dispatch"}}`,
			wantRefund: true,
		},
		{
			name:       "edit accepted timeout never replays",
			path:       "/v1/images/edits",
			mode:       relayconstant.RelayModeImagesEdits,
			status:     http.StatusGatewayTimeout,
			body:       `{"error":{"message":"job_id=fake-edit-1 queued"}}`,
			wantRefund: true,
			accepted:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestID := "image2-composed-" + test.name
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				require.Equal(t, test.path, r.URL.Path)
				require.Equal(t, requestID, r.Header.Get("X-Request-ID"))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()

			funding := &image2FakeFunding{}
			info := &relaycommon.RelayInfo{
				RequestId:       requestID,
				IsPlayground:    true,
				ForcePreConsume: true,
				OriginModelName: "gpt-image-2",
				RelayMode:       test.mode,
			}
			session := &BillingSession{relayInfo: info, funding: funding}
			ctx := image2BillingTestContext(requestID)
			require.Nil(t, session.preConsume(ctx, 100))

			req, err := http.NewRequest(http.MethodPost, server.URL+test.path, nil)
			require.NoError(t, err)
			req.Header.Set("X-Request-ID", requestID)
			response, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			responseBody, err := io.ReadAll(response.Body)
			response.Body.Close()
			require.NoError(t, err)
			require.Equal(t, test.status, response.StatusCode)
			require.Equal(t, test.body, string(responseBody))

			if test.status == http.StatusOK {
				require.NoError(t, session.Settle(test.actual))
				require.NoError(t, session.Settle(test.actual))
			} else {
				decision := EvaluateSafeFailover(SafeFailoverInput{
					RelayMode: test.mode,
					ModelName: "gpt-image-2",
					Error: types.NewErrorWithStatusCode(
						fmt.Errorf("fake upstream: %s", string(responseBody)),
						types.ErrorCodeBadResponseStatusCode,
						test.status,
					),
				})
				if test.accepted {
					require.False(t, decision.Retry)
					require.Equal(t, "upstream_accepted", decision.Reason)
				}
				session.Refund(ctx)
				session.Refund(ctx)
			}

			require.Eventually(t, func() bool {
				funding.mu.Lock()
				defer funding.mu.Unlock()
				return calls.Load() == 1 && len(funding.preConsume) == 1 &&
					((test.status == http.StatusOK && len(funding.settleDeltas) == 1 && funding.refunds == 0) ||
						(test.wantRefund && len(funding.settleDeltas) == 0 && funding.refunds == 1))
			}, time.Second, 10*time.Millisecond)
			funding.mu.Lock()
			defer funding.mu.Unlock()
			require.Equal(t, int32(1), calls.Load(), "polling or failover must not call the fake upstream again")
			require.Equal(t, requestID, info.RequestId)
		})
	}
}
