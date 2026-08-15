package controller

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/model"
)

var managedImage2SettingKeys = []string{
	"image2_declared_capability",
	"image2_capability",
	"image2_capability_verification",
}

// mergeManagedImage2Settings prevents an ordinary channel edit made from an
// older or stale admin form from silently deleting Image2 routing evidence.
// A caller can still replace a managed field by including that field
// explicitly; omission means preserve the current server value.
func mergeManagedImage2Settings(original, incoming string, allowManagedReplacement bool) (string, error) {
	var before map[string]json.RawMessage
	if original == "" {
		before = map[string]json.RawMessage{}
	} else if err := json.Unmarshal([]byte(original), &before); err != nil {
		return "", fmt.Errorf("current channel setting is invalid: %w", err)
	}
	var after map[string]json.RawMessage
	if incoming == "" {
		after = map[string]json.RawMessage{}
	} else if err := json.Unmarshal([]byte(incoming), &after); err != nil {
		return "", fmt.Errorf("submitted channel setting is invalid: %w", err)
	}
	for _, key := range managedImage2SettingKeys {
		if allowManagedReplacement {
			continue
		}
		if value, exists := before[key]; exists {
			after[key] = value
		} else {
			delete(after, key)
		}
	}
	encoded, err := json.Marshal(after)
	if err != nil {
		return "", fmt.Errorf("cannot encode merged channel setting: %w", err)
	}
	return string(encoded), nil
}

func managedImage2SettingsSHA256(setting string) (string, error) {
	var value map[string]json.RawMessage
	if setting == "" {
		value = map[string]json.RawMessage{}
	} else if err := json.Unmarshal([]byte(setting), &value); err != nil {
		return "", err
	}
	managed := make(map[string]json.RawMessage, len(managedImage2SettingKeys))
	for _, key := range managedImage2SettingKeys {
		if raw, exists := value[key]; exists {
			managed[key] = raw
		}
	}
	encoded, err := json.Marshal(managed)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest[:]), nil
}

func image2UpstreamConfigChanged(original, incoming *model.Channel) bool {
	if original == nil || incoming == nil {
		return false
	}
	if incoming.Key != "" && incoming.Key != original.Key {
		return true
	}
	if (incoming.Type != 0 && incoming.Type != original.Type) ||
		(incoming.Models != "" && incoming.Models != original.Models) ||
		(incoming.Group != "" && incoming.Group != original.Group) {
		return true
	}
	stringPointer := func(value *string) string {
		if value == nil {
			return ""
		}
		return strings.TrimSpace(*value)
	}
	return (incoming.BaseURL != nil && stringPointer(incoming.BaseURL) != stringPointer(original.BaseURL)) ||
		(incoming.ModelMapping != nil && stringPointer(incoming.ModelMapping) != stringPointer(original.ModelMapping))
}

func markImage2VerificationStale(channel *model.Channel) error {
	setting, err := channel.ParseSetting()
	if err != nil || setting.Image2CapabilityVerification == nil {
		return err
	}
	setting.Image2CapabilityVerification.Status = "stale"
	channel.SetSetting(setting)
	return nil
}
