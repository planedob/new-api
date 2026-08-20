package gemini

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

func TestGetRequestURLImageModelForcesGenerateContent(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		IsStream: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelBaseUrl:    "https://generativelanguage.googleapis.com",
			UpstreamModelName: "gemini-2.5-flash-image",
		},
	}

	got, err := adaptor.GetRequestURL(info)
	if err != nil {
		t.Fatalf("GetRequestURL returned error: %v", err)
	}
	want := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash-image:generateContent"
	if got != want {
		t.Fatalf("GetRequestURL() = %q, want %q", got, want)
	}
	if info.IsStream {
		t.Fatal("image model must disable streaming before upstream dispatch")
	}
}

func TestGetRequestURLTextModelKeepsStreaming(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		IsStream: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelBaseUrl:    "https://generativelanguage.googleapis.com",
			UpstreamModelName: "gemini-2.5-flash",
		},
	}

	got, err := adaptor.GetRequestURL(info)
	if err != nil {
		t.Fatalf("GetRequestURL returned error: %v", err)
	}
	want := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse"
	if got != want {
		t.Fatalf("GetRequestURL() = %q, want %q", got, want)
	}
	if !info.IsStream {
		t.Fatal("text model streaming must remain enabled")
	}
}
