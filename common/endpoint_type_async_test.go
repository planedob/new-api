package common

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
)

func TestAsyncImageChannelEndpointTypeIsNotDuplicated(t *testing.T) {
	for _, channelType := range []int{constant.ChannelTypeAPIMart, constant.ChannelTypeCodeFoxAsync} {
		got := GetEndpointTypesByChannelType(channelType, "gpt-image-2")
		if len(got) != 1 || got[0] != constant.EndpointTypeImageGeneration {
			t.Fatalf("channel %d endpoint types = %#v", channelType, got)
		}
	}
}

func TestSecureSkillNativeH3UsesVideoEndpointAndOpenAIAdminType(t *testing.T) {
	got := GetEndpointTypesByChannelType(constant.ChannelTypeSecureSkillNativeH3, "minimax-h3")
	if len(got) != 1 || got[0] != constant.EndpointTypeOpenAIVideo {
		t.Fatalf("endpoint types = %#v", got)
	}
	apiType, ok := ChannelType2APIType(constant.ChannelTypeSecureSkillNativeH3)
	if !ok || apiType != constant.APITypeOpenAI {
		t.Fatalf("api type = %d, ok = %v", apiType, ok)
	}
}
