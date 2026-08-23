package common

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveImage2SmartRoutingEnabledUsesDatabaseOverride(t *testing.T) {
	tests := []struct {
		name      string
		env       bool
		dbValue   string
		dbPresent bool
		want      bool
	}{
		{name: "safe default when neither source enables", env: false, want: false},
		{name: "environment fallback when database row is absent", env: true, want: true},
		{name: "database can enable over disabled environment fallback", env: false, dbValue: "true", dbPresent: true, want: true},
		{name: "database can immediately disable environment-enabled routing", env: true, dbValue: "false", dbPresent: true, want: false},
		{name: "invalid database value fails closed", env: true, dbValue: "enabled", dbPresent: true, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, ResolveImage2SmartRoutingEnabled(test.env, test.dbValue, test.dbPresent))
		})
	}
}

func TestImage2SmartRoutingDefaultGateIsDisabled(t *testing.T) {
	assert.False(t, ResolveImage2SmartRoutingEnabled(false, "", false))
}

func TestParseImage2SmartRoutingSettingRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"", "enabled", "true-ish", "null"} {
		_, err := ParseImage2SmartRoutingSetting(value)
		require.Error(t, err, value)
	}

	for _, value := range []string{"true", " FALSE ", "1", "0"} {
		_, err := ParseImage2SmartRoutingSetting(value)
		require.NoError(t, err, value)
	}
}

func TestImage2SmartRoutingRuntimeGateIsConcurrentSafe(t *testing.T) {
	original := GetImage2SmartRoutingEnabled()
	t.Cleanup(func() { SetImage2SmartRoutingEnabled(original) })

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			SetImage2SmartRoutingEnabled(true)
		}()
		go func() {
			defer wg.Done()
			_ = GetImage2SmartRoutingEnabled()
		}()
	}
	wg.Wait()
	assert.True(t, GetImage2SmartRoutingEnabled())
}
