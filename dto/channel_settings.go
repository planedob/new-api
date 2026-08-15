package dto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ChannelSettings struct {
	ForceFormat            bool   `json:"force_format,omitempty"`
	ThinkingToContent      bool   `json:"thinking_to_content,omitempty"`
	Proxy                  string `json:"proxy"`
	PassThroughBodyEnabled bool   `json:"pass_through_body_enabled,omitempty"`
	SystemPrompt           string `json:"system_prompt,omitempty"`
	SystemPromptOverride   bool   `json:"system_prompt_override,omitempty"`
	// Image2Capability contains controlled-test or legacy observed capability.
	// It is used as a fallback only when the supplier declaration is absent.
	Image2Capability *Image2ChannelCapability `json:"image2_capability,omitempty"`
	// Image2DeclaredCapability records the supplier capability statement and is
	// the primary smart-routing contract. Tests supplement a missing statement
	// and detect operational conflicts; they are not mandatory for every
	// declared combination.
	Image2DeclaredCapability *Image2ChannelCapability `json:"image2_declared_capability,omitempty"`
	// Image2CapabilityVerification binds the tested fallback capability above to
	// controlled evidence when no supplier declaration is available.
	Image2CapabilityVerification *Image2CapabilityVerification `json:"image2_capability_verification,omitempty"`
}

// Image2ChannelCapability describes Image2 request shapes. The same shape is
// used for a supplier declaration and for test-derived fallback data.
// RoutePriority is not a price, channel priority, or weight.
type Image2ChannelCapability struct {
	Enabled     bool     `json:"enabled,omitempty"`
	Operations  []string `json:"operations,omitempty"`  // generations, edits
	Resolutions []string `json:"resolutions,omitempty"` // 1024, 2048, uhd
	// Qualities is the explicit quality allow-list. An empty list means that
	// only the provider default (request quality omitted or "auto") is proven;
	// it is never a wildcard for unverified explicit qualities.
	Qualities     []string `json:"qualities,omitempty"`
	MaxN          uint     `json:"max_n,omitempty"` // zero means no declared limit
	RoutePriority int      `json:"route_priority,omitempty"`
	EditsAccepted bool     `json:"edits_accepted,omitempty"`
	// Profiles are the exact operation/resolution/quality/n combinations proven
	// by tests. When present, routing uses these rows instead of inventing a
	// cross-product from the summary lists above.
	Profiles []Image2CapabilityProfile `json:"profiles,omitempty"`
}

type Image2CapabilityProfile struct {
	Operation  string `json:"operation"`
	Resolution string `json:"resolution"`
	// Size is the exact tested request size. Empty is accepted only for legacy
	// profiles created before exact-size evidence was introduced.
	Size string `json:"size,omitempty"`
	// Quality is "default" for omitted/auto requests, otherwise standard/high.
	Quality string `json:"quality"`
	MaxN    uint   `json:"max_n"`
}

// Image2CapabilityVerification is integrity metadata for a capability derived
// from controlled fixed-channel tests. Request IDs are deliberately not stored
// here; EvidenceSHA256 binds the non-secret evidence document without placing
// operational logs or credentials in channel settings.
type Image2CapabilityVerification struct {
	Status           string   `json:"status"` // passed, failed, conflict, stale
	Source           string   `json:"source"` // fixed_channel_test
	VerifiedAt       string   `json:"verified_at"`
	ValidUntil       string   `json:"valid_until"`
	CapabilitySHA256 string   `json:"capability_sha256"`
	EvidenceSHA256   []string `json:"evidence_sha256"`
}

func (verification *Image2CapabilityVerification) Validate() error {
	if verification == nil {
		return nil
	}
	status := strings.ToLower(strings.TrimSpace(verification.Status))
	switch status {
	case "passed", "failed", "conflict", "stale":
	default:
		return fmt.Errorf("image2_capability_verification.status is invalid")
	}
	if strings.ToLower(strings.TrimSpace(verification.Source)) != "fixed_channel_test" {
		return fmt.Errorf("image2_capability_verification.source must be fixed_channel_test")
	}
	verifiedAt, err := time.Parse(time.RFC3339, verification.VerifiedAt)
	if err != nil {
		return fmt.Errorf("image2_capability_verification.verified_at must be RFC3339")
	}
	validUntil, err := time.Parse(time.RFC3339, verification.ValidUntil)
	if err != nil {
		return fmt.Errorf("image2_capability_verification.valid_until must be RFC3339")
	}
	if !validUntil.After(verifiedAt) {
		return fmt.Errorf("image2_capability_verification.valid_until must be after verified_at")
	}
	if len(verification.EvidenceSHA256) == 0 {
		return fmt.Errorf("image2_capability_verification.evidence_sha256 is required")
	}
	if !validSHA256(verification.CapabilitySHA256) {
		return fmt.Errorf("image2_capability_verification.capability_sha256 is invalid")
	}
	seen := make(map[string]struct{}, len(verification.EvidenceSHA256))
	for _, digest := range verification.EvidenceSHA256 {
		digest = strings.TrimSpace(digest)
		if !validSHA256(digest) {
			return fmt.Errorf("image2_capability_verification.evidence_sha256 contains an invalid digest")
		}
		if _, duplicate := seen[digest]; duplicate {
			return fmt.Errorf("image2_capability_verification.evidence_sha256 contains a duplicate digest")
		}
		seen[digest] = struct{}{}
	}
	return nil
}

