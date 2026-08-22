package service

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const (
	relayPassiveKindPreRouteFailure  = "pre_route_failure"
	relayPassiveKindGatewayTimeout   = "upstream_gateway_timeout"
	relayPassiveKindSlowImageSuccess = "slow_image_success"

	// Keep each event family bounded independently so a high-cardinality model
	// or group cannot starve timeout or slow-success signals.
	maxRelayPassiveSeriesPerKind  = 24
	maxRelayPassiveDimensionRunes = 64
	relayPassiveSeriesTTL         = 24 * time.Hour
	relayPassiveElapsedCap        = 24 * time.Hour
	relayPassiveSlowImage         = 60 * time.Second
)

const relayPassiveImage2CapabilityContextKey = "relay_passive_image2_capability"

type relayPassiveKey struct {
	Kind           string
	Model          string
	Group          string
	Operation      string
	Resolution     string
	Quality        string
	N              uint
	ErrorCode      string
	ChannelID      int
	StatusCode     int
	UpstreamCalled bool
}

type relayPassiveCounters struct {
	Count          uint64
	TotalElapsedMs int64
	MaxElapsedMs   int64
	LastSeen       time.Time
}

// RelayPassiveSeries contains only fixed, aggregation-safe dimensions. It
// deliberately excludes request IDs, URLs, prompts, files, keys, tokens and
// provider response bodies.
type RelayPassiveSeries struct {
	Kind           string `json:"kind"`
	Model          string `json:"model,omitempty"`
	Group          string `json:"group,omitempty"`
	Operation      string `json:"operation,omitempty"`
	Resolution     string `json:"resolution,omitempty"`
	Quality        string `json:"quality,omitempty"`
	N              uint   `json:"n,omitempty"`
	ErrorCode      string `json:"error_code,omitempty"`
	ChannelID      int    `json:"channel_id"`
	StatusCode     int    `json:"status_code"`
	UpstreamCalled bool   `json:"upstream_called"`
	Count          uint64 `json:"count"`
	TotalElapsedMs int64  `json:"total_elapsed_ms"`
	MaxElapsedMs   int64  `json:"max_elapsed_ms"`
	AvgElapsedMs   int64  `json:"avg_elapsed_ms"`
}

type RelayPassiveSnapshot struct {
	Enabled        bool                 `json:"enabled"`
	StartedAt      int64                `json:"started_at"`
	GeneratedAt    int64                `json:"generated_at"`
	Series         []RelayPassiveSeries `json:"series"`
	OverflowByKind map[string]uint64    `json:"overflow_by_kind"`
}

var relayPassiveMonitor = struct {
	sync.Mutex
	series         map[relayPassiveKey]relayPassiveCounters
	overflowByKind map[string]uint64
	startedAt      time.Time
}{
	series:         make(map[relayPassiveKey]relayPassiveCounters),
	overflowByKind: make(map[string]uint64),
	startedAt:      time.Now(),
}

func boundedRelayPassiveDimension(value string) string {
	return boundedRelayErrorLogText(value, maxRelayPassiveDimensionRunes)
}

func relayPassiveElapsed(c *gin.Context) time.Duration {
	if c == nil {
		return 0
	}
	startedAt := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
	if startedAt.IsZero() {
		return 0
	}
	elapsed := time.Since(startedAt)
	if elapsed < 0 {
		return 0
	}
	if elapsed > relayPassiveElapsedCap {
		return relayPassiveElapsedCap
	}
	return elapsed
}

func setImage2PassiveCapability(c *gin.Context, capability Image2RequestCapability) {
	if c != nil {
		c.Set(relayPassiveImage2CapabilityContextKey, capability)
	}
}

// SetImage2PassiveRequestCapability stores only the normalized request shape
// for later passive dimensions. It never stores request bodies or URLs.
func SetImage2PassiveRequestCapability(c *gin.Context, capability Image2RequestCapability) {
	setImage2PassiveCapability(c, capability)
}

