package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"kvm_console/service"
)

// ListLXCMounts GET /api/lxc/:name/mounts
func ListLXCMounts(c *gin.Context) {
	res, err := service.LXCListMounts(c.Param("name"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": res})
}

type addLXCMountReq struct {
	HostPath string `json:"host_path" binding:"required"`
	Target   string `json:"target" binding:"required"`
	ReadOnly bool   `json:"read_only"`
}

// AddLXCMount POST /api/lxc/:name/mounts
func AddLXCMount(c *gin.Context) {
	var req addLXCMountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	if err := service.LXCAddMount(c.Param("name"), service.LXCMount{
		HostPath: req.HostPath, Target: req.Target, ReadOnly: req.ReadOnly,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "目录挂载已添加"})
}

// DeleteLXCMount DELETE /api/lxc/:name/mounts?target=/mnt/data
func DeleteLXCMount(c *gin.Context) {
	target := c.Query("target")
	if target == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "缺少 target 参数"})
		return
	}
	if err := service.LXCDeleteMount(c.Param("name"), target); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "目录挂载已删除"})
}
