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
