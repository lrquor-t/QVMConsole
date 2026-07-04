package lxc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"kvm_console/config"
	"kvm_console/logger"
	"kvm_console/model"
	"kvm_console/service/lxc/template"
	"kvm_console/utils"
)

// CreateContainerParams 异步创建容器参数（task.Params JSON）。
type CreateContainerParams struct {
	Name            string `json:"name"`
	Template        string `json:"template"`
	OwnerUsername   string `json:"owner_username"`
	Remark          string `json:"remark"`
	GroupName       string `json:"group_name"`
	CPUShares       int    `json:"cpu_shares"`
	MemoryMB        int    `json:"memory_mb"`
	Autostart       bool   `json:"autostart"`
	SwitchID        uint   `json:"switch_id"`
	SecurityGroupID uint   `json:"security_group_id"`
}

// ParseCreateContainerParams 反序列化任务参数 JSON。
func ParseCreateContainerParams(s string) (*CreateContainerParams, error) {
	var p CreateContainerParams
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

var containerNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

func validateContainerName(name string) error {
	if name == "" {
		return errors.New("容器名称不能为空")
	}
	if isReservedName(name) {
		return errors.New("名称使用了保留前缀")
	}
	if !containerNameRE.MatchString(name) {
		return errors.New("名称只能含小写字母、数字、连字符，2-63 字符")
	}
	return nil
}

func isReservedName(name string) bool {
	prefix := config.GlobalConfig.LXCBasePrefix
	return len(name) > len(prefix) && name[:len(prefix)] == prefix
}

// CreateContainer 由模板克隆创建容器（异步任务调用）。progress 上报进度。
func CreateContainer(params *CreateContainerParams, progress func(int, string)) error {
	if progress == nil {
		progress = func(int, string) {}
	}
	if err := validateContainerName(params.Name); err != nil {
		return err
	}
	if params.OwnerUsername == "" {
		params.OwnerUsername = "admin"
	}
	tpl, err := template.GetTemplate(params.Template)
	if err != nil {
		return err
	}
	if tpl.Disabled {
		return errors.New("模板已禁用")
	}
	progress(10, "校验完成，开始克隆")

	// 克隆（按 backing 分支）：zfs 走手动 clone（CoW）+ 改 config；dir/overlay 走 lxc-copy。
	progress(20, "克隆容器（"+tpl.Backing+"）")
	if err := cloneContainer(params.Name, tpl); err != nil {
		return err
	}
	progress(50, "克隆完成，写入配置")

	// 由容器名派生 per-container MAC，保证 DB 行与容器 config 一致（findVethByMAC 据此关联）。
	mac := genMacByName(params.Name)

	// 覆写克隆 config：per-clone cgroup/autostart/mac
	if err := applyCloneConfig(params, mac); err != nil {
		_ = DestroyContainer(params.Name)
		return err
	}

	// 写缓存行
	row := model.LXCCache{
		Name:          params.Name,
		OwnerUsername: params.OwnerUsername,
		Status:        "STOPPED",
		Template:      params.Template,
		CPUShares:     params.CPUShares,
		MemoryMB:      params.MemoryMB,
		Backing:       tpl.Backing,
		Autostart:     params.Autostart,
		Remark:        params.Remark,
		GroupName:     params.GroupName,
		MacAddress:    mac,
		Present:       true,
	}
	if err := model.DB.Create(&row).Error; err != nil {
		_ = DestroyContainer(params.Name)
		return fmt.Errorf("保存容器记录失败: %w", err)
	}

	progress(80, "启动容器")
	// 创建后默认启动，便于分配 IP。
	if err := StartContainer(params.Name); err != nil {
		logger.App.Warn("容器启动失败（已创建，保持停止态）", "name", params.Name, "error", err)
	}
	progress(90, "接入 VPC 网络")
	if err := AttachContainerToVPC(params.Name, params.SwitchID, params.SecurityGroupID); err != nil {
		logger.App.Warn("容器 VPC 接入失败", "name", params.Name, "error", err)
	}
	// VPC 接入后回填运行态 veth/ip 到缓存。
	_ = RefreshRuntimeFields(params.Name)

	// 可选 PostCreateCommand
	if tpl.PostCreateCommand != "" {
		_ = utils.ExecCommandQuiet("bash", "-c", "lxc-attach -n "+utils.ShellSingleQuote(params.Name)+" -- "+tpl.PostCreateCommand)
	}
	progress(100, "完成")
	return nil
}

// cloneContainer 按 backing 克隆基底。
// zfs：zfs clone <parent>/<base>@base → <parent>/<name>（mountpoint <lxcpath>/<name>），克隆继承基底
// config+rootfs（CoW），把 config 的 rootfs.path 改成 <lxcpath>/<name>/rootfs。
// dir/overlay：lxc-copy（overlay 在 LXC 5.0.2 克隆会失败，错误带 stdout）。
func cloneContainer(name string, tpl *model.LXCTemplate) error {
	lxcpath := config.GlobalConfig.LXCLxcPath
	if tpl.Backing == "zfs" {
		parent, err := ZfsResolveParent(lxcpath)
		if err != nil {
			return err
		}
		if err := zfsCloneContainer(parent, tpl.BaseContainerName, name); err != nil {
			return fmt.Errorf("zfs 克隆失败: %w", err)
		}
		// 克隆 dataset 已挂载在 <lxcpath>/<name>，config 继承自基底；改 rootfs.path 指向自己的 rootfs。
		cfgPath := filepath.Join(lxcpath, name, "config")
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			return fmt.Errorf("读克隆 config 失败: %w", err)
		}
		rewritten := rewriteRootfsPathForClone(string(data),
			filepath.Join(lxcpath, tpl.BaseContainerName, "rootfs"),
			filepath.Join(lxcpath, name, "rootfs"))
		if err := os.WriteFile(cfgPath, []byte(rewritten), 0644); err != nil {
			return fmt.Errorf("写克隆 config 失败: %w", err)
		}
		return nil
	}
	res := utils.ExecCommandLongRunning("lxc-copy", "-n", tpl.BaseContainerName, "-N", name, "-B", tpl.Backing)
	if res.Error != nil {
		return fmt.Errorf("克隆失败: %w (lxc-copy stdout: %q)", res.Error, res.Stdout)
	}
	return nil
}

func applyCloneConfig(p *CreateContainerParams, mac string) error {
	cfg := filepath.Join(config.GlobalConfig.LXCLxcPath, p.Name, "config")
	// 追加覆盖项（lxc 配置后值覆盖前值）
	lines := []string{
		"lxc.cgroup2.cpu.weight = " + itoaDefault(p.CPUShares, 256),
		"lxc.cgroup2.memory.max = " + memMax(p.MemoryMB),
		"lxc.start.auto = " + autoVal(p.Autostart),
		"lxc.net.0.hwaddr = " + mac,
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	f, err := openForAppend(cfg)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// 以下小工具仅 create 流程使用；共享工具（genMacByName/openForAppend/
// RefreshRuntimeFields/findVethByMAC）见 command.go。
func itoaDefault(v, def int) string {
	if v <= 0 {
		v = def
	}
	return fmt.Sprintf("%d", v)
}

func memMax(mb int) string {
	if mb <= 0 {
		return "512M"
	}
	return fmt.Sprintf("%dM", mb)
}

func autoVal(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
