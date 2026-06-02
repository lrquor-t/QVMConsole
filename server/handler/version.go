package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"kvm_console/config"
)

// GetVersion 返回系统版本信息
func GetVersion(c *gin.Context) {
	// Version 变量来自 main 包，通过 ldflags 注入
	// 由于跨包引用 main.Version 不方便，这里通过接口文档 /version 内的结构体返回
	c.JSON(http.StatusOK, gin.H{
		"version":    Version,
		"build_time": BuildTime,
		"site_title": config.GlobalConfig.SiteTitle,
	})
}

// Version 通过 ldflags 在构建时注入，格式: -X kvm_console/handler.Version=v1.0.0
var Version = "dev"

// BuildTime 通过 ldflags 在构建时注入，格式: -X kvm_console/handler.BuildTime=2025-01-01T00:00:00Z
var BuildTime = ""
