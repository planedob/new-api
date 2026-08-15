package service

import (
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/model"
)

// Image2CapabilityOption is the customer-safe projection of one supported
// Image2 combination. It deliberately contains no channel, provider, key, or
// health information; it is suitable for the authenticated frontend options
// endpoint.
type Image2CapabilityOption struct {
	Operation string `json:"operation"`
	Size      string `json:"size"`
	Quality   string `json:"quality"`
	MaxN      uint   `json:"max_n"`
}

// Image2CapabilityCatalog is the capability entry returned to a customer for
// one resolved model/group. Profiles remain the source of truth for dependent
// size/quality controls; summary lists are convenience projections only.
type Image2CapabilityCatalog struct {
	Model      string                   `json:"model"`
	Group      string                   `json:"group"`
	Operations []string                 `json:"operations"`
	Profiles   []Image2CapabilityOption `json:"profiles"`
	MaxN       uint                     `json:"max_n"`
}

// BuildImage2CapabilityCatalog projects the configured declarations for a
// model/group without exposing channel topology. A supplier declaration is
// preferred, matching the router contract; a tested/legacy capability is used
// only when the declaration is absent. Malformed declarations fail closed for
// that channel rather than silently falling back to stale data.
func BuildImage2CapabilityCatalog(group, modelName string, channels []*model.Channel) Image2CapabilityCatalog {
	catalog := Image2CapabilityCatalog{
		Model:      strings.TrimSpace(modelName),
		Group:      strings.TrimSpace(group),
		Operations: []string{},
		Profiles:   []Image2CapabilityOption{},
	}
	if catalog.Model == "" {
		catalog.Model = "gpt-image-2"
	}

	// Keep the highest max_n for a duplicate combination. A disabled channel is
	// still included here because the UI must distinguish a valid capability
	// from a temporarily unavailable one (the request will receive 503).
	profileMaxN := make(map[string]uint)
	operationSet := make(map[string]struct{})
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		settings, err := channel.ParseSetting()
		if err != nil {
			continue
		}
		capability := settings.Image2DeclaredCapability
		if capability == nil {
			capability = settings.Image2Capability
		}
		if capability == nil || !capability.Enabled || capability.Validate() != nil {
			continue
		}
		for _, operation := range capability.Operations {
			normalized := strings.ToLower(strings.TrimSpace(operation))
			if normalized == "generations" || normalized == "edits" {
				operationSet[normalized] = struct{}{}
			}
		}

		if len(capability.Profiles) > 0 {
			for _, profile := range capability.Profiles {
				operation := strings.ToLower(strings.TrimSpace(profile.Operation))
				if operation != "generations" && operation != "edits" {
					continue
				}
				size := canonicalImage2CatalogSize(profile.Size)
				if size == "auto" {
					size = canonicalImage2CatalogSize(profile.Resolution)
				}
				quality := normalizeImage2CatalogQuality(profile.Quality)
				if size == "" || quality == "" {
					continue
				}
				operationSet[operation] = struct{}{}
				key := image2CatalogProfileKey(operation, size, quality)
				if profile.MaxN > profileMaxN[key] {
					profileMaxN[key] = profile.MaxN
				}
			}
			continue
		}

		// Supplier declarations without exact profiles are already the router's
		// declared cross-product contract, so projecting that same product is safe.
		qualities := []string{"auto"}
		if len(capability.Qualities) > 0 {
			qualities = make([]string, 0, len(capability.Qualities))
			for _, quality := range capability.Qualities {
				if normalized := normalizeImage2CatalogQuality(quality); normalized != "" {
					qualities = append(qualities, normalized)
				}
			}
			if len(qualities) == 0 {
				qualities = []string{"auto"}
			}
		}
		for _, operation := range capability.Operations {
			normalizedOperation := strings.ToLower(strings.TrimSpace(operation))
			if normalizedOperation != "generations" && normalizedOperation != "edits" {
				continue
			}
			for _, resolution := range capability.Resolutions {
				size := canonicalImage2CatalogSize(resolution)
				if size == "auto" || size == "" {
					continue
				}
				for _, quality := range qualities {
					key := image2CatalogProfileKey(normalizedOperation, size, quality)
					if capability.MaxN > profileMaxN[key] {
						profileMaxN[key] = capability.MaxN
					}
				}
			}
		}
	}

	for operation := range operationSet {
		catalog.Operations = append(catalog.Operations, operation)
	}
	sort.Strings(catalog.Operations)
	for key, maxN := range profileMaxN {
		parts := strings.Split(key, "\x00")
		if len(parts) != 3 {
			continue
		}
		catalog.Profiles = append(catalog.Profiles, Image2CapabilityOption{
			Operation: parts[0],
			Size:      parts[1],
			Quality:   parts[2],
			MaxN:      maxN,
		})
		if maxN > catalog.MaxN {
			catalog.MaxN = maxN
		}
	}
	sort.Slice(catalog.Profiles, func(i, j int) bool {
		left, right := catalog.Profiles[i], catalog.Profiles[j]
		if left.Operation != right.Operation {
			return left.Operation < right.Operation
		}
		if left.Size != right.Size {
			return left.Size < right.Size
		}
		return left.Quality < right.Quality
	})
	return catalog
}

func image2CatalogProfileKey(operation, size, quality string) string {
	return strings.ToLower(strings.TrimSpace(operation)) + "\x00" + canonicalImage2CatalogSize(size) + "\x00" + normalizeImage2CatalogQuality(quality)
}

func normalizeImage2CatalogQuality(quality string) string {
	quality = strings.ToLower(strings.TrimSpace(quality))
	if quality == "" || quality == "default" || quality == "auto" {
		return "auto"
	}
	if quality == "standard" || quality == "high" {
		return quality
	}
	return ""
}

func canonicalImage2CatalogSize(size string) string {
	size = strings.ToLower(strings.TrimSpace(size))
	switch size {
	case "", "auto":
		return "auto"
	case "1024":
		return "1024x1024"
	case "2048":
		return "2048x2048"
	case "4096":
		return "4096x4096"
	case "uhd":
		return "3840x2160"
	default:
		return size
	}
}
