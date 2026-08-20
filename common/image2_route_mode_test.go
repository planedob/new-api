package common

import "testing"

func TestNormalizeImage2RouteMode(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "default", value: "", want: Image2RouteModeAdvanced},
		{name: "advanced", value: "advanced", want: Image2RouteModeAdvanced},
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
