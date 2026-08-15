package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetRandomSatisfiedChannelWithPolicyStopsAtExhaustion(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true

	channelSyncLock.Lock()
	oldGroupMap := group2model2channels
	oldChannels := channelsIDM
	group2model2channels = map[string]map[string][]int{
		"default": {
			"gpt-image-2": {25, 35},
		},
	}
	channelsIDM = map[int]*Channel{
		25: {Id: 25, Priority: common.GetPointer(int64(100)), Weight: common.GetPointer(uint(100))},
		35: {Id: 35, Priority: common.GetPointer(int64(50)), Weight: common.GetPointer(uint(100))},
	}
	channelSyncLock.Unlock()

	t.Cleanup(func() {
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		channelSyncLock.Lock()
		group2model2channels = oldGroupMap
		channelsIDM = oldChannels
		channelSyncLock.Unlock()
	})

	first, err := GetRandomSatisfiedChannelWithPolicy("default", "gpt-image-2", 0, true)
	require.NoError(t, err)
	require.Equal(t, 25, first.Id)

	second, err := GetRandomSatisfiedChannelWithPolicy("default", "gpt-image-2", 1, true)
	require.NoError(t, err)
	require.Equal(t, 35, second.Id)

	exhausted, err := GetRandomSatisfiedChannelWithPolicy("default", "gpt-image-2", 2, true)
	require.NoError(t, err)
	require.Nil(t, exhausted)

	legacy, err := GetRandomSatisfiedChannelWithPolicy("default", "gpt-image-2", 2, false)
	require.NoError(t, err)
	require.Equal(t, 35, legacy.Id)
}

func TestGetRandomSatisfiedChannelWithPolicyStopsAfterSingleCachedChannel(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true

	channelSyncLock.Lock()
	oldGroupMap := group2model2channels
	oldChannels := channelsIDM
	group2model2channels = map[string]map[string][]int{
		"default": {
			"gpt-single": {25},
		},
	}
	channelsIDM = map[int]*Channel{
		25: {Id: 25, Priority: common.GetPointer(int64(100)), Weight: common.GetPointer(uint(100))},
	}
	channelSyncLock.Unlock()

	t.Cleanup(func() {
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		channelSyncLock.Lock()
		group2model2channels = oldGroupMap
		channelsIDM = oldChannels
		channelSyncLock.Unlock()
	})

	first, err := GetRandomSatisfiedChannelWithPolicy("default", "gpt-single", 0, true)
	require.NoError(t, err)
	require.Equal(t, 25, first.Id)

	exhausted, err := GetRandomSatisfiedChannelWithPolicy("default", "gpt-single", 1, true)
	require.NoError(t, err)
	require.Nil(t, exhausted)
}

func TestGetRandomSatisfiedChannelExcludingExhaustsSamePriorityBeforeFallback(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true

	channelSyncLock.Lock()
	oldGroupMap := group2model2channels
	oldChannels := channelsIDM
	group2model2channels = map[string]map[string][]int{
		"default": {
			"gpt-image-2": {25, 35, 36, 40},
		},
	}
	channelsIDM = map[int]*Channel{
		25: {Id: 25, Priority: common.GetPointer(int64(100)), Weight: common.GetPointer(uint(100))},
		35: {Id: 35, Priority: common.GetPointer(int64(100)), Weight: common.GetPointer(uint(100))},
		36: {Id: 36, Priority: common.GetPointer(int64(50)), Weight: common.GetPointer(uint(100))},
		40: {Id: 40, Priority: common.GetPointer(int64(10)), Weight: common.GetPointer(uint(100))},
	}
	channelSyncLock.Unlock()

	t.Cleanup(func() {
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		channelSyncLock.Lock()
		group2model2channels = oldGroupMap
		channelsIDM = oldChannels
		channelSyncLock.Unlock()
	})

	excluded := make(map[int]struct{})
	first, err := GetRandomSatisfiedChannelExcluding("default", "gpt-image-2", excluded)
	require.NoError(t, err)
	require.Contains(t, []int{25, 35}, first.Id)
	excluded[first.Id] = struct{}{}

	second, err := GetRandomSatisfiedChannelExcluding("default", "gpt-image-2", excluded)
	require.NoError(t, err)
	require.Contains(t, []int{25, 35}, second.Id)
	require.NotEqual(t, first.Id, second.Id)
	excluded[second.Id] = struct{}{}

	third, err := GetRandomSatisfiedChannelExcluding("default", "gpt-image-2", excluded)
	require.NoError(t, err)
	require.Equal(t, 36, third.Id)
	excluded[third.Id] = struct{}{}

	fourth, err := GetRandomSatisfiedChannelExcluding("default", "gpt-image-2", excluded)
	require.NoError(t, err)
	require.Equal(t, 40, fourth.Id)
	excluded[fourth.Id] = struct{}{}

	exhausted, err := GetRandomSatisfiedChannelExcluding("default", "gpt-image-2", excluded)
	require.NoError(t, err)
	require.Nil(t, exhausted)
}

