package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func image2CatalogTestChannel(id int, capability *dto.Image2ChannelCapability, declared bool) *model.Channel {
	channel := &model.Channel{Id: id}
	settings := dto.ChannelSettings{}
	if declared {
		settings.Image2DeclaredCapability = capability
	} else {
		settings.Image2Capability = capability
	}
	encodedBytes, err := common.Marshal(settings)
	if err != nil {
		panic(err)
	}
	encoded := string(encodedBytes)
	channel.Setting = &encoded
	return channel
}

func TestBuildImage2CapabilityCatalogProjectsExactProfilesWithoutTopology(t *testing.T) {
	catalog := BuildImage2CapabilityCatalog("gpt-image-2-4k", "gpt-image-2", []*model.Channel{
		image2CatalogTestChannel(44, &dto.Image2ChannelCapability{
			Enabled:       true,
			Operations:    []string{"generations", "edits"},
			Resolutions:   []string{"uhd"},
			Qualities:     []string{"high"},
			EditsAccepted: true,
			Profiles: []dto.Image2CapabilityProfile{
				{Operation: "generations", Resolution: "uhd", Size: "4096x4096", Quality: "high", MaxN: 2},
				{Operation: "edits", Resolution: "uhd", Size: "4096x4096", Quality: "high", MaxN: 1},
			},
		}, true),
	})

	require.Equal(t, "gpt-image-2-4k", catalog.Group)
	require.Equal(t, []string{"edits", "generations"}, catalog.Operations)
	require.Equal(t, uint(2), catalog.MaxN)
	require.Equal(t, []Image2CapabilityOption{
		{Operation: "edits", Size: "4096x4096", Quality: "high", MaxN: 1},
		{Operation: "generations", Size: "4096x4096", Quality: "high", MaxN: 2},
	}, catalog.Profiles)
}

func TestBuildImage2CapabilityCatalogUsesAutoOnlyForEmptyQualityDeclaration(t *testing.T) {
	catalog := BuildImage2CapabilityCatalog("gpt-image-2-az", "gpt-image-2", []*model.Channel{
		image2CatalogTestChannel(45, &dto.Image2ChannelCapability{
			Enabled:     true,
			Operations:  []string{"generations"},
			Resolutions: []string{"1024"},
			MaxN:        1,
		}, true),
	})

	require.Equal(t, []Image2CapabilityOption{{
		Operation: "generations", Size: "1024x1024", Quality: "auto", MaxN: 1,
	}}, catalog.Profiles)
}

func TestBuildImage2CapabilityCatalogDoesNotFallbackFromMalformedDeclaration(t *testing.T) {
	malformed := &dto.Image2ChannelCapability{
		Enabled:     true,
		Operations:  []string{"generations"},
		Resolutions: []string{"1024"},
		Qualities:   []string{"unsupported"},
	}
	catalog := BuildImage2CapabilityCatalog("default", "gpt-image-2", []*model.Channel{
		image2CatalogTestChannel(46, malformed, true),
	})
	require.Empty(t, catalog.Profiles)
	require.Empty(t, catalog.Operations)
}
