package controller

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

// GetUserImage2Capabilities returns only customer-safe Image2 combinations for
// the requested authorized group. It intentionally omits channel IDs, names,
// provider details, health, and routing order.
func GetUserImage2Capabilities(c *gin.Context) {
	modelName := strings.TrimSpace(c.Query("model"))
	if !strings.EqualFold(modelName, "gpt-image-2") {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Image2 capability lookup requires model=gpt-image-2",
		})
		return
	}

	userID := c.GetInt("id")
	authorizedGroups, err := service.GetUserSelectableTokenGroups(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "无法读取当前用户分组授权",
		})
		return
	}

	requestedGroup := strings.TrimSpace(c.Query("group"))
	if requestedGroup == "" {
		requestedGroup = "auto"
	}
	groups := make([]string, 0, 1)
	if requestedGroup == "auto" {
		for _, candidate := range setting.GetAutoGroups() {
			if _, ok := authorizedGroups[candidate]; ok {
				groups = append(groups, candidate)
			}
		}
	} else if _, ok := authorizedGroups[requestedGroup]; ok {
		groups = append(groups, requestedGroup)
	} else {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "无权访问该分组",
		})
		return
	}

	channels := make([]*model.Channel, 0)
	for _, group := range groups {
		groupChannels, channelErr := model.GetImage2Channels(group, modelName)
		if channelErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"message": "无法读取 Image2 能力配置",
			})
			return
		}
		channels = append(channels, groupChannels...)
	}

	catalog := service.BuildImage2CapabilityCatalog(requestedGroup, modelName, channels)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    catalog,
	})
}
