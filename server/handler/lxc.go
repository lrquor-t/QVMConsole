package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"kvm_console/service"
)

// ListLXCContainers 列出当前用户可见的 LXC 容器。
func ListLXCContainers(c *gin.Context) {
	username, _ := c.Get("username")
	role, _ := c.Get("role")
	rows, err := service.LXCListContainers(username.(string), role == "admin")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "获取容器列表失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": rows})
}

// GetLXCDetail 获取容器详情。
func GetLXCDetail(c *gin.Context) {
	name := c.Param("name")
	d, err := service.LXCGetContainerDetail(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "获取容器详情失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": d})
}
