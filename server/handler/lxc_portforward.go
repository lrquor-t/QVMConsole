package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"kvm_console/service"
	"kvm_console/service/lxc"
	netservice "kvm_console/service/network"
)

// ListLXCPortForwards 列出容器端口映射（按容器所有可能 IP 过滤全局规则）。
func ListLXCPortForwards(c *gin.Context) {
	list, err := service.LXCListPortForwards(c.Param("name"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": list})
}

// AddLXCPortForward 新增端口映射。
// 配额校验与 VM（handler/network.go:222 AddPortForward）一致：非 admin 用户走
// max_port_forwards 用户配额（容器与 VM 共用同一份用户配额池）。
func AddLXCPortForward(c *gin.Context) {
	var req service.LXCPortForwardRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}

	role, _ := c.Get("role")
	username, _ := c.Get("username")
	roleStr := strings.TrimSpace(fmt.Sprint(role))
	usernameStr := strings.TrimSpace(fmt.Sprint(username))
	name := c.Param("name")

	// 与 VM 共用 max_port_forwards 用户配额（handler/network.go:244-267 同款逻辑）
	if role != "admin" {
		quotaDelta := portForwardProtocolCount(req.Protocol) // handler/network.go:214
		if service.IsLightweightCloudUser(usernameStr) {
			if err := service.CheckLightweightVMPortForwardQuota(usernameStr, name, quotaDelta); err != nil {
				c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": err.Error()})
				return
			}
		} else if err := netservice.CheckUserPortForwardFeatureEnabled(usernameStr); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": err.Error()})
			return
		} else if err := netservice.CheckUserPortForwardQuota(usernameStr, quotaDelta); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": err.Error()})
			return
		}
	}

	if err := service.LXCAddPortForward(name, req, usernameStr, roleStr == "admin"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok"})
}

// DeleteLXCPortForward 删除端口映射（service 层校验规则归属，防误删他者规则）。
func DeleteLXCPortForward(c *gin.Context) {
	if !requireHighRiskVerification(c, "delete_port_forward") {
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "id 参数错误"})
		return
	}
	if err := service.LXCDeletePortForward(c.Param("name"), id); err != nil {
		if errors.Is(err, lxc.ErrPortForwardNotOwned) {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok"})
}
