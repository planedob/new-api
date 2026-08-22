package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func relayObservabilityTestContext(path string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	c.Set("id", 901)
	c.Set("username", "observability-test-user")
	c.Set("token_id", 902)
	c.Set("token_name", "must-not-be-recorded")
	c.Set("original_model", "gpt-image-2")
	c.Set("group", "image2-test")
	c.Set("use_channel", []string{})
	c.Set(common.RequestIdKey, "synthetic-request-id")
	common.SetContextKey(c, constant.ContextKeyRequestStartTime, time.Now().Add(-1500*time.Millisecond))
	return c
}

func TestRelayObservabilityFlagsOffIsNoOp(t *testing.T) {
	previousErrorLog := constant.ErrorLogEnabled
	previousPassive := constant.Image2PassiveMonitorEnabled
	constant.ErrorLogEnabled = false
	constant.Image2PassiveMonitorEnabled = false
	resetRelayPassiveMonitorForTest()
	t.Cleanup(func() {
		constant.ErrorLogEnabled = previousErrorLog
		constant.Image2PassiveMonitorEnabled = previousPassive
		resetRelayPassiveMonitorForTest()
	})

	truncate(t)
	c := relayObservabilityTestContext("/v1/images/generations")
	err := types.NewErrorWithStatusCode(errors.New("synthetic no candidate"), types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable)
	capability := Image2RequestCapability{Operation: "generations", Resolution: "1024", Quality: "standard", N: 1}
	require.False(t, RecordRelayErrorLog(c, err, RelayErrorLogOptions{
		Stage: "channel_selection", Image2: &capability, BillingState: RelayErrorBillingNotStarted,
		Charged: common.GetPointer(false),
	}))
	assert.Empty(t, RelayPassiveMonitorSnapshot().Series)
	var count int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestRecordRelayErrorLogPersistsSafeNoCandidateEvent(t *testing.T) {
	previousErrorLog := constant.ErrorLogEnabled
	previousPassive := constant.Image2PassiveMonitorEnabled
	constant.ErrorLogEnabled = true
	constant.Image2PassiveMonitorEnabled = true
	resetRelayPassiveMonitorForTest()
	t.Cleanup(func() {
		constant.ErrorLogEnabled = previousErrorLog
		constant.Image2PassiveMonitorEnabled = previousPassive
		resetRelayPassiveMonitorForTest()
	})

	truncate(t)
	c := relayObservabilityTestContext("/v1/images/generations?token=do-not-store")
	err := types.NewErrorWithStatusCode(errors.New("prompt=secret provider-body=secret"), types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable)
	capability := Image2RequestCapability{Operation: "generations", Resolution: "2048", Quality: "high", N: 1}
	require.True(t, RecordRelayErrorLog(c, err, RelayErrorLogOptions{
		Stage:          "channel_selection",
		ModelName:      "gpt-image-2",
		Group:          "image2-test",
		UpstreamCalled: false,
		BillingState:   RelayErrorBillingNotStarted,
		Charged:        common.GetPointer(false),
		Image2:         &capability,
		Extra: map[string]interface{}{
			"image2_candidate_decisions": "44:resolution_unsupported,32:quality_unsupported",
			"prompt":                     "prompt-secret-must-not-enter-log",
			"provider_body":              "provider-secret-must-not-enter-log",
		},
	}))

	var row model.Log
	require.NoError(t, model.LOG_DB.Where("request_id = ?", "synthetic-request-id").First(&row).Error)
	assert.Equal(t, 0, row.ChannelId)
	assert.Equal(t, "gpt-image-2", row.ModelName)
	assert.Empty(t, row.TokenName)
	assert.Zero(t, row.TokenId)
	assert.Zero(t, row.Quota)
	assert.NotContains(t, row.Content, "provider-body")
	assert.NotContains(t, row.Other, "prompt-secret")
	assert.NotContains(t, row.Other, "provider-secret")
	assert.NotContains(t, row.Other, "request_path")
	assert.NotContains(t, row.Other, "token=do-not-store")
	assert.Contains(t, row.Other, "image2_candidate_decisions")

	var other map[string]interface{}
	require.NoError(t, common.UnmarshalJsonStr(row.Other, &other))
	assert.Equal(t, "channel_selection", other["error_stage"])
	assert.Equal(t, false, other["upstream_called"])
	assert.Equal(t, "not_started", other["billing_state"])
	assert.Equal(t, false, other["charged"])
	assert.Equal(t, "images", other["request_route"])

	snapshot := RelayPassiveMonitorSnapshot()
	require.Len(t, snapshot.Series, 1)
	assert.Equal(t, relayPassiveKindPreRouteFailure, snapshot.Series[0].Kind)
	assert.Equal(t, "2048", snapshot.Series[0].Resolution)
	assert.Equal(t, "high", snapshot.Series[0].Quality)
}

func TestRelayPassiveMonitorTTLAndCapacityAreBounded(t *testing.T) {
	previousPassive := constant.Image2PassiveMonitorEnabled
	constant.Image2PassiveMonitorEnabled = true
	resetRelayPassiveMonitorForTest()
	t.Cleanup(func() {
		constant.Image2PassiveMonitorEnabled = previousPassive
		resetRelayPassiveMonitorForTest()
	})

	now := time.Now()
	for i := 0; i < maxRelayPassiveSeriesPerKind+5; i++ {
		observeRelayPassiveAt(relayPassiveKey{
			Kind:       relayPassiveKindPreRouteFailure,
			Model:      "model-" + strconv.Itoa(i) + "-" + strings.Repeat("x", 100) + "\nsecret",
			Group:      "image2",
			Operation:  "generations",
			Resolution: "1024",
		}, time.Second, now)
	}
	// A saturated kind must not starve another fixed event family.
	observeRelayPassiveAt(relayPassiveKey{Kind: relayPassiveKindGatewayTimeout, StatusCode: 504, UpstreamCalled: true}, 0, now)

	snapshot := RelayPassiveMonitorSnapshot()
	assert.Len(t, snapshot.Series, maxRelayPassiveSeriesPerKind+1)
	assert.Equal(t, uint64(5), snapshot.OverflowByKind[relayPassiveKindPreRouteFailure])
	for _, series := range snapshot.Series {
		assert.LessOrEqual(t, len([]rune(series.Model)), maxRelayPassiveDimensionRunes+len([]rune("...[truncated]")))
		assert.NotContains(t, series.Model, "\n")
		assert.NotContains(t, series.Model, "secret")
	}

	// Insert an already-expired sample; the snapshot must prune it by TTL.
	observeRelayPassiveAt(relayPassiveKey{Kind: relayPassiveKindSlowImageSuccess, Model: "expired"}, 0, now.Add(-relayPassiveSeriesTTL-time.Minute))
	pruned := RelayPassiveMonitorSnapshot()
	for _, series := range pruned.Series {
		assert.NotEqual(t, "expired", series.Model)
	}
}

func TestRelayPassiveMonitorConcurrentAnd504AreObservationOnly(t *testing.T) {
	previousPassive := constant.Image2PassiveMonitorEnabled
	constant.Image2PassiveMonitorEnabled = true
	resetRelayPassiveMonitorForTest()
	t.Cleanup(func() {
		constant.Image2PassiveMonitorEnabled = previousPassive
		resetRelayPassiveMonitorForTest()
	})

	c := relayObservabilityTestContext("/v1/images/generations")
	capability := Image2RequestCapability{Operation: "generations", Resolution: "1024", Quality: "standard", N: 1}
	SetImage2PassiveRequestCapability(c, capability)
	c.Set("channel_id", 44)
	timeoutErr := types.NewErrorWithStatusCode(errors.New("provider response must not be stored"), types.ErrorCodeBadResponseStatusCode, http.StatusGatewayTimeout)
	ObserveImage2UpstreamError(c, timeoutErr, 44)
	ObserveImage2UpstreamError(c, timeoutErr, 44)
	assert.Empty(t, c.GetStringSlice("use_channel"), "observer must not add or select a channel")

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			observeRelayPassive(relayPassiveKey{
				Kind:       relayPassiveKindSlowImageSuccess,
				Model:      "gpt-image-2",
				Group:      "image2",
				Operation:  "generations",
				Resolution: "1024",
				Quality:    "standard",
				N:          uint(i%2 + 1),
			}, time.Millisecond)
		}(i)
	}
	wg.Wait()

	snapshot := RelayPassiveMonitorSnapshot()
	var timeoutFound bool
	for _, series := range snapshot.Series {
		if series.Kind == relayPassiveKindGatewayTimeout {
			timeoutFound = true
			assert.Equal(t, uint64(2), series.Count)
			assert.Equal(t, http.StatusGatewayTimeout, series.StatusCode)
			assert.NotContains(t, series.ErrorCode, "provider response")
		}
	}
	assert.True(t, timeoutFound)
}