func validSHA256(digest string) bool {
	digest = strings.TrimSpace(digest)
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == 32 && digest == strings.ToLower(digest)
}

func Image2CapabilitySHA256(capability *Image2ChannelCapability) (string, error) {
	encoded, err := json.Marshal(capability)
	if err != nil {
		return "", err
	}
	var canonical any
	if err := json.Unmarshal(encoded, &canonical); err != nil {
		return "", err
	}
	encoded, err = json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// RoutingReason returns an empty string only for a current passed test record.
func (verification *Image2CapabilityVerification) RoutingReason(now time.Time, capability *Image2ChannelCapability) string {
	if verification == nil {
		return "image2_verification_missing"
	}
	if err := verification.Validate(); err != nil {
		return "image2_verification_invalid"
	}
	status := strings.ToLower(strings.TrimSpace(verification.Status))
	if status != "passed" {
		return "image2_verification_" + status
	}
	capabilityDigest, err := Image2CapabilitySHA256(capability)
	if err != nil || capabilityDigest != verification.CapabilitySHA256 {
		return "image2_verification_capability_mismatch"
	}
	validUntil, _ := time.Parse(time.RFC3339, verification.ValidUntil)
	if !now.Before(validUntil) {
		return "image2_verification_expired"
	}
	return ""
}

func (capability *Image2ChannelCapability) Validate() error {
	if capability == nil {
		return nil
	}
	if !capability.Enabled {
		return nil
	}
	if len(capability.Operations) == 0 {
		return fmt.Errorf("image2_capability.operations is required when enabled")
	}
	if len(capability.Resolutions) == 0 {
		return fmt.Errorf("image2_capability.resolutions is required when enabled")
	}
	if err := validateImage2CapabilityValues("operations", capability.Operations, map[string]struct{}{
		"generations": {},
		"edits":       {},
	}); err != nil {
		return err
	}
	if err := validateImage2CapabilityValues("resolutions", capability.Resolutions, map[string]struct{}{
		"1024": {},
		"2048": {},
		"uhd":  {},
	}); err != nil {
		return err
	}
	if !capability.EditsAccepted {
		for _, operation := range capability.Operations {
			if strings.EqualFold(strings.TrimSpace(operation), "edits") {
				return fmt.Errorf("image2_capability.edits_accepted must be true when operations includes edits")
			}
		}
	}
	if err := validateImage2QualityValues(capability.Qualities); err != nil {
		return err
	}
	seenProfiles := make(map[string]struct{}, len(capability.Profiles))
	for index, profile := range capability.Profiles {
		operation := strings.ToLower(strings.TrimSpace(profile.Operation))
		if operation != "generations" && operation != "edits" {
			return fmt.Errorf("image2_capability.profiles[%d].operation is unsupported", index)
		}
		resolution := strings.ToLower(strings.TrimSpace(profile.Resolution))
		if resolution != "1024" && resolution != "2048" && resolution != "uhd" {
			return fmt.Errorf("image2_capability.profiles[%d].resolution is unsupported", index)
		}
		quality := strings.ToLower(strings.TrimSpace(profile.Quality))
		if quality != "default" && quality != "standard" && quality != "high" {
			return fmt.Errorf("image2_capability.profiles[%d].quality is unsupported", index)
		}
		if profile.MaxN < 1 {
			return fmt.Errorf("image2_capability.profiles[%d].max_n must be positive", index)
		}
		if strings.ContainsAny(profile.Size, "\r\n\x00") {
			return fmt.Errorf("image2_capability.profiles[%d].size is invalid", index)
		}
		key := fmt.Sprintf("%s/%s/%s/%s/%d", operation, resolution, strings.ToLower(strings.TrimSpace(profile.Size)), quality, profile.MaxN)
		if _, duplicate := seenProfiles[key]; duplicate {
			return fmt.Errorf("image2_capability.profiles contains duplicate %s", key)
		}
		seenProfiles[key] = struct{}{}
	}
	return nil
}

func validateImage2QualityValues(values []string) error {
	allowed := map[string]struct{}{
		"standard": {},
		"high":     {},
	}
	seen := make(map[string]struct{}, len(values))
	for _, quality := range values {
		normalized := strings.ToLower(strings.TrimSpace(quality))
		if normalized == "" {
			return fmt.Errorf("image2_capability.qualities cannot contain an empty value")
		}
		if normalized == "auto" {
			return fmt.Errorf("image2_capability.qualities cannot contain auto; omit qualities to use the provider default")
		}
		if _, ok := allowed[normalized]; !ok {
			return fmt.Errorf("image2_capability.qualities contains unsupported value %q", quality)
		}
		if _, duplicate := seen[normalized]; duplicate {
			return fmt.Errorf("image2_capability.qualities contains duplicate value %q", quality)
		}
		seen[normalized] = struct{}{}
	}
	return nil
}

func validateImage2CapabilityValues(field string, values []string, allowed map[string]struct{}) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if _, ok := allowed[normalized]; !ok {
			return fmt.Errorf("image2_capability.%s contains unsupported value %q", field, value)
		}
		if _, duplicate := seen[normalized]; duplicate {
			return fmt.Errorf("image2_capability.%s contains duplicate value %q", field, value)
		}
		seen[normalized] = struct{}{}
	}
	return nil
}

