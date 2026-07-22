package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"kvm_console/model"
	"kvm_console/service"
)

// healthCheckReq 健康检查规则的新增/更新请求体。
type healthCheckReq struct {
	CheckName    string `json:"check_name"`
	Type         string `json:"type"`          // http/tcp/script
	Target       string `json:"target"`
	ExpectedCode int    `json:"expected_code"`
	Critical     bool   `json:"critical"`
	Enabled      bool   `json:"enabled"`
}

// ListLXCHealthChecks 列出容器全部健康检查规则。
func ListLXCHealthChecks(c *gin.Context) {
	list, err := service.LXCListHealthChecks(c.Param("name"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": list})
}

// AddLXCHealthCheck 新增健康检查规则。
// script 类型在容器内执行任意命令（lxc-attach），仅管理员可配置；http/tcp 任意 owner 可配。
// 当前用户取值方式与 handler/lxc_portforward.go（AddLXCPortForward）一致：c.Get("role"/"username")。
func AddLXCHealthCheck(c *gin.Context) {
	var req healthCheckReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	role, _ := c.Get("role")
	username, _ := c.Get("username")
	roleStr := strings.TrimSpace(fmt.Sprint(role))
	usernameStr := strings.TrimSpace(fmt.Sprint(username))
	if req.Type == "script" && roleStr != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "命令式检查仅管理员可配置"})
		return
	}
	h := &model.LXCHealthCheck{
		Name:         c.Param("name"),
		CheckName:    req.CheckName,
		Type:         req.Type,
		Target:       req.Target,
		ExpectedCode: req.ExpectedCode,
		Critical:     req.Critical,
		Enabled:      req.Enabled,
		CreatedBy:    usernameStr,
	}
	if h.ExpectedCode == 0 {
		h.ExpectedCode = 200
	}
	if err := service.LXCCreateHealthCheck(h); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": h})
}

// UpdateLXCHealthCheck 更新健康检查规则。
// script 类型同样限管理员（与 AddLXCHealthCheck 一致），防止 owner 把已有 http 规则改成 script 绕过限制。
func UpdateLXCHealthCheck(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "id 参数错误"})
		return
	}
	var req healthCheckReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	role, _ := c.Get("role")
	roleStr := strings.TrimSpace(fmt.Sprint(role))
	if req.Type == "script" && roleStr != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "命令式检查仅管理员可配置"})
		return
	}
	h, err := service.LXCGetHealthCheck(uint(id), c.Param("name"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "规则不存在"})
		return
	}
	h.CheckName = req.CheckName
	h.Type = req.Type
	h.Target = req.Target
	h.ExpectedCode = req.ExpectedCode
	h.Critical = req.Critical
	h.Enabled = req.Enabled
	if err := service.LXCUpdateHealthCheck(h); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok"})
}

// DeleteLXCHealthCheck 删除单条健康检查规则（service 层校验 id+container 归属）。
func DeleteLXCHealthCheck(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "id 参数错误"})
		return
	}
	if err := service.LXCDeleteHealthCheck(uint(id), c.Param("name")); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "规则不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok"})
}

// GetLXCHealth 取容器的聚合健康状态 + 各项明细。
// 聚合状态从 LXCCache.HealthStatus 读（后台调度器写入），明细为规则最新一次探测结果。
func GetLXCHealth(c *gin.Context) {
	list, err := service.LXCListHealthChecks(c.Param("name"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	agg := ""
	var lastHealthAt time.Time
	if cache, err := service.LXCGetCacheByName(c.Param("name")); err == nil {
		agg = cache.HealthStatus
		lastHealthAt = cache.LastHealthAt
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "ok",
		"data": gin.H{
			"status":     agg,
			"checks":     list,
			"checked_at": lastHealthAt,
		},
	})
}

// ProbeLXCHealth 手动立即探测一次（同步），返回最新聚合状态。
func ProbeLXCHealth(c *gin.Context) {
	status, err := service.LXCRunHealthProbe(c.Param("name"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "ok",
		"data":    gin.H{"status": status},
	})
}
