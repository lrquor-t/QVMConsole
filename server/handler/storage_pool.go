package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"kvm_console/model"
	"kvm_console/service"
	"kvm_console/taskqueue"
)

// GetStoragePoolList 获取宿主机硬盘存储池列表
func GetStoragePoolList(c *gin.Context) {
	pools, err := service.ListStoragePools()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "获取存储池列表失败: " + err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": pools})
}

// GetStoragePoolDetail 获取单个宿主机硬盘详情
func GetStoragePoolDetail(c *gin.Context) {
	id := c.Param("id")
	pool, err := service.GetStoragePool(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": pool})
}

// GetAllISOs 获取全局 ISO（聚合）
func GetAllISOs(c *gin.Context) {
	isos, err := service.GetAllISOs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "获取 ISO 列表失败: " + err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": isos})
}

// UpdateStoragePoolConfig 更新显示名称和启用状态
func UpdateStoragePoolConfig(c *gin.Context) {
	id := c.Param("id")
	var req service.UpdateHostStoragePoolConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	if err := service.UpdateHostStoragePoolConfig(id, req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "存储池配置已更新"})
}

// SetDefaultStoragePool 设置默认虚拟机存储位置
func SetDefaultStoragePool(c *gin.Context) {
	id := c.Param("id")
	if err := service.SetDefaultHostStoragePool(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "已设为默认存储位置"})
}

// FormatMountStoragePool 提交格式化并挂载任务
func FormatMountStoragePool(c *gin.Context) {
	if !requireHighRiskVerification(c, "format_storage_pool") {
		return
	}
	id := c.Param("id")
	var req struct {
		FSType string `json:"fstype"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		// 兼容旧版本不带 body 的请求
		req.FSType = ""
	}
	if req.FSType == "" {
		req.FSType = "ext4"
	}
	username, _ := c.Get("username")
	usernameStr, _ := username.(string)
	task, err := taskqueue.SubmitWithStruct(model.TaskTypeStorageFormat, gin.H{"id": id, "fstype": req.FSType}, usernameStr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "提交格式化任务失败: " + err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "格式化并挂载任务已提交",
		"data":    gin.H{"task_id": task.ID},
	})
}

// CreateStoragePartition 提交创建分区任务
func CreateStoragePartition(c *gin.Context) {
	if !requireHighRiskVerification(c, "create_storage_partition") {
		return
	}
	id := c.Param("id")
	var req struct {
		SizeGB int `json:"size_gb"` // 分区大小(GB)，0 表示使用全部剩余空间
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	username, _ := c.Get("username")
	usernameStr, _ := username.(string)
	task, err := taskqueue.SubmitWithStruct(model.TaskTypeStorageCreatePartition, gin.H{
		"id":      id,
		"size_gb": req.SizeGB,
	}, usernameStr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "提交创建分区任务失败: " + err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "创建分区任务已提交",
		"data":    gin.H{"task_id": task.ID},
	})
}

// DeleteStoragePartitions 提交删除所有分区任务
func DeleteStoragePartitions(c *gin.Context) {
	if !requireHighRiskVerification(c, "delete_storage_partitions") {
		return
	}
	id := c.Param("id")
	username, _ := c.Get("username")
	usernameStr, _ := username.(string)
	task, err := taskqueue.SubmitWithStruct(model.TaskTypeStorageDeletePartitions, gin.H{"id": id}, usernameStr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "提交删除分区任务失败: " + err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "删除分区任务已提交",
		"data":    gin.H{"task_id": task.ID},
	})
}

// GetAvailablePVTargets 获取可供 LVM 使用的磁盘列表
func GetAvailablePVTargets(c *gin.Context) {
	pools, err := service.GetAvailablePVTargets()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "获取可用磁盘列表失败: " + err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": pools})
}

// CreateStorageVolume 提交创建 LVM 存储卷任务
func CreateStorageVolume(c *gin.Context) {
	if !requireHighRiskVerification(c, "create_storage_volume") {
		return
	}
	var req service.LVMVolumeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误: " + err.Error()})
		return
	}
	username, _ := c.Get("username")
	usernameStr, _ := username.(string)

	paramsJSON, _ := json.Marshal(req)
	task, err := taskqueue.SubmitWithStruct(model.TaskTypeStorageCreateLVMVolume, gin.H{"params": string(paramsJSON)}, usernameStr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "提交创建存储卷任务失败: " + err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "创建 LVM 存储卷任务已提交",
		"data":    gin.H{"task_id": task.ID},
	})
}

// DeleteStorageVolume 提交删除 LVM 存储卷任务
func DeleteStorageVolume(c *gin.Context) {
	if !requireHighRiskVerification(c, "delete_storage_volume") {
		return
	}
	var req struct {
		VGName string `json:"vg_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误: " + err.Error()})
		return
	}
	if strings.TrimSpace(req.VGName) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "卷组名称不能为空"})
		return
	}
	username, _ := c.Get("username")
	usernameStr, _ := username.(string)
	task, err := taskqueue.SubmitWithStruct(model.TaskTypeStorageDeleteLVMVolume, gin.H{"vg_name": req.VGName}, usernameStr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "提交删除存储卷任务失败: " + err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "删除 LVM 存储卷任务已提交",
		"data":    gin.H{"task_id": task.ID},
	})
}

// GetVMStorageTargets 获取创建虚拟机可选存储位置
func GetVMStorageTargets(c *gin.Context) {
	role, _ := c.Get("role")
	targets, err := service.ListVMStorageTargets(role == "admin")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "获取虚拟机存储位置失败: " + err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": targets})
}
