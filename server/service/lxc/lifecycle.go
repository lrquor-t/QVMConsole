package lxc

import (
	"kvm_console/model"
	"kvm_console/utils"
)

// StartContainer 启动容器并回填运行态字段。
func StartContainer(name string) error {
	res := utils.ExecCommandLongRunning("lxc-start", "-n", name)
	if res.Error != nil {
		return res.Error
	}
	_ = RefreshRuntimeFields(name)
	return nil
}

// StopContainer 停止容器。
func StopContainer(name string) error {
	res := utils.ExecCommandLongRunning("lxc-stop", "-n", name)
	if res.Error != nil {
		return res.Error
	}
	return updateStatus(name, "STOPPED")
}

// RestartContainer 重启容器（先停后启，忽略未运行错误）。
func RestartContainer(name string) error {
	_ = StopContainer(name)
	return StartContainer(name)
}

// DestroyContainer 先停后删，并清理缓存行。
func DestroyContainer(name string) error {
	// 先停后删
	_ = utils.ExecCommandQuiet("lxc-stop", "-n", name).Error
	res := utils.ExecCommandLongRunning("lxc-destroy", "-n", name)
	if res.Error != nil {
		return res.Error
	}
	model.DB.Where("name = ?", name).Delete(&model.LXCCache{})
	return nil
}

func updateStatus(name, status string) error {
	return model.DB.Model(&model.LXCCache{}).Where("name = ?", name).Update("status", status).Error
}
