package common

import (
	"os"
	"testing"
)

func TestImage2SmartRoutingDefaultIsDisabledWhenUnset(t *testing.T) {
	if _, exists := os.LookupEnv("IMAGE2_SMART_ROUTING_ENABLED"); exists {
		t.Skip("startup default assertion requires IMAGE2_SMART_ROUTING_ENABLED to be unset")
	}
	if Image2SmartRoutingEnabled {
		t.Fatal("Image2 smart routing must default to disabled when the environment variable is unset")
	}
}

func TestNormalizeImage2RouteMode(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "default", value: "", want: Image2RouteModeAdvanced},
		{name: "advanced", value: "advanced", want: Image2RouteModeAdvanced},
		{name: "observe ignores case and surrounding whitespace", value: " OBSERVE ", want: Image2RouteModeObserve},
		{name: "legacy ignores case and surrounding whitespace", value: " LEGACY ", want: Image2RouteModeLegacy},
		{name: "unknown stays strict", value: "minimal", want: Image2RouteModeAdvanced},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeImage2RouteMode(test.value); got != test.want {
				t.Fatalf("normalizeImage2RouteMode(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}
