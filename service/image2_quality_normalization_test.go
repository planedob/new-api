package service

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/stretchr/testify/require"
)

func TestParseImage2RequestCapabilityNormalizesLegalQuality(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-2",
		RelayMode:       relayconstant.RelayModeImagesGenerations,
	}

	for _, requested := range []string{"", "auto", "standard", "high"} {
		capability, err := ParseImage2RequestCapability(info, &dto.ImageRequest{
			Quality: requested,
			Size:    "3840x2160",
		})
		require.NoError(t, err, requested)
		wantRequested := requested
		if wantRequested == "" {
			wantRequested = "auto"
		}
		require.Equal(t, wantRequested, capability.RequestedQuality)
		require.Equal(t, "auto", capability.EffectiveQuality)
		require.Equal(t, "auto", capability.Quality)
		require.Equal(t, "uhd", capability.Resolution)
		require.Equal(t, "3840x2160", capability.Size)
	}
}

func TestImage2HighDoesNotFilterUHDGenerationCandidate(t *testing.T) {
	channel := image2TestChannel(32, 10, []string{"generations"}, []string{"uhd"}, false)
	router := newImage2SmartRouter(Image2RequestCapability{
		Operation:        "generations",
		Resolution:       "uhd",
		Size:             "3840x2160",
		RequestedQuality: "high",
		Quality:          "high",
		N:                1,
	}, []*model.Channel{channel})

	selected, err := router.Next()
	require.Nil(t, err)
	require.Equal(t, channel.Id, selected.Id)
	require.Equal(t, "high", router.RequestCapability().RequestedQuality)
	require.Equal(t, "auto", router.RequestCapability().EffectiveQuality)
	require.Equal(t, "auto", router.RequestCapability().Quality)
}

func TestNormalizeImage2RequestForUpstreamKeepsAuditQualityAndChangesWireValue(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-2",
		RelayMode:       relayconstant.RelayModeImagesGenerations,
	}
	request := &dto.ImageRequest{Quality: "high", Size: "3840x2160"}
	capability, err := NormalizeImage2RequestForUpstream(info, request)
	require.NoError(t, err)
	require.Equal(t, "high", capability.RequestedQuality)
	require.Equal(t, "auto", capability.EffectiveQuality)
	require.Equal(t, "auto", request.Quality)
}

func TestImage2QualityDoesNotChangeImagePriceRatio(t *testing.T) {
	auto := (&dto.ImageRequest{Model: "gpt-image-2", Quality: "auto", Size: "1024x1024"}).GetTokenCountMeta()
	high := (&dto.ImageRequest{Model: "gpt-image-2", Quality: "high", Size: "1024x1024"}).GetTokenCountMeta()
	standard := (&dto.ImageRequest{Model: "gpt-image-2", Quality: "standard", Size: "1024x1024"}).GetTokenCountMeta()
	require.Equal(t, auto.ImagePriceRatio, high.ImagePriceRatio)
	require.Equal(t, auto.ImagePriceRatio, standard.ImagePriceRatio)
}

func TestParseImage2RequestCapabilityRejectsUnknownQuality(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-2",
		RelayMode:       relayconstant.RelayModeImagesGenerations,
	}
	_, err := ParseImage2RequestCapability(info, &dto.ImageRequest{Quality: "ultra"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "quality")
}