func TestCachedLegacySelectorsNeverReturnDisabledChannel(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true

	channelSyncLock.Lock()
	oldGroupMap := group2model2channels
	oldChannels := channelsIDM
	group2model2channels = map[string]map[string][]int{
		"default": {
			"gpt-image-2": {23, 44},
		},
	}
	channelsIDM = map[int]*Channel{
		23: {Id: 23, Status: common.ChannelStatusManuallyDisabled, Priority: common.GetPointer(int64(100)), Weight: common.GetPointer(uint(100))},
		44: {Id: 44, Status: common.ChannelStatusEnabled, Priority: common.GetPointer(int64(50)), Weight: common.GetPointer(uint(100))},
	}
	channelSyncLock.Unlock()

	t.Cleanup(func() {
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		channelSyncLock.Lock()
		group2model2channels = oldGroupMap
		channelsIDM = oldChannels
		channelSyncLock.Unlock()
	})

	selected, err := GetRandomSatisfiedChannelWithPolicy("default", "gpt-image-2", 0, true)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, 44, selected.Id)

	selected, err = GetRandomSatisfiedChannelExcluding("default", "gpt-image-2", nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, 44, selected.Id)
}

func TestCachedLegacySelectorsReturnNoChannelWhenAllCandidatesDisabled(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true

	channelSyncLock.Lock()
	oldGroupMap := group2model2channels
	oldChannels := channelsIDM
	group2model2channels = map[string]map[string][]int{
		"default": {
			"gpt-image-2": {23},
		},
	}
	channelsIDM = map[int]*Channel{
		23: {Id: 23, Status: common.ChannelStatusAutoDisabled, Priority: common.GetPointer(int64(100)), Weight: common.GetPointer(uint(100))},
	}
	channelSyncLock.Unlock()

	t.Cleanup(func() {
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		channelSyncLock.Lock()
		group2model2channels = oldGroupMap
		channelsIDM = oldChannels
		channelSyncLock.Unlock()
	})

	selected, err := GetRandomSatisfiedChannelWithPolicy("default", "gpt-image-2", 0, true)
	require.NoError(t, err)
	require.Nil(t, selected)

	selected, err = GetRandomSatisfiedChannelExcluding("default", "gpt-image-2", nil)
	require.NoError(t, err)
	require.Nil(t, selected)
}

func TestInitChannelCacheExcludesDisabledAbility(t *testing.T) {
	oldMemoryCacheEnabled := common.MemoryCacheEnabled
	oldUsingSQLite := common.UsingSQLite
	oldDB := DB
	oldGroupMap := group2model2channels
	oldChannels := channelsIDM
	common.MemoryCacheEnabled = true
	common.UsingSQLite = true
	initCol()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	DB = db

	priority := int64(100)
	weight := uint(100)
	require.NoError(t, db.Create(&Channel{Id: 23, Status: common.ChannelStatusEnabled, Models: "gpt-image-2", Group: "default", Priority: &priority, Weight: &weight}).Error)
	require.NoError(t, db.Create(&Channel{Id: 44, Status: common.ChannelStatusEnabled, Models: "gpt-image-2", Group: "default", Priority: &priority, Weight: &weight}).Error)
	require.NoError(t, db.Create(&Ability{Group: "default", Model: "gpt-image-2", ChannelId: 23, Enabled: false, Priority: &priority, Weight: weight}).Error)
	require.NoError(t, db.Create(&Ability{Group: "default", Model: "gpt-image-2", ChannelId: 44, Enabled: true, Priority: &priority, Weight: weight}).Error)

	t.Cleanup(func() {
		common.MemoryCacheEnabled = oldMemoryCacheEnabled
		common.UsingSQLite = oldUsingSQLite
		DB = oldDB
		channelSyncLock.Lock()
		group2model2channels = oldGroupMap
		channelsIDM = oldChannels
		channelSyncLock.Unlock()
	})

	InitChannelCache()
	selected, err := GetRandomSatisfiedChannelWithPolicy("default", "gpt-image-2", 0, true)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, 44, selected.Id)

	// The non-memory legacy path must enforce the channel status boundary too.
	common.MemoryCacheEnabled = false
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", 44).Update("status", common.ChannelStatusManuallyDisabled).Error)
	selected, err = GetChannelWithPolicy("default", "gpt-image-2", 0, true)
	require.NoError(t, err)
	require.Nil(t, selected)
}
