package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestSecureSkillFailedPollCASRefundsOnceAndNeverPostsAgain(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 80, 80, 80
	const initialQuota, preConsumed, tokenRemain = 10000, 3000, 5000

	seedUser(t, userID, initialQuota)
	seedToken(t, tokenID, userID, "sk-h3-polling", tokenRemain)

	var mu sync.Mutex
	createPosts := 0
	statusGets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos":
			createPosts++
			_, _ = io.WriteString(w, `{"id":"h3-poll-failure","status":"queued"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/h3-poll-failure":
			statusGets++
			_, _ = io.WriteString(w, `{"id":"h3-poll-failure","status":"failed","error":{"message":"provider rejected task"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	baseURL := server.URL
	channel := &model.Channel{
		Id:      channelID,
		Type:    constant.ChannelTypeSecureSkill,
		Key:     "channel-fallback-key",
		BaseURL: &baseURL,
		Status:  common.ChannelStatusEnabled,
		Name:    "SecureSkill H3 fake",
		Models:  "minimax-h3",
	}
	require.NoError(t, model.DB.Create(channel).Error)

	// Establish exactly one successful creation against the fake upstream.
	createResp, err := http.Post(server.URL+"/v1/videos", "application/json", strings.NewReader(`{"model":"minimax-h3"}`))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, createResp.Body)
	_ = createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task_h3_poll_failure"
	task.Status = model.TaskStatusInProgress
	task.PrivateData.UpstreamTaskID = "h3-poll-failure"
	require.NoError(t, model.DB.Create(task).Error)
	taskM := map[string]*model.Task{task.GetUpstreamTaskID(): task}

	adaptor := &fakeSecureSkillPollingAdaptor{}
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType:    constant.ChannelTypeSecureSkill,
		ChannelBaseUrl: server.URL,
		ApiKey:         "selected-runtime-key",
	}})

	ctx := context.Background()
	require.NoError(t, updateVideoSingleTask(ctx, adaptor, channel, task.GetUpstreamTaskID(), taskM))
	firstRefundedQuota := getUserQuota(t, userID)
	firstRefundedTokenQuota := getTokenRemainQuota(t, tokenID)
	firstRefundLogCount := countLogs(t)
	require.Equal(t, initialQuota+preConsumed, firstRefundedQuota)
	require.Equal(t, tokenRemain+preConsumed, firstRefundedTokenQuota)
	require.Equal(t, int64(1), firstRefundLogCount)

	// A duplicate/stale worker sees the terminal H3 state and must not issue
	// another GET, POST, or refund.
	require.NoError(t, updateVideoSingleTask(ctx, adaptor, channel, task.GetUpstreamTaskID(), taskM))
	require.Equal(t, firstRefundedQuota, getUserQuota(t, userID))
	require.Equal(t, firstRefundedTokenQuota, getTokenRemainQuota(t, tokenID))
	require.Equal(t, firstRefundLogCount, countLogs(t))
	var persisted model.Task
	require.NoError(t, model.DB.First(&persisted, task.ID).Error)
	require.Equal(t, model.TaskStatus(model.TaskStatusFailure), persisted.Status)

	mu.Lock()
	defer mu.Unlock()
	if createPosts != 1 {
		t.Fatalf("fake upstream POST count = %d, want exactly one creation", createPosts)
	}
	if statusGets != 1 {
		t.Fatalf("fake upstream GET count = %d, want one status poll before terminal guard", statusGets)
	}
}

func TestSecureSkillTerminalStatusCannotFlipOrPoll(t *testing.T) {
	for _, initial := range []model.TaskStatus{model.TaskStatusSuccess, model.TaskStatusFailure} {
		t.Run(string(initial), func(t *testing.T) {
			adaptor := &terminalGuardPollingAdaptor{}
			baseURL := "http://127.0.0.1:1"
			channel := &model.Channel{Type: constant.ChannelTypeSecureSkill, BaseURL: &baseURL}
			task := &model.Task{TaskID: "task_terminal_" + strings.ToLower(string(initial)), Status: initial}
			taskM := map[string]*model.Task{task.GetUpstreamTaskID(): task}

			require.NoError(t, updateVideoSingleTask(context.Background(), adaptor, channel, task.GetUpstreamTaskID(), taskM))
			require.Equal(t, 0, adaptor.fetches, "a persisted H3 terminal task must not be polled again")
			require.Equal(t, initial, task.Status, "H3 terminal state must be monotonic")
		})
	}
}