type VertexKeyType string

const (
	VertexKeyTypeJSON   VertexKeyType = "json"
	VertexKeyTypeAPIKey VertexKeyType = "api_key"
)

type AwsKeyType string

const (
	AwsKeyTypeAKSK   AwsKeyType = "ak_sk" // 默认
	AwsKeyTypeApiKey AwsKeyType = "api_key"
)

type ChannelOtherSettings struct {
	AzureResponsesVersion                 string        `json:"azure_responses_version,omitempty"`
	VertexKeyType                         VertexKeyType `json:"vertex_key_type,omitempty"` // "json" or "api_key"
	OpenRouterEnterprise                  *bool         `json:"openrouter_enterprise,omitempty"`
	ClaudeBetaQuery                       bool          `json:"claude_beta_query,omitempty"`         // Claude 渠道是否强制追加 ?beta=true
	AllowServiceTier                      bool          `json:"allow_service_tier,omitempty"`        // 是否允许 service_tier 透传（默认过滤以避免额外计费）
	AllowInferenceGeo                     bool          `json:"allow_inference_geo,omitempty"`       // 是否允许 inference_geo 透传（仅 Claude，默认过滤以满足数据驻留合规
	AllowSpeed                            bool          `json:"allow_speed,omitempty"`               // 是否允许 speed 透传（仅 Claude，默认过滤以避免意外切换推理速度模式）
	AllowSafetyIdentifier                 bool          `json:"allow_safety_identifier,omitempty"`   // 是否允许 safety_identifier 透传（默认过滤以保护用户隐私）
	DisableStore                          bool          `json:"disable_store,omitempty"`             // 是否禁用 store 透传（默认允许透传，禁用后可能导致 Codex 无法使用）
	AllowIncludeObfuscation               bool          `json:"allow_include_obfuscation,omitempty"` // 是否允许 stream_options.include_obfuscation 透传（默认过滤以避免关闭流混淆保护）
	AwsKeyType                            AwsKeyType    `json:"aws_key_type,omitempty"`
	UpstreamModelUpdateCheckEnabled       bool          `json:"upstream_model_update_check_enabled,omitempty"`        // 是否检测上游模型更新
	UpstreamModelUpdateAutoSyncEnabled    bool          `json:"upstream_model_update_auto_sync_enabled,omitempty"`    // 是否自动同步上游模型更新
	UpstreamModelUpdateLastCheckTime      int64         `json:"upstream_model_update_last_check_time,omitempty"`      // 上次检测时间
	UpstreamModelUpdateLastDetectedModels []string      `json:"upstream_model_update_last_detected_models,omitempty"` // 上次检测到的可加入模型
	UpstreamModelUpdateLastRemovedModels  []string      `json:"upstream_model_update_last_removed_models,omitempty"`  // 上次检测到的可删除模型
	UpstreamModelUpdateIgnoredModels      []string      `json:"upstream_model_update_ignored_models,omitempty"`       // 手动忽略的模型
}

func (s *ChannelOtherSettings) IsOpenRouterEnterprise() bool {
	if s == nil || s.OpenRouterEnterprise == nil {
		return false
	}
	return *s.OpenRouterEnterprise
}
