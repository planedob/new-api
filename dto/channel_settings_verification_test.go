package dto

import (
	"strings"
	"testing"
	"time"
)

func TestImage2CapabilityVerificationValidation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	capability := &Image2ChannelCapability{Enabled: true, Operations: []string{"generations"}, Resolutions: []string{"1024"}}
	capabilityDigest, err := Image2CapabilitySHA256(capability)
	if err != nil {
		t.Fatal(err)
	}
	valid := &Image2CapabilityVerification{
		Status:           "passed",
		Source:           "fixed_channel_test",
		VerifiedAt:       now.Format(time.RFC3339),
		ValidUntil:       now.Add(24 * time.Hour).Format(time.RFC3339),
		CapabilitySHA256: capabilityDigest,
		EvidenceSHA256:   []string{strings.Repeat("a", 64)},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid verification rejected: %v", err)
	}

	invalid := *valid
	invalid.EvidenceSHA256 = []string{"request-id-is-not-evidence-integrity"}
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid evidence digest accepted")
	}

	expired := *valid
	expired.VerifiedAt = now.Add(-2 * time.Hour).Format(time.RFC3339)
	expired.ValidUntil = now.Add(-time.Hour).Format(time.RFC3339)
	if reason := expired.RoutingReason(now, capability); reason != "image2_verification_expired" {
		t.Fatalf("RoutingReason() = %q", reason)
	}
}

func TestImage2CapabilityDigestMatchesOfflineMatrixBuilder(t *testing.T) {
	capability := &Image2ChannelCapability{
		Enabled: true, Operations: []string{"generations"}, Resolutions: []string{"1024"},
		MaxN: 1, RoutePriority: 10,
		Profiles: []Image2CapabilityProfile{{Operation: "generations", Resolution: "1024", Size: "1024x1024", Quality: "default", MaxN: 1}},
	}
	digest, err := Image2CapabilitySHA256(capability)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "6e6f0106c7bfb2d72c5205649d84b1e5cd71c8f8a46ae969283f5be07976411a" {
		t.Fatalf("cross-language capability digest mismatch: %s", digest)
	}
}
