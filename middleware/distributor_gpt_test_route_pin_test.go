package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func gptTestRoutePinNow() time.Time {
	return time.Date(2026, time.August, 13, 1, 0, 0, 0, time.FixedZone("CST", 8*60*60))
}

func setGPTTestRoutePinEnv(t *testing.T, enabled, tokenID, channelID, surface string, until time.Time) {
	t.Helper()
	t.Setenv(gptTestRoutePinEnabledEnv, enabled)
	t.Setenv(gptTestRoutePinTokenIDEnv, tokenID)
	t.Setenv(gptTestRoutePinChannelIDEnv, channelID)
	t.Setenv(gptTestRoutePinSurfaceEnv, surface)
	if until.IsZero() {
		t.Setenv(gptTestRoutePinUntilEnv, "")
	} else {
		t.Setenv(gptTestRoutePinUntilEnv, until.Format(time.RFC3339))
	}
}

func newGPTTestRoutePinContext(t *testing.T, path, remoteAddr, body string) *gin.Context {
	t.Helper()
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	ctx.Request.RemoteAddr = remoteAddr
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("id", gptTestRoutePinUserID)
	ctx.Set("token_id", 730055)
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, gptTestRoutePinGroup)
	return ctx
}

func TestGPTTestRoutePinFailsClosed(t *testing.T) {
	now := gptTestRoutePinNow()
	until := now.Add(time.Hour)
	setGPTTestRoutePinEnv(t, "true", "730055", "70", "loopback", until)

	for _, tc := range []struct {
		name, path, remote, body, group string
		token, user                     int
		master                          bool
		want                            bool
	}{
		{"exact-loopback-chat-pins", "/v1/chat/completions", "127.0.0.1:3100", `{"model":"gpt-5.5"}`, gptTestRoutePinGroup, 730055, gptTestRoutePinUserID, false, true},
		{"exact-loopback-responses-pins", "/v1/responses", "[::1]:3100", `{"model":"gpt-5.5"}`, gptTestRoutePinGroup, 730055, gptTestRoutePinUserID, false, true},
		{"public-remote-does-not-pin", "/v1/chat/completions", "203.0.113.9:3100", `{"model":"gpt-5.5"}`, gptTestRoutePinGroup, 730055, gptTestRoutePinUserID, false, false},
		{"wrong-model-does-not-pin", "/v1/chat/completions", "127.0.0.1:3100", `{"model":"gpt-5.4"}`, gptTestRoutePinGroup, 730055, gptTestRoutePinUserID, false, false},
		{"wrong-group-does-not-pin", "/v1/chat/completions", "127.0.0.1:3100", `{"model":"gpt-5.5"}`, "gpt满血版 1x", 730055, gptTestRoutePinUserID, false, false},
		{"wrong-token-does-not-pin", "/v1/chat/completions", "127.0.0.1:3100", `{"model":"gpt-5.5"}`, gptTestRoutePinGroup, 730056, gptTestRoutePinUserID, false, false},
		{"master-does-not-pin", "/v1/chat/completions", "127.0.0.1:3100", `{"model":"gpt-5.5"}`, gptTestRoutePinGroup, 730055, gptTestRoutePinUserID, true, false},
		{"other-endpoint-does-not-pin", "/v1/images/generations", "127.0.0.1:3100", `{"model":"gpt-5.5"}`, gptTestRoutePinGroup, 730055, gptTestRoutePinUserID, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldMaster := common.IsMasterNode
			common.IsMasterNode = tc.master
			t.Cleanup(func() { common.IsMasterNode = oldMaster })
			ctx := newGPTTestRoutePinContext(t, tc.path, tc.remote, tc.body)
			ctx.Set("id", tc.user)
			ctx.Set("token_id", tc.token)
			common.SetContextKey(ctx, constant.ContextKeyUsingGroup, tc.group)
			ctx.Request.Header.Set("X-Channel-ID", "70")
			ctx.Request.Header.Set("X-Forwarded-For", "127.0.0.1")
			request, _, err := getModelRequest(ctx)
			require.NoError(t, err)
			got := maybeSetGPTTestRoutePin(ctx, request, now)
			require.Equal(t, tc.want, got)
			channelID, exists := common.GetContextKey(ctx, constant.ContextKeyTokenSpecificChannelId)
			if tc.want {
				require.True(t, exists)
				require.Equal(t, "70", channelID)
				require.True(t, ctx.GetBool("gpt_test_route_pin_active"))
			} else {
				require.False(t, exists)
				require.False(t, ctx.GetBool("gpt_test_route_pin_active"))
			}
		})
	}
}

