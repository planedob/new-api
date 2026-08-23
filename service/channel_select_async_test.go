package service

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

func TestApplyImageTaskChannelFilter(t *testing.T) {
	tests := []struct {
		path string
		want int
	}{
		{path: "/v1/images/generations/jobs", want: constant.ChannelTypeAPIMart},
		{path: "/pg/images/jobs/generations", want: constant.ChannelTypeAPIMart},
		{path: "/v1/images/batches", want: constant.ChannelTypeCodeFoxAsync},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			param := &RetryParam{}
			ApplyImageTaskChannelFilter(param, tt.path)
			if !param.ExhaustiveSafeFailover {
				t.Fatal("async image task selection must be exhaustive")
			}
			if _, ok := param.AllowedChannelTypes[tt.want]; !ok || len(param.AllowedChannelTypes) != 1 {
				t.Fatalf("allowed channel types = %#v, want only %d", param.AllowedChannelTypes, tt.want)
			}
		})
	}
}

func TestApplyImageTaskChannelFilterLeavesSyncImagesUnchanged(t *testing.T) {
	param := &RetryParam{}
	ApplyImageTaskChannelFilter(param, "/v1/images/generations")
	if param.ExhaustiveSafeFailover || len(param.AllowedChannelTypes) != 0 {
		t.Fatalf("sync image route unexpectedly filtered: %#v", param)
	}
}

func TestImageTaskChannelAllowedRejectsSynchronousProvider(t *testing.T) {
	if IsImageTaskChannelAllowed("/v1/images/generations/jobs", &model.Channel{Type: constant.ChannelTypeOpenAI}) {
		t.Fatal("synchronous OpenAI channel must not serve APIMart job route")
	}
	if !IsImageTaskChannelAllowed("/v1/images/generations/jobs", &model.Channel{Type: constant.ChannelTypeAPIMart}) {
		t.Fatal("APIMart task channel was rejected")
	}
	if !IsImageTaskChannelAllowed("/v1/images/batches", &model.Channel{Type: constant.ChannelTypeCodeFoxAsync}) {
		t.Fatal("CodeFox async task channel was rejected")
	}
}
