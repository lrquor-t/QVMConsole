package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"kvm_console/service"
)

// lxcConfigFileErrCode 把 service 层错误文案映射到 HTTP 状态码。
func lxcConfigFileErrCode(err error) (int, string) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "不存在"):
		return http.StatusNotFound, msg
	case strings.Contains(msg, "请先停止"),
		strings.Contains(msg, "过大"),
		strings.Contains(msg, "无效"):
		return http.StatusBadRequest, msg
	default:
		return http.StatusInternalServerError, msg
	}
}

// GetLXCConfigFile GET /api/lxc/:name/config-file
func GetLXCConfigFile(c *gin.Context) {
	cf, err := service.LXCReadContainerConfigFile(c.Param("name"))
	if err != nil {
		code, msg := lxcConfigFileErrCode(err)
		c.JSON(code, gin.H{"code": code, "message": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": cf})
}

type setLXCConfigFileReq struct {
	Content string `json:"content"`
}

// SetLXCConfigFile PUT /api/lxc/:name/config-file（STOPPED-only，自动备份+原子写）
func SetLXCConfigFile(c *gin.Context) {
	var req setLXCConfigFileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	if err := service.LXCWriteContainerConfigFile(c.Param("name"), req.Content); err != nil {
		code, msg := lxcConfigFileErrCode(err)
		c.JSON(code, gin.H{"code": code, "message": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "配置文件已保存"})
}

// ListLXCConfigBackups GET /api/lxc/:name/config-file/backups
func ListLXCConfigBackups(c *gin.Context) {
	bs, err := service.LXCListContainerConfigBackups(c.Param("name"))
	if err != nil {
		code, msg := lxcConfigFileErrCode(err)
		c.JSON(code, gin.H{"code": code, "message": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": bs})
}

// GetLXCConfigBackup GET /api/lxc/:name/config-file/backups/:bak（读单份备份内容，admin-only）
func GetLXCConfigBackup(c *gin.Context) {
	cf, err := service.LXCReadContainerConfigBackup(c.Param("name"), c.Param("bak"))
	if err != nil {
		code, msg := lxcConfigFileErrCode(err)
		c.JSON(code, gin.H{"code": code, "message": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": cf})
}

// RestoreLXCConfigFile POST /api/lxc/:name/config-file/backups/:bak/restore（STOPPED-only）
func RestoreLXCConfigFile(c *gin.Context) {
	if err := service.LXCRestoreContainerConfigFile(c.Param("name"), c.Param("bak")); err != nil {
		code, msg := lxcConfigFileErrCode(err)
		c.JSON(code, gin.H{"code": code, "message": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "已恢复"})
}

// DeleteLXCConfigFileBackup DELETE /api/lxc/:name/config-file/backups/:bak
func DeleteLXCConfigFileBackup(c *gin.Context) {
	if err := service.LXCDeleteContainerConfigFileBackup(c.Param("name"), c.Param("bak")); err != nil {
		code, msg := lxcConfigFileErrCode(err)
		c.JSON(code, gin.H{"code": code, "message": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "已删除"})
}
