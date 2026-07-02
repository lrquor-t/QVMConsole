package handler

import (
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"

	"kvm_console/service/lxc/template"
)

type finalizeLXCTemplateReq struct {
	Name              string `json:"name" binding:"required"`
	DisplayName       string `json:"display_name"`
	Distro            string `json:"distro"`
	Release           string `json:"release"`
	Arch              string `json:"arch"`
	Description       string `json:"description"`
	SourcePath        string `json:"source_path"`        // 上传落地的临时 tarball 路径
	HostPath          string `json:"host_path"`          // 或主机绝对路径
	PostCreateCommand string `json:"post_create_command"`
}

// FinalizeLXCTemplate 由上传或主机路径的 tarball 创建模板。
func FinalizeLXCTemplate(c *gin.Context) {
	var req finalizeLXCTemplateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误: name 必填"})
		return
	}
	src := req.SourcePath
	if src == "" {
		src = req.HostPath
	}
	if src != "" && req.HostPath != "" {
		if !filepath.IsAbs(req.HostPath) {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "主机导入路径必须为绝对路径"})
			return
		}
		src = filepath.Clean(req.HostPath)
	}
	username, _ := c.Get("username")
	params := &template.ImportParams{
		Name: req.Name, DisplayName: req.DisplayName, Distro: req.Distro,
		Release: req.Release, Arch: req.Arch, Description: req.Description,
		SourcePath: src, PostCreateCommand: req.PostCreateCommand,
		OwnerUsername: username.(string),
	}
	if err := template.FinalizeImport(params); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "导入模板失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok"})
}

func ListLXCTemplates(c *gin.Context) {
	rows, err := template.ListTemplates()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "获取模板列表失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": rows})
}

func GetLXCTemplateDetail(c *gin.Context) {
	tpl, err := template.GetTemplate(c.Param("name"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": tpl})
}

func DeleteLXCTemplate(c *gin.Context) {
	if err := template.DeleteTemplate(c.Param("name")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok"})
}
