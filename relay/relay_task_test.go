package relay

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/constant"
)

func TestImageTaskFetchRouteStrictlySeparatesAsyncProviders(t *testing.T) {
	apimart := constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeAPIMart))
	codefox := constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeCodeFoxAsync))
	tests := []struct {
		path        string
		wantRoute   imageTaskFetchRoute
		platform    constant.TaskPlatform
		wantAllowed bool
	}{
		{path: "/v1/images/generations/jobs/task_apimart", wantRoute: imageTaskFetchRouteAPIMart, platform: apimart, wantAllowed: true},
		{path: "/pg/images/jobs/task_apimart", wantRoute: imageTaskFetchRouteAPIMart, platform: apimart, wantAllowed: true},
		{path: "/v1/images/generations/jobs/task_codefox", wantRoute: imageTaskFetchRouteAPIMart, platform: codefox, wantAllowed: false},
		{path: "/v1/images/batches/task_codefox", wantRoute: imageTaskFetchRouteCodeFox, platform: codefox, wantAllowed: true},
		{path: "/v1/images/batches/task_apimart", wantRoute: imageTaskFetchRouteCodeFox, platform: apimart, wantAllowed: false},
		{path: "/v1/images/generations/task_not_a_job", wantRoute: imageTaskFetchRouteUnknown, platform: apimart, wantAllowed: false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := imageTaskFetchRouteForPath(tt.path); got != tt.wantRoute {
				t.Fatalf("imageTaskFetchRouteForPath(%q) = %d, want %d", tt.path, got, tt.wantRoute)
			}
			if got := imageTaskPlatformAllowed(tt.wantRoute, tt.platform); got != tt.wantAllowed {
				t.Fatalf("imageTaskPlatformAllowed(%d, %q) = %v, want %v", tt.wantRoute, tt.platform, got, tt.wantAllowed)
			}
		})
	}
}