func image2PassiveCapability(c *gin.Context) (Image2RequestCapability, bool) {
	if c == nil {
		return Image2RequestCapability{}, false
	}
	value, ok := c.Get(relayPassiveImage2CapabilityContextKey)
	if !ok {
		return Image2RequestCapability{}, false
	}
	switch capability := value.(type) {
	case Image2RequestCapability:
		return capability, true
	case *Image2RequestCapability:
		if capability != nil {
			return *capability, true
		}
	}
	return Image2RequestCapability{}, false
}

func normalizeRelayPassiveKey(key relayPassiveKey) relayPassiveKey {
	key.Model = boundedRelayPassiveDimension(key.Model)
	key.Group = boundedRelayPassiveDimension(key.Group)
	key.Operation = boundedRelayPassiveDimension(strings.ToLower(strings.TrimSpace(key.Operation)))
	key.Resolution = boundedRelayPassiveDimension(strings.ToLower(strings.TrimSpace(key.Resolution)))
	key.Quality = boundedRelayPassiveDimension(strings.ToLower(strings.TrimSpace(key.Quality)))
	key.ErrorCode = boundedRelayPassiveDimension(key.ErrorCode)
	return key
}

func pruneRelayPassiveLocked(now time.Time) {
	for key, counters := range relayPassiveMonitor.series {
		if counters.LastSeen.IsZero() || now.Sub(counters.LastSeen) >= relayPassiveSeriesTTL {
			delete(relayPassiveMonitor.series, key)
		}
	}
}

func observeRelayPassiveAt(key relayPassiveKey, elapsed time.Duration, now time.Time) {
	if !constant.Image2PassiveMonitorEnabled {
		return
	}
	key = normalizeRelayPassiveKey(key)
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed > relayPassiveElapsedCap {
		elapsed = relayPassiveElapsedCap
	}
	elapsedMs := elapsed.Milliseconds()

	relayPassiveMonitor.Lock()
	defer relayPassiveMonitor.Unlock()
	pruneRelayPassiveLocked(now)
	counters, exists := relayPassiveMonitor.series[key]
	if !exists {
		kindSeries := 0
		for existingKey := range relayPassiveMonitor.series {
			if existingKey.Kind == key.Kind {
				kindSeries++
			}
		}
		if kindSeries >= maxRelayPassiveSeriesPerKind {
			relayPassiveMonitor.overflowByKind[key.Kind]++
			return
		}
	}
	if counters.TotalElapsedMs > math.MaxInt64-elapsedMs {
		counters.TotalElapsedMs = math.MaxInt64
	} else {
		counters.TotalElapsedMs += elapsedMs
	}
	counters.Count++
	if elapsedMs > counters.MaxElapsedMs {
		counters.MaxElapsedMs = elapsedMs
	}
	counters.LastSeen = now
	relayPassiveMonitor.series[key] = counters
}

func observeRelayPassive(key relayPassiveKey, elapsed time.Duration) {
	observeRelayPassiveAt(key, elapsed, time.Now())
}

// ObserveImage2PreRouteFailure records a no-candidate event only. It has no
// effect on the caller's error, billing, selector, retry state or response.
func ObserveImage2PreRouteFailure(c *gin.Context, capability Image2RequestCapability, statusCode int, errorCode types.ErrorCode) {
	if c == nil {
		return
	}
	observeRelayPassive(relayPassiveKey{
		Kind:       relayPassiveKindPreRouteFailure,
		Model:      c.GetString("original_model"),
		Group:      c.GetString("group"),
		Operation:  capability.Operation,
		Resolution: capability.Resolution,
		Quality:    capability.Quality,
		N:          capability.N,
		ErrorCode:  string(errorCode),
		StatusCode: statusCode,
	}, relayPassiveElapsed(c))
}

// ObserveImage2UpstreamError only aggregates 504/524. In particular, this
// function never calls a selector or retry helper.
func ObserveImage2UpstreamError(c *gin.Context, err *types.NewAPIError, channelID int) {
	capability, ok := image2PassiveCapability(c)
	if !ok || err == nil || (err.StatusCode != 504 && err.StatusCode != 524) {
		return
	}
	observeRelayPassive(relayPassiveKey{
		Kind:           relayPassiveKindGatewayTimeout,
		Model:          c.GetString("original_model"),
		Group:          c.GetString("group"),
		Operation:      capability.Operation,
		Resolution:     capability.Resolution,
		Quality:        capability.Quality,
		N:              capability.N,
		ErrorCode:      string(err.GetErrorCode()),
		ChannelID:      channelID,
		StatusCode:     err.StatusCode,
		UpstreamCalled: true,
	}, relayPassiveElapsed(c))
}

