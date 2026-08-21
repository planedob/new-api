package middleware

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImage2AsyncRoutesResolveAsImagePaths(t *testing.T) {
	require.True(t, isImageGenerationsPath("/pg/images/jobs/generations"))
	require.True(t, isImageEditsPath("/pg/images/jobs/edits"))
	require.False(t, isImageGenerationsPath("/pg/images/jobs/edits"))
	require.False(t, isImageEditsPath("/pg/images/jobs/generations"))
}
