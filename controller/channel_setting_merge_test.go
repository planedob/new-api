package controller

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/model"
)

func TestMergeManagedImage2SettingsPreservesCapabilityOnOrdinaryEdit(t *testing.T) {
	original := `{"proxy":"","image2_capability":{"enabled":true,"operations":["generations","edits"]},"image2_capability_verification":{"status":"passed"}}`
	incoming := `{"proxy":"socks5://redacted"}`
	merged, err := mergeManagedImage2Settings(original, incoming, false)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(merged), &value); err != nil {
		t.Fatal(err)
	}
	if value["image2_capability"] == nil || value["image2_capability_verification"] == nil {
		t.Fatalf("managed Image2 fields were lost: %s", merged)
	}
	if value["proxy"] != "socks5://redacted" {
		t.Fatalf("ordinary setting was not updated: %s", merged)
	}
}

func TestMergeManagedImage2SettingsRejectsStaleExplicitManagedReplacement(t *testing.T) {
	original := `{"image2_capability":{"enabled":true,"operations":["generations"]}}`
	incoming := `{"image2_capability":{"enabled":false}}`
	merged, err := mergeManagedImage2Settings(original, incoming, false)
	if err != nil {
		t.Fatal(err)
	}
	if merged != original {
		t.Fatalf("stale managed replacement was not ignored: %s", merged)
	}
}

func TestMergeManagedImage2SettingsAllowsCASVerifiedReplacement(t *testing.T) {
	original := `{"image2_capability":{"enabled":true,"operations":["generations"]}}`
	incoming := `{"image2_capability":{"enabled":false}}`
	merged, err := mergeManagedImage2Settings(original, incoming, true)
	if err != nil {
		t.Fatal(err)
	}
	if merged != incoming {
		t.Fatalf("CAS-authorized managed replacement was not honored: %s", merged)
	}
}

func TestImage2VerificationBecomesStaleWhenKeyOrMappingChanges(t *testing.T) {
	baseURL := "https://upstream.example"
	mapping := `{"gpt-image-2":"provider-image"}`
	original := &model.Channel{Type: 1, Key: "old", Models: "gpt-image-2", Group: "image", BaseURL: &baseURL, ModelMapping: &mapping}
	unchanged := *original
	unchanged.Key = ""
	if image2UpstreamConfigChanged(original, &unchanged) {
		t.Fatal("an omitted key must not invalidate verification")
	}
	changed := unchanged
	changed.Key = "new"
	if !image2UpstreamConfigChanged(original, &changed) {
		t.Fatal("a rotated key must invalidate verification")
	}
	newMapping := `{"gpt-image-2":"provider-image-v2"}`
	changed = unchanged
	changed.ModelMapping = &newMapping
	if !image2UpstreamConfigChanged(original, &changed) {
		t.Fatal("a model mapping change must invalidate verification")
	}
}