func TestSecureSkillStalePollCannotFlipPersistedTerminalStatus(t *testing.T) {
	for _, tt := range []struct {
		name            string
		persistedStatus model.TaskStatus
		providerStatus  model.TaskStatus
	}{
		{name: "success cannot become failure", persistedStatus: model.TaskStatusSuccess, providerStatus: model.TaskStatusFailure},
		{name: "failure cannot become success", persistedStatus: model.TaskStatusFailure, providerStatus: model.TaskStatusSuccess},
	} {
		t.Run(tt.name, func(t *testing.T) {
			truncate(t)
			const userID, tokenID, channelID = 440, 441, 442
			const initialQuota, tokenRemain = 10000, 9000
			seedUser(t, userID, initialQuota)
			seedToken(t, tokenID, userID, "sk-h3-stale", tokenRemain)
			baseURL := "http://127.0.0.1:1"
			channel := &model.Channel{
				Id: channelID, Type: constant.ChannelTypeSecureSkill, Key: "channel-key", BaseURL: &baseURL,
				Status: common.ChannelStatusEnabled, Name: "SecureSkill stale fake",
			}
			task := makeTask(userID, channelID, 3000, tokenID, BillingSourceWallet, 0)
			task.TaskID = "task_h3_stale_" + strings.ReplaceAll(tt.name, " ", "_")
			task.Status = tt.persistedStatus
			task.PrivateData.UpstreamTaskID = "upstream-h3-stale"
			require.NoError(t, model.DB.Create(channel).Error)
			require.NoError(t, model.DB.Create(task).Error)

			adaptor := &fixedStatusPollingAdaptor{status: tt.providerStatus}
			staleTask := *task
			staleTask.Status = model.TaskStatusInProgress
			taskM := map[string]*model.Task{staleTask.GetUpstreamTaskID(): &staleTask}
			require.NoError(t, updateVideoSingleTask(context.Background(), adaptor, channel, staleTask.GetUpstreamTaskID(), taskM))
			require.Equal(t, 1, adaptor.fetches, "a stale worker may perform one read-only status fetch")
			require.Equal(t, initialQuota, getUserQuota(t, userID), "CAS loss must not refund or settle stale task")
			require.Equal(t, tokenRemain, getTokenRemainQuota(t, tokenID))
			require.Equal(t, int64(0), countLogs(t))
			var persisted model.Task
			require.NoError(t, model.DB.First(&persisted, task.ID).Error)
			require.Equal(t, tt.persistedStatus, persisted.Status, "a terminal DB state must be monotonic")
		})
	}
}

func TestNonSecureSkillVideoFailureStillRefundsOnFirstTerminalPoll(t *testing.T) {
	for _, channelType := range []int{constant.ChannelTypeOpenAI, constant.ChannelTypeSora, constant.ChannelTypeDoubaoVideo} {
		t.Run(strconv.Itoa(channelType), func(t *testing.T) {
			truncate(t)
			userID := 180 + channelType
			tokenID := 280 + channelType
			channelID := 380 + channelType
			const initialQuota, preConsumed, tokenRemain = 10000, 3000, 5000
			seedUser(t, userID, initialQuota)
			seedToken(t, tokenID, userID, "sk-non-h3-"+strconv.Itoa(channelType), tokenRemain)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = io.WriteString(w, `{"id":"upstream","status":"failed","error":{"message":"legacy provider rejected task"}}`)
			}))
			defer server.Close()
			baseURL := server.URL
			channel := &model.Channel{
				Id: channelID, Type: channelType, Key: "channel-key", BaseURL: &baseURL,
				Status: common.ChannelStatusEnabled, Name: "legacy video fake",
			}
			task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
			task.TaskID = "task_non_h3_failure_" + strconv.Itoa(channelType)
			task.PrivateData.UpstreamTaskID = "upstream_non_h3_failure_" + strconv.Itoa(channelType)
			require.NoError(t, model.DB.Create(channel).Error)
			require.NoError(t, model.DB.Create(task).Error)

			adaptor := &fakeSecureSkillPollingAdaptor{}
			adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType: channelType, ChannelBaseUrl: baseURL, ApiKey: "selected-runtime-key",
			}})
			taskM := map[string]*model.Task{task.GetUpstreamTaskID(): task}
			require.NoError(t, updateVideoSingleTask(context.Background(), adaptor, channel, task.GetUpstreamTaskID(), taskM))
			require.Equal(t, initialQuota+preConsumed, getUserQuota(t, userID))
			require.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
			require.Equal(t, int64(1), countLogs(t), "existing non-SecureSkill video paths must retain first-failure refund")
			require.Equal(t, model.TaskStatus(model.TaskStatusFailure), task.Status)
		})
	}
}

type terminalGuardPollingAdaptor struct {
	fetches int
}

type fixedStatusPollingAdaptor struct {
	status  model.TaskStatus
	fetches int
}

func (a *fixedStatusPollingAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a *fixedStatusPollingAdaptor) FetchTask(_ string, _ string, _ map[string]any, _ string) (*http.Response, error) {
	a.fetches++
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"status":"provider"}`)),
	}, nil
}

func (a *fixedStatusPollingAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return &relaycommon.TaskInfo{Status: string(a.status)}, nil
}

func (a *fixedStatusPollingAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

func (a *terminalGuardPollingAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a *terminalGuardPollingAdaptor) FetchTask(string, string, map[string]any, string) (*http.Response, error) {
	a.fetches++
	return nil, io.ErrUnexpectedEOF
}

func (a *terminalGuardPollingAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return &relaycommon.TaskInfo{Status: model.TaskStatusFailure}, nil
}

func (a *terminalGuardPollingAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

// fakeSecureSkillPollingAdaptor keeps this service-package test independent
// from relay/channel/task/secureskill (which imports service for HTTP clients)
// while preserving the real polling contract used by updateVideoSingleTask.
type fakeSecureSkillPollingAdaptor struct{}

func (f *fakeSecureSkillPollingAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (f *fakeSecureSkillPollingAdaptor) FetchTask(baseURL, key string, body map[string]any, _ string) (*http.Response, error) {
	taskID, _ := body["task_id"].(string)
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/videos/"+taskID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	return http.DefaultClient.Do(req)
}

func (f *fakeSecureSkillPollingAdaptor) ParseTaskResult(body []byte) (*relaycommon.TaskInfo, error) {
	var response struct {
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := common.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	result := &relaycommon.TaskInfo{Status: response.Status}
	if response.Status == string(model.TaskStatusFailure) || response.Status == "failed" {
		result.Status = string(model.TaskStatusFailure)
		if response.Error != nil {
			result.Reason = response.Error.Message
		}
	}
	return result, nil
}

func (f *fakeSecureSkillPollingAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}