// ObserveImage2Success records only slow successful Image2 calls. The relay
// already completed the upstream call before invoking this observer.
func ObserveImage2Success(c *gin.Context, statusCode int) {
	capability, ok := image2PassiveCapability(c)
	if !ok || statusCode < 200 || statusCode >= 300 {
		return
	}
	elapsed := relayPassiveElapsed(c)
	if elapsed < relayPassiveSlowImage {
		return
	}
	observeRelayPassive(relayPassiveKey{
		Kind:           relayPassiveKindSlowImageSuccess,
		Model:          c.GetString("original_model"),
		Group:          c.GetString("group"),
		Operation:      capability.Operation,
		Resolution:     capability.Resolution,
		Quality:        capability.Quality,
		N:              capability.N,
		ChannelID:      c.GetInt("channel_id"),
		StatusCode:     statusCode,
		UpstreamCalled: true,
	}, elapsed)
}

func RelayPassiveMonitorSnapshot() RelayPassiveSnapshot {
	relayPassiveMonitor.Lock()
	defer relayPassiveMonitor.Unlock()
	now := time.Now()
	pruneRelayPassiveLocked(now)
	snapshot := RelayPassiveSnapshot{
		Enabled:        constant.Image2PassiveMonitorEnabled,
		StartedAt:      relayPassiveMonitor.startedAt.Unix(),
		GeneratedAt:    now.Unix(),
		Series:         make([]RelayPassiveSeries, 0, len(relayPassiveMonitor.series)),
		OverflowByKind: make(map[string]uint64, len(relayPassiveMonitor.overflowByKind)),
	}
	for key, counters := range relayPassiveMonitor.series {
		average := int64(0)
		if counters.Count > 0 {
			average = counters.TotalElapsedMs / int64(counters.Count)
		}
		snapshot.Series = append(snapshot.Series, RelayPassiveSeries{
			Kind:           key.Kind,
			Model:          key.Model,
			Group:          key.Group,
			Operation:      key.Operation,
			Resolution:     key.Resolution,
			Quality:        key.Quality,
			N:              key.N,
			ErrorCode:      key.ErrorCode,
			ChannelID:      key.ChannelID,
			StatusCode:     key.StatusCode,
			UpstreamCalled: key.UpstreamCalled,
			Count:          counters.Count,
			TotalElapsedMs: counters.TotalElapsedMs,
			MaxElapsedMs:   counters.MaxElapsedMs,
			AvgElapsedMs:   average,
		})
	}
	for kind, count := range relayPassiveMonitor.overflowByKind {
		snapshot.OverflowByKind[kind] = count
	}
	sort.Slice(snapshot.Series, func(i, j int) bool {
		left, right := snapshot.Series[i], snapshot.Series[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Model != right.Model {
			return left.Model < right.Model
		}
		if left.Group != right.Group {
			return left.Group < right.Group
		}
		if left.Operation != right.Operation {
			return left.Operation < right.Operation
		}
		if left.Resolution != right.Resolution {
			return left.Resolution < right.Resolution
		}
		if left.Quality != right.Quality {
			return left.Quality < right.Quality
		}
		if left.StatusCode != right.StatusCode {
			return left.StatusCode < right.StatusCode
		}
		return left.ChannelID < right.ChannelID
	})
	return snapshot
}

func resetRelayPassiveMonitorForTest() {
	relayPassiveMonitor.Lock()
	defer relayPassiveMonitor.Unlock()
	relayPassiveMonitor.series = make(map[relayPassiveKey]relayPassiveCounters)
	relayPassiveMonitor.overflowByKind = make(map[string]uint64)
	relayPassiveMonitor.startedAt = time.Now()
}
