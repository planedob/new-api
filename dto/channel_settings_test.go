package dto

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImage2ChannelCapabilityValidate(t *testing.T) {
	for _, test := range []struct {
		name       string
		capability *Image2ChannelCapability
		wantError  string
	}{
		{
			name: "valid enabled capability",
			capability: &Image2ChannelCapability{
				Enabled:       true,
				Operations:    []string{"generations", "edits"},
				Resolutions:   []string{"1024", "2048", "uhd"},
				Qualities:     []string{"standard", "high"},
				MaxN:          4,
				EditsAccepted: true,
			},
		},
		{
			name:       "disabled capability may be empty",
			capability: &Image2ChannelCapability{Enabled: false},
		},
		{
			name:       "enabled operations required",
			capability: &Image2ChannelCapability{Enabled: true, Resolutions: []string{"1024"}, MaxN: 1},
			wantError:  "operations is required",
		},
		{
			name:       "enabled resolutions required",
			capability: &Image2ChannelCapability{Enabled: true, Operations: []string{"generations"}, MaxN: 1},
			wantError:  "resolutions is required",
		},
		{
			name: "unknown operation rejected",
			capability: &Image2ChannelCapability{
				Enabled: true, Operations: []string{"generate"}, Resolutions: []string{"1024"}, MaxN: 1,
			},
			wantError: "unsupported value",
		},
		{
			name: "unknown resolution rejected",
			capability: &Image2ChannelCapability{
				Enabled: true, Operations: []string{"generations"}, Resolutions: []string{"4k"}, MaxN: 1,
			},
			wantError: "unsupported value",
		},
		{
			name: "duplicates rejected case insensitively",
			capability: &Image2ChannelCapability{
				Enabled: true, Operations: []string{"edits", "EDITS"}, Resolutions: []string{"1024"}, MaxN: 1, EditsAccepted: true,
			},
			wantError: "duplicate value",
		},
		{
			name: "blank quality rejected",
			capability: &Image2ChannelCapability{
				Enabled: true, Operations: []string{"generations"}, Resolutions: []string{"1024"}, Qualities: []string{""}, MaxN: 1,
			},
			wantError: "cannot contain an empty value",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.capability.Validate()
			if test.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestImage2CapabilityProfilesAndBoundsFailClosed(t *testing.T) {
	require.ErrorContains(t, (&Image2ChannelCapability{
		Enabled: true, Operations: []string{"generations"}, Resolutions: []string{"1024"},
	}).Validate(), "max_n must be between")
	require.ErrorContains(t, (&Image2ChannelCapability{
		Enabled: true, Operations: []string{"edits"}, Resolutions: []string{"1024"}, MaxN: 1,
	}).Validate(), "edits_accepted")
	require.ErrorContains(t, (&Image2ChannelCapability{
		Enabled: true, Operations: []string{"generations"}, Resolutions: []string{"1024"}, MaxN: 1,
		Profiles: []Image2CapabilityProfile{{Operation: "generations", Resolution: "1024", Size: "2048x2048", Quality: "default", MaxN: 1}},
	}).Validate(), "does not match resolution")
}

func TestImage2CapabilityVerificationBindsCurrentEvidence(t *testing.T) {
	capability := &Image2ChannelCapability{
		Enabled: true, Operations: []string{"generations"}, Resolutions: []string{"1024"}, MaxN: 1,
	}
	now := time.Date(2026, time.August, 23, 1, 0, 0, 0, time.UTC)
	digest, err := Image2CapabilitySHA256(capability)
	require.NoError(t, err)
	verification := &Image2CapabilityVerification{
		Status: "passed", Source: "fixed_channel_test",
		VerifiedAt: now.Add(-time.Hour).Format(time.RFC3339), ValidUntil: now.Add(time.Hour).Format(time.RFC3339),
		CapabilitySHA256: digest,
		EvidenceSHA256:   []string{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	}
	require.NoError(t, verification.Validate())
	assert.Empty(t, verification.RoutingReason(now, capability))

	changed := *capability
	changed.MaxN = 2
	assert.Equal(t, "image2_verification_capability_mismatch", verification.RoutingReason(now, &changed))
	verification.ValidUntil = now.Format(time.RFC3339)
	assert.Equal(t, "image2_verification_expired", verification.RoutingReason(now, capability))
}
