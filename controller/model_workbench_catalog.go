package controller

import (
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// LocalModelCatalogItem is deliberately a public, redacted DTO. It must never
// grow fields that identify a channel, credential, customer, or production
// routing decision.
type LocalModelCatalogItem struct {
	Model             string         `json:"model"`
	Vendor            string         `json:"vendor"`
	Group             string         `json:"group"`
	Operation         string         `json:"operation"`
	ParameterBounds   map[string]any `json:"parameter_bounds"`
	Cataloged         bool           `json:"cataloged"`
	Selectable        bool           `json:"selectable"`
	Testable          bool           `json:"testable"`
	Verified          string         `json:"verified"`
	VerificationScope string         `json:"verification_scope"`
	PriceSummary      string         `json:"price_summary"`
	Performance       map[string]any `json:"performance"`
	Tags              []string       `json:"tags"`
	EndpointType      string         `json:"endpoint_type"`
}

type localModelCatalogDefinition struct {
	Model             string
	Vendor            string
	Group             string
	Operations        []string
	ParameterBounds   map[string]any
	Cataloged         bool
	Selectable        bool
	Testable          bool
	Verified          string
	VerificationScope string
	PriceSummary      string
	Performance       map[string]any
	Tags              []string
	EndpointType      string
}

var localModelCatalogDefinitions = []localModelCatalogDefinition{
	{Model: "gpt-4o-mini", Vendor: "OpenAI", Group: "通用模型", Operations: []string{"chat", "responses"}, ParameterBounds: map[string]any{"stream": true, "max_tokens": 16384}, Cataloged: true, Selectable: true, Testable: true, Verified: "local_fixture", VerificationScope: "LOCAL_FIXTURE", PriceSummary: "按 token（脱敏快照）", Performance: map[string]any{"latency_ms": 420, "success_rate": 0.99, "throughput": "high"}, Tags: []string{"文本", "流式"}, EndpointType: "chat"},
	{Model: "gpt-image-2", Vendor: "OpenAI", Group: "生图模型", Operations: []string{"image_generation", "image_edits"}, ParameterBounds: map[string]any{"size": []string{"1024x1024", "1536x1024", "1024x1536"}, "quality": []string{"auto", "medium", "high"}, "n": "1-4"}, Cataloged: true, Selectable: true, Testable: true, Verified: "local_fixture", VerificationScope: "LOCAL_FIXTURE", PriceSummary: "按请求（脱敏快照）", Performance: map[string]any{"latency_ms": 1800, "success_rate": 0.97, "throughput": "medium"}, Tags: []string{"图片", "generation", "edits"}, EndpointType: "images"},
	{Model: "gemini-3-pro-image-preview", Vendor: "Google", Group: "生图模型", Operations: []string{"image_generation", "image_edits"}, ParameterBounds: map[string]any{"resolution": []string{"1K", "2K"}, "quality": []string{"auto"}}, Cataloged: true, Selectable: true, Testable: true, Verified: "local_fixture", VerificationScope: "LOCAL_FIXTURE", PriceSummary: "按请求（脱敏快照）", Performance: map[string]any{"latency_ms": 2400, "success_rate": 0.95, "throughput": "medium"}, Tags: []string{"图片", "原生签名"}, EndpointType: "images"},
	{Model: "gemini-3.1-flash-image-preview", Vendor: "Google", Group: "生图模型", Operations: []string{"image_generation", "image_edits"}, ParameterBounds: map[string]any{"resolution": []string{"1K", "2K", "4K"}, "quality": []string{"auto"}}, Cataloged: true, Selectable: true, Testable: true, Verified: "local_fixture", VerificationScope: "LOCAL_FIXTURE", PriceSummary: "按请求（脱敏快照）", Performance: map[string]any{"latency_ms": 1300, "success_rate": 0.98, "throughput": "high"}, Tags: []string{"图片", "快速"}, EndpointType: "images"},
	{Model: "minimax-h3", Vendor: "MiniMax", Group: "视频模型", Operations: []string{"video_generation"}, ParameterBounds: map[string]any{"duration_seconds": []int{5, 6, 10, 15}, "resolution": []string{"1K", "2K"}}, Cataloged: true, Selectable: true, Testable: true, Verified: "local_fixture", VerificationScope: "LOCAL_FIXTURE", PriceSummary: "按请求（脱敏快照）", Performance: map[string]any{"latency_ms": 5200, "success_rate": 0.94, "throughput": "queue"}, Tags: []string{"视频", "异步"}, EndpointType: "video"},
	{Model: "glm-5.2", Vendor: "智谱", Group: "国产模型", Operations: []string{"chat", "responses"}, ParameterBounds: map[string]any{"stream": true, "max_tokens": 32768}, Cataloged: true, Selectable: true, Testable: false, Verified: "unknown", VerificationScope: "LOCAL_FIXTURE", PriceSummary: "按 token（脱敏快照）", Performance: map[string]any{"latency_ms": nil, "success_rate": nil, "throughput": "unknown"}, Tags: []string{"文本", "国产"}, EndpointType: "chat"},
	{Model: "kimi-k3", Vendor: "月之暗面", Group: "国产模型", Operations: []string{"chat"}, ParameterBounds: map[string]any{"stream": true, "max_tokens": 32768}, Cataloged: true, Selectable: true, Testable: false, Verified: "unknown", VerificationScope: "LOCAL_FIXTURE", PriceSummary: "按 token（脱敏快照）", Performance: map[string]any{"latency_ms": nil, "success_rate": nil, "throughput": "unknown"}, Tags: []string{"文本", "国产"}, EndpointType: "chat"},
	{Model: "deepseek-v4-pro", Vendor: "DeepSeek", Group: "国产模型", Operations: []string{"chat", "responses"}, ParameterBounds: map[string]any{"stream": true, "max_tokens": 32768}, Cataloged: true, Selectable: true, Testable: false, Verified: "unknown", VerificationScope: "LOCAL_FIXTURE", PriceSummary: "按 token（脱敏快照）", Performance: map[string]any{"latency_ms": nil, "success_rate": nil, "throughput": "unknown"}, Tags: []string{"文本", "国产"}, EndpointType: "chat"},
}

func localModelCatalog() []LocalModelCatalogItem {
	items := make([]LocalModelCatalogItem, 0, len(localModelCatalogDefinitions))
	for _, definition := range localModelCatalogDefinitions {
		for _, operation := range definition.Operations {
			items = append(items, LocalModelCatalogItem{
				Model:             definition.Model,
				Vendor:            definition.Vendor,
				Group:             definition.Group,
				Operation:         operation,
				ParameterBounds:   definition.ParameterBounds,
				Cataloged:         definition.Cataloged,
				Selectable:        definition.Selectable,
				Testable:          definition.Testable,
				Verified:          definition.Verified,
				VerificationScope: definition.VerificationScope,
				PriceSummary:      definition.PriceSummary,
				Performance:       definition.Performance,
				Tags:              definition.Tags,
				EndpointType:      definition.EndpointType,
			})
		}
	}
	return items
}

func isLoopbackWorkbenchHost(host string) bool {
	if host == "" {
		return false
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// GetLocalModelCatalog exposes only the fixed local snapshot when explicitly
// enabled. With the flag absent, this route is unavailable instead of
// silently becoming a production catalog endpoint.
func GetLocalModelCatalog(c *gin.Context) {
	if os.Getenv("MODEL_WORKBENCH_LOCAL_FIXTURE") != "true" || !isLoopbackWorkbenchHost(c.Request.Host) {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "local fixture disabled"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": localModelCatalog(), "scope": "LOCAL_FIXTURE"})
}
