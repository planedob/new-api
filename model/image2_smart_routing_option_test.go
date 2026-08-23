package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUpdateOptionMapImage2SmartRoutingIsImmediateAndFailClosed(t *testing.T) {
	originalEnabled := common.GetImage2SmartRoutingEnabled()
	originalOptions := common.OptionMap
	common.OptionMap = map[string]string{}
	t.Cleanup(func() {
		common.SetImage2SmartRoutingEnabled(originalEnabled)
		common.OptionMap = originalOptions
	})

	require.NoError(t, updateOptionMap(common.Image2SmartRoutingOptionKey, "true"))
	assert.True(t, common.GetImage2SmartRoutingEnabled())
	assert.Equal(t, "true", common.OptionMap[common.Image2SmartRoutingOptionKey])

	require.Error(t, updateOptionMap(common.Image2SmartRoutingOptionKey, "not-a-boolean"))
	assert.False(t, common.GetImage2SmartRoutingEnabled(), "invalid persisted state must disable routing")
}

func TestLoadOptionsImage2SmartRoutingDatabaseOverridesEnvironment(t *testing.T) {
	originalDB := DB
	originalEnabled := common.GetImage2SmartRoutingEnabled()
	originalEnv := common.Image2SmartRoutingEnvEnabled
	originalOptions := common.OptionMap
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	common.OptionMap = map[string]string{}
	common.Image2SmartRoutingEnvEnabled = false
	common.SetImage2SmartRoutingEnabled(false)
	t.Cleanup(func() {
		DB = originalDB
		common.Image2SmartRoutingEnvEnabled = originalEnv
		common.SetImage2SmartRoutingEnabled(originalEnabled)
		common.OptionMap = originalOptions
	})

	require.NoError(t, db.Create(&Option{Key: common.Image2SmartRoutingOptionKey, Value: "true"}).Error)
	loadOptionsFromDatabase()
	assert.True(t, common.GetImage2SmartRoutingEnabled(), "database true must override disabled environment fallback")

	require.NoError(t, db.Delete(&Option{}, "key = ?", common.Image2SmartRoutingOptionKey).Error)
	common.Image2SmartRoutingEnvEnabled = true
	loadOptionsFromDatabase()
	assert.True(t, common.GetImage2SmartRoutingEnabled(), "absent database row must restore environment fallback")

	require.NoError(t, db.Create(&Option{Key: common.Image2SmartRoutingOptionKey, Value: "malformed"}).Error)
	loadOptionsFromDatabase()
	assert.False(t, common.GetImage2SmartRoutingEnabled(), "malformed database value must fail closed")
}

func TestLoadOptionsImage2SmartRoutingDatabaseReadFailureFailsClosed(t *testing.T) {
	originalDB := DB
	originalEnabled := common.GetImage2SmartRoutingEnabled()
	originalEnv := common.Image2SmartRoutingEnvEnabled
	originalOptions := common.OptionMap
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	DB = db // Deliberately do not migrate Option, so AllOption returns an error.
	common.OptionMap = map[string]string{}
	common.Image2SmartRoutingEnvEnabled = true
	common.SetImage2SmartRoutingEnabled(true)
	t.Cleanup(func() {
		DB = originalDB
		common.Image2SmartRoutingEnvEnabled = originalEnv
		common.SetImage2SmartRoutingEnabled(originalEnabled)
		common.OptionMap = originalOptions
	})

	loadOptionsFromDatabase()
	assert.False(t, common.GetImage2SmartRoutingEnabled(), "database read failure must disable routing")
	assert.Equal(t, "false", common.OptionMap[common.Image2SmartRoutingOptionKey])
}
