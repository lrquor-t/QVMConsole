package lxc

import (
	"encoding/json"
	"fmt"

	"kvm_console/model"
	"kvm_console/utils"
)

// BatchCreateContainerParams 批量创建容器参数（task.Params JSON）。
type BatchCreateContainerParams struct {
	Prefix          string                   `json:"prefix"`
	StartNum        int                      `json:"start_num"`
	Count           int                      `json:"count"`
	OwnerUsername   string                   `json:"owner_username"`
	Source          string                   `json:"source"` // clone（默认/空）| download
	Template        string                   `json:"template"`
	Distro          string                   `json:"distro"`
	Release         string                   `json:"release"`
	Arch            string                   `json:"arch"`
	Remark          string                   `json:"remark"`
	GroupName       string                   `json:"group_name"`
	CPUShares       int                      `json:"cpu_shares"`
	MemoryMB        int                      `json:"memory_mb"`
	DiskLimitGB     int                      `json:"disk_limit_gb"`
	Autostart       bool                     `json:"autostart"`
	SwitchID        uint                     `json:"switch_id"`
	SecurityGroupID uint                     `json:"security_group_id"`
	ExtraNics       []AddLXCInterfaceRequest `json:"extra_nics"`
}

// LXCBatchResult 单个容器的批量创建结果。
type LXCBatchResult struct {
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
}

// BatchName 生成批量容器名：prefix-NN（2 位补零）。
// 预检/创建/前端预览共用此函数，杜绝格式漂移。
func BatchName(prefix string, n int) string {
	return fmt.Sprintf("%s-%02d", prefix, n)
}

// ParseBatchCreateContainerParams 反序列化批量创建任务参数。
func ParseBatchCreateContainerParams(s string) (*BatchCreateContainerParams, error) {
	var p BatchCreateContainerParams
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// NameExists 报告容器名是否已被占用：DB 缓存行(present) 或 lxc-info 命中。
func NameExists(name string) bool {
	var count int64
	model.DB.Model(&model.LXCCache{}).Where("name = ? AND present = ?", name, true).Count(&count)
	if count > 0 {
		return true
	}
	// lxc-info 退出码 0 = 容器存在
	if res := utils.ExecCommandQuiet("lxc-info", "-n", name); res.ExitCode == 0 {
		return true
	}
	return false
}
