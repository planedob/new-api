package controller

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGetLocalModelCatalogRequiresExplicitFlag(t *testing.T) {
	t.Setenv("MODEL_WORKBENCH_LOCAL_FIXTURE", "false")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/catalog/v1/models", GetLocalModelCatalog)
	req := httptest.NewRequest(http.MethodGet, "/api/catalog/v1/models", nil)
	req.Host = "127.0.0.1:3000"
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("expected disabled fixture to return 404, got %d", res.Code)
	}
}

func TestGetLocalModelCatalogIsRedacted(t *testing.T) {
	previous := os.Getenv("MODEL_WORKBENCH_LOCAL_FIXTURE")
	t.Cleanup(func() { _ = os.Setenv("MODEL_WORKBENCH_LOCAL_FIXTURE", previous) })
	_ = os.Setenv("MODEL_WORKBENCH_LOCAL_FIXTURE", "true")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/catalog/v1/models", GetLocalModelCatalog)
	req := httptest.NewRequest(http.MethodGet, "/api/catalog/v1/models", nil)
	req.Host = "127.0.0.1:3000"
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected enabled fixture to return 200, got %d", res.Code)
	}
	body := res.Body.String()
	for _, forbidden := range []string{"channel_id", "base_url", "priority", "weight", "token", "api_key", "cookie", "prompt"} {
		if strings.Contains(strings.ToLower(body), `"`+forbidden+`"`) {
			t.Fatalf("catalog contains forbidden field %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "LOCAL_FIXTURE") || !strings.Contains(body, "gpt-image-2") || !strings.Contains(body, `"operation":"image_generation"`) || !strings.Contains(body, `"operation":"image_edits"`) {
		t.Fatalf("catalog missing fixed scope or representative model: %s", body)
	}
}

func TestGetLocalModelCatalogRejectsNonLoopbackHost(t *testing.T) {
	t.Setenv("MODEL_WORKBENCH_LOCAL_FIXTURE", "true")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/catalog/v1/models", GetLocalModelCatalog)
	req := httptest.NewRequest(http.MethodGet, "/api/catalog/v1/models", nil)
	req.Host = "aibuff.cc"
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("expected non-loopback fixture request to return 404, got %d", res.Code)
	}
}