func TestGPTTestRoutePinMoniSurfacePinsOnlyExactTestIdentity(t *testing.T) {
	now := gptTestRoutePinNow()
	setGPTTestRoutePinEnv(t, "true", "730055", "70", "moni", now.Add(time.Hour))

	for _, tc := range []struct {
		name, remote, group, model string
		token, user                int
		master                     bool
		want                       bool
	}{
		{"moni-safari-path-pins-on-master", "203.0.113.9:3100", gptTestRoutePinGroup, "gpt-5.5", 730055, gptTestRoutePinUserID, true, true},
		{"moni-safari-path-pins-on-slave", "203.0.113.9:3100", gptTestRoutePinGroup, "gpt-5.5", 730055, gptTestRoutePinUserID, false, true},
		{"wrong-token-does-not-pin", "203.0.113.9:3100", gptTestRoutePinGroup, "gpt-5.5", 730056, gptTestRoutePinUserID, true, false},
		{"wrong-user-does-not-pin", "203.0.113.9:3100", gptTestRoutePinGroup, "gpt-5.5", 730055, 27, true, false},
		{"wrong-group-does-not-pin", "203.0.113.9:3100", "gpt满血版 1x", "gpt-5.5", 730055, gptTestRoutePinUserID, true, false},
		{"wrong-model-does-not-pin", "203.0.113.9:3100", gptTestRoutePinGroup, "gpt-5.4", 730055, gptTestRoutePinUserID, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldMaster := common.IsMasterNode
			common.IsMasterNode = tc.master
			t.Cleanup(func() { common.IsMasterNode = oldMaster })
			ctx := newGPTTestRoutePinContext(t, "/v1/chat/completions", tc.remote, `{"model":"`+tc.model+`"}`)
			ctx.Set("id", tc.user)
			ctx.Set("token_id", tc.token)
			common.SetContextKey(ctx, constant.ContextKeyUsingGroup, tc.group)
			request, _, err := getModelRequest(ctx)
			require.NoError(t, err)
			got := maybeSetGPTTestRoutePin(ctx, request, now)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestGPTTestRoutePinConfigFailsClosed(t *testing.T) {
	now := gptTestRoutePinNow()
	for _, tc := range []struct {
		name, enabled, tokenID, channelID, surface string
		until                                      string
		want                                       bool
	}{
		{"complete-loopback", "true", "730055", "70", "loopback", now.Add(time.Hour).Format(time.RFC3339), true},
		{"complete-moni", "true", "730055", "70", "moni", now.Add(time.Hour).Format(time.RFC3339), true},
		{"missing-token", "true", "", "70", "loopback", now.Add(time.Hour).Format(time.RFC3339), false},
		{"missing-channel", "true", "730055", "", "loopback", now.Add(time.Hour).Format(time.RFC3339), false},
		{"multiple-channel", "true", "730055", "70,73", "loopback", now.Add(time.Hour).Format(time.RFC3339), false},
		{"bad-surface", "true", "730055", "70", "public", now.Add(time.Hour).Format(time.RFC3339), false},
		{"expired", "true", "730055", "70", "loopback", now.Add(-time.Second).Format(time.RFC3339), false},
		{"future-too-long", "true", "730055", "70", "loopback", now.Add(2*time.Hour + time.Second).Format(time.RFC3339), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(gptTestRoutePinEnabledEnv, tc.enabled)
			t.Setenv(gptTestRoutePinTokenIDEnv, tc.tokenID)
			t.Setenv(gptTestRoutePinChannelIDEnv, tc.channelID)
			t.Setenv(gptTestRoutePinSurfaceEnv, tc.surface)
			t.Setenv(gptTestRoutePinUntilEnv, tc.until)
			_, ok := gptTestRoutePinConfigFromEnv(now)
			require.Equal(t, tc.want, ok)
		})
	}
}
