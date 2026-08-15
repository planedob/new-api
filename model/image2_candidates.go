package model

import (
	"fmt"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// GetSatisfiedChannels returns every enabled channel bound to a group/model.
// Unlike the normal selector it does not apply channel priority or weight: a
// caller that needs capability-based routing must see the complete candidate
// set before it makes its own deterministic decision.
func GetSatisfiedChannels(group string, modelName string) ([]*Channel, error) {
	if common.MemoryCacheEnabled {
		channelSyncLock.RLock()
		defer channelSyncLock.RUnlock()
		ids := group2model2channels[group][modelName]
		if len(ids) == 0 {
			ids = group2model2channels[group][ratio_setting.FormatMatchingModelName(modelName)]
		}
		channels := make([]*Channel, 0, len(ids))
		for _, id := range ids {
			channel, ok := channelsIDM[id]
			if !ok {
				return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", id)
			}
			channels = append(channels, channel)
		}
		return channels, nil
	}

	var abilities []Ability
	if err := DB.Where(commonGroupCol+" = ? and model = ? and enabled = ?", group, modelName, true).
		Order("priority DESC").Order("weight DESC").Find(&abilities).Error; err != nil {
		return nil, err
	}
	channels := make([]*Channel, 0, len(abilities))
	seen := make(map[int]struct{}, len(abilities))
	for _, ability := range abilities {
		if _, exists := seen[ability.ChannelId]; exists {
			continue
		}
		var channel Channel
		if err := DB.Where("id = ? and status = ?", ability.ChannelId, common.ChannelStatusEnabled).First(&channel).Error; err != nil {
			continue
		}
		seen[channel.Id] = struct{}{}
		channels = append(channels, &channel)
	}
	// Keep a stable fallback order for callers that do not use every field.
	sort.SliceStable(channels, func(i, j int) bool { return channels[i].Id < channels[j].Id })
	return channels, nil
}

// GetImage2Channels returns all channels bound to the requested group/model,
// including ability rows and channel rows that are currently disabled. Image2
// routing needs this wider view to distinguish a valid but temporarily
// unavailable capability (503, no upstream call) from a request combination
// that no channel supports (422). The normal selector must keep using
// GetSatisfiedChannels, which intentionally excludes disabled channels.
//
// This query is deliberately kept separate from the legacy cache path: the
// in-memory group2model2channels index only contains enabled channels and
// therefore cannot provide the distinction required by the Image2 error
// contract without silently treating disabled capabilities as unsupported.
func GetImage2Channels(group string, modelName string) ([]*Channel, error) {
	if DB == nil {
		return nil, fmt.Errorf("database is not initialized")
	}
	var abilities []Ability
	// Do not filter abilities by enabled here. UpdateChannelStatus mirrors a
	// channel's runtime state into the ability row, so filtering it would erase
	// the only evidence that a compatible capability exists but is temporarily
	// unavailable. The smart router performs the status check after loading the
	// capability declaration.
	query := DB.Where(commonGroupCol+" = ? and model = ?", group, modelName).
		Order("priority DESC").Order("weight DESC")
	if err := query.Find(&abilities).Error; err != nil {
		return nil, err
	}
	enabledByChannel := make(map[int]bool, len(abilities))
	for _, ability := range abilities {
		if ability.Enabled {
			enabledByChannel[ability.ChannelId] = true
		}
	}
	channels := make([]*Channel, 0, len(abilities))
	seen := make(map[int]struct{}, len(abilities))
	for _, ability := range abilities {
		if _, exists := seen[ability.ChannelId]; exists {
			continue
		}
		var channel Channel
		if err := DB.Where("id = ?", ability.ChannelId).First(&channel).Error; err != nil {
			continue
		}
		if !enabledByChannel[channel.Id] && channel.Status == common.ChannelStatusEnabled {
			// Ability.Enabled is the per-group/model availability mirror. Keep the
			// capability visible, but mark it as temporarily unavailable so the
			// smart router emits 503 instead of accidentally selecting it.
			channel.Status = common.ChannelStatusAutoDisabled
		}
		seen[channel.Id] = struct{}{}
		channels = append(channels, &channel)
	}
	// Keep deterministic ordering; capability route priority is applied later
	// by service/image2_smart_router.go.
	sort.SliceStable(channels, func(i, j int) bool { return channels[i].Id < channels[j].Id })
	return channels, nil
}
