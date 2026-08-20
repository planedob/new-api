package model

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	commonRelay "github.com/QuantumNous/new-api/relay/common"
)

func TestInitTaskSnapshotsSecureSkillSelectedKey(t *testing.T) {
	info := &commonRelay.RelayInfo{
		UserId: 7,
		ChannelMeta: &commonRelay.ChannelMeta{
			ChannelType: constant.ChannelTypeSecureSkill,
			ChannelId:   11,
			ApiKey:      "selected-runtime-key",
		},
		TaskRelayInfo: &commonRelay.TaskRelayInfo{PublicTaskID: "task_h3_test"},
	}

	task := InitTask(constant.TaskPlatform("video"), info)
	if task.PrivateData.Key != "selected-runtime-key" {
		t.Fatalf("stored task key = %q, want selected runtime key", task.PrivateData.Key)
	}
}
