package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/codefoxasync"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// ImageBatchContentProxy serves one completed CodeFox batch item without
// exposing the provider host, upstream task id, or upstream API key.
func ImageBatchContentProxy(c *gin.Context) {
	publicTaskID := strings.TrimSpace(c.Param("task_id"))
	itemIndex, err := strconv.Atoi(c.Param("item_index"))
	if publicTaskID == "" || err != nil || itemIndex < 0 {
		videoProxyError(c, http.StatusBadRequest, "invalid_request_error", "valid task_id and item_index are required")
		return
	}
	task, exists, err := model.GetByTaskId(c.GetInt("id"), publicTaskID)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to query image batch task %s: %s", publicTaskID, err.Error()))
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to query task")
		return
	}
	if !exists || task == nil || task.Platform != constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeCodeFoxAsync)) {
		videoProxyError(c, http.StatusNotFound, "invalid_request_error", "Task not found")
		return
	}
	if task.Status != model.TaskStatusSuccess {
		videoProxyError(c, http.StatusBadRequest, "invalid_request_error", "Task is not completed")
		return
	}
	providerTask, err := codefoxasync.ParseBatchTaskResponse(task.Data)
	if err != nil {
		videoProxyError(c, http.StatusBadGateway, "server_error", "Task result is unavailable")
		return
	}
	found := false
	for _, item := range providerTask.Results {
		if item.ItemIndex == itemIndex {
			found = true
			break
		}
	}
	if !found {
		videoProxyError(c, http.StatusNotFound, "invalid_request_error", "Image item not found")
		return
	}
	ch, err := model.CacheGetChannel(task.ChannelId)
	if err != nil || ch.Type != constant.ChannelTypeCodeFoxAsync {
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to retrieve channel information")
		return
	}
	key := strings.TrimSpace(task.PrivateData.Key)
	if key == "" {
		key = strings.TrimSpace(ch.Key)
	}
	baseURL := strings.TrimSpace(ch.GetBaseURL())
	if key == "" || baseURL == "" {
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Task credentials are unavailable")
		return
	}
	httpClient, err := service.GetHttpClientWithProxy(ch.GetSetting().Proxy)
	if err != nil {
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to create proxy client")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	client := &codefoxasync.Client{BaseURL: baseURL, APIKey: key, HTTPClient: httpClient}
	resp, err := client.FetchImage(ctx, task.GetUpstreamTaskID(), itemIndex)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to fetch image batch item for public task %s", publicTaskID))
		videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to fetch image content")
		return
	}
	defer resp.Body.Close()
	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		videoProxyError(c, http.StatusBadGateway, "server_error", "Upstream returned non-image content")
		return
	}
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "private, max-age=86400")
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, resp.Body); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to stream image batch item for task %s: %s", publicTaskID, err.Error()))
	}
}
