package middleware

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// BodyStorageCleanup 请求体存储清理中间件
// 在请求处理完成后自动清理磁盘/内存缓存
func BodyStorageCleanup() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 处理请求
		c.Next()

		// Multipart parsing may spill files to disk. Remove those temporary files
		// only after every downstream handler and adaptor has completed.
		if c.Request != nil && c.Request.MultipartForm != nil {
			_ = c.Request.MultipartForm.RemoveAll()
			c.Request.MultipartForm = nil
		}

		// 请求结束后清理存储
		common.CleanupBodyStorage(c)

		// 清理文件缓存（URL 下载的文件等）
		service.CleanupFileSources(c)
	}
}
