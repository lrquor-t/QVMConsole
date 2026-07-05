package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"kvm_console/model"
	"kvm_console/service"
	"kvm_console/taskqueue"
)

type createLXCReq struct {
	Name            string `json:"name" binding:"required"`
	Template        string `json:"template"`
	Remark          string `json:"remark"`
	GroupName       string `json:"group_name"`
	CPUShares       int    `json:"cpu_shares"`
	MemoryMB        int    `json:"memory_mb"`
	Autostart       bool   `json:"autostart"`
	SwitchID        uint   `json:"switch_id"`
	SecurityGroupID uint   `json:"security_group_id"`
	Source          string `json:"source"` // clone（默认/空）| download
	Distro          string `json:"distro"`
	Release         string `json:"release"`
	Arch            string `json:"arch"`
	ExtraNics       []service.LXCAddInterfaceRequest `json:"extra_nics"`
}

// CreateLXCContainer 提交异步创建容器任务（克隆模板金基底）。
func CreateLXCContainer(c *gin.Context) {
	var req createLXCReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误: name 必填"})
		return
	}
	source := req.Source
	if source == "" {
		source = "clone"
	}
	switch source {
	case "clone":
		if req.Template == "" {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "克隆模式必须选择模板"})
			return
		}
	case "download":
		if strings.TrimSpace(req.Distro) == "" || strings.TrimSpace(req.Release) == "" || strings.TrimSpace(req.Arch) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "镜像下载模式必须填写发行版/版本/架构"})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "未知来源: " + req.Source})
		return
	}
	username, _ := c.Get("username")
	role, _ := c.Get("role")
	if role != "admin" {
		if err := service.LXCCheckQuota(username.(string), req.CPUShares, req.MemoryMB); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
	}
	params := &service.LXCCreateContainerParams{
		Name:            req.Name,
		Template:        req.Template,
		OwnerUsername:   username.(string),
		Remark:          req.Remark,
		GroupName:       req.GroupName,
		CPUShares:       req.CPUShares,
		MemoryMB:        req.MemoryMB,
		Autostart:       req.Autostart,
		SwitchID:        req.SwitchID,
		SecurityGroupID: req.SecurityGroupID,
		Source:          source,
		Distro:          req.Distro,
		Release:         req.Release,
		Arch:            req.Arch,
		ExtraNics:       req.ExtraNics,
	}
	task, err := taskqueue.SubmitWithStruct(model.TaskTypeLXCCreate, params, username.(string))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "提交创建任务失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "创建任务已提交", "data": gin.H{"task_id": task.ID}})
}

type operateLXCReq struct {
	Action string `json:"action" binding:"required"` // start|stop|restart
}

// OperateLXC 单容器生命周期操作（start/stop/restart）。
func OperateLXC(c *gin.Context) {
	name := c.Param("name")
	var req operateLXCReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	var err error
	switch req.Action {
	case "start":
		err = service.LXCStartContainer(name)
	case "stop":
		err = service.LXCStopContainer(name)
	case "restart":
		err = service.LXCRestartContainer(name)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "未知操作: " + req.Action})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok"})
}

// DeleteLXCContainer 提交异步销毁容器任务（大 rootfs 可能耗时）。
func DeleteLXCContainer(c *gin.Context) {
	name := c.Param("name")
	username, _ := c.Get("username")
	// task.Params 为容器名的 JSON 字符串（SubmitWithStruct(string) → "name"）。
	task, err := taskqueue.SubmitWithStruct(model.TaskTypeLXCDestroy, name, username.(string))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "提交删除任务失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "删除任务已提交", "data": gin.H{"task_id": task.ID}})
}

type batchLXCReq struct {
	Names  []string `json:"names" binding:"required"`
	Action string   `json:"action" binding:"required"` // start|stop|restart|delete
}

// BatchOperateLXC 批量生命周期操作。
func BatchOperateLXC(c *gin.Context) {
	var req batchLXCReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	failed := map[string]string{}
	for _, n := range req.Names {
		var err error
		switch req.Action {
		case "start":
			err = service.LXCStartContainer(n)
		case "stop":
			err = service.LXCStopContainer(n)
		case "restart":
			err = service.LXCRestartContainer(n)
		case "delete":
			err = service.LXCDestroyContainer(n)
		default:
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "未知操作: " + req.Action})
			return
		}
		if err != nil {
			failed[n] = err.Error()
		}
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": gin.H{"failed": failed}})
}

// GetLXCContainerIP 返回容器当前 IP。
func GetLXCContainerIP(c *gin.Context) {
	d, err := service.LXCGetContainerDetail(c.Param("name"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": gin.H{"ip": d.IP}})
}
