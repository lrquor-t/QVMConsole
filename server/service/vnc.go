package service

import (
	"fmt"
	"strings"

	"kvm_console/utils"
)

// VncInfo VNC 状态信息
type VncInfo struct {
	Enabled  bool   `json:"enabled"`
	Port     string `json:"port"`
	Auth     string `json:"auth"`
	Password bool   `json:"has_password"`
	Socket   string `json:"socket,omitempty"`
	Exposed  bool   `json:"exposed"` // 是否对外暴露（监听 0.0.0.0）
}

// GetVncStatus 获取 VNC 状态
func GetVncStatus(vmName string) (*VncInfo, error) {
	info := &VncInfo{}

	state := utils.ExecCommand("virsh", "domstate", vmName)
	if state.Error != nil {
		return nil, fmt.Errorf("虚拟机不存在: %s", vmName)
	}

	vmState := strings.TrimSpace(state.Stdout)
	if vmState != "running" && vmState != "paused" {
		// 从 XML 配置检查是否有 VNC
		xmlResult := utils.ExecCommand("virsh", "dumpxml", "--inactive", vmName)
		if xmlResult.Error == nil {
			info.Enabled = strings.Contains(xmlResult.Stdout, "graphics type='vnc'")
		}
		return info, nil
	}

	// 运行中，通过 QEMU Monitor 获取详情
	vncResult := utils.ExecCommand("virsh", "qemu-monitor-command", vmName, "--hmp", "info vnc")
	if vncResult.Error == nil {
		info.Enabled = strings.Contains(vncResult.Stdout, "Server:")

		// 解析端口和监听地址
		for _, line := range strings.Split(vncResult.Stdout, "\n") {
			if strings.Contains(line, "Server:") {
				parts := strings.Split(line, ":")
				if len(parts) >= 3 {
					info.Port = strings.TrimSpace(parts[len(parts)-1])
				}
				// 检测是否对外暴露（监听 0.0.0.0）
				if strings.Contains(line, "0.0.0.0") {
					info.Exposed = true
				}
			}
			if strings.Contains(line, "Auth:") {
				authParts := strings.SplitN(line, ":", 2)
				if len(authParts) >= 2 {
					info.Auth = strings.TrimSpace(authParts[1])
					info.Password = info.Auth == "vnc"
				}
			}
		}
	} else {
		// 从 XML 检查暴露状态
		xmlResult := utils.ExecCommand("virsh", "dumpxml", "--inactive", vmName)
		if xmlResult.Error == nil && strings.Contains(xmlResult.Stdout, "listen='0.0.0.0'") {
			info.Exposed = true
		}
	}

	return info, nil
}

// EnableVnc 开启 VNC（使用 TCP 本地模式，绑定 127.0.0.1 仅本机可访问）
func EnableVnc(vmName, password string) error {
	stateResult := utils.ExecCommand("virsh", "domstate", vmName)
	if stateResult.Error != nil {
		return fmt.Errorf("虚拟机不存在: %s", vmName)
	}
	state := strings.TrimSpace(stateResult.Stdout)

	tmpXML := fmt.Sprintf("/tmp/vnc_setup_%s.xml", vmName)

	// 直接导出 XML 到文件（避免 echo 管道的单引号问题）
	if state == "running" {
		utils.ExecShell(fmt.Sprintf("virsh dumpxml %s > %s", utils.ShellSingleQuote(vmName), utils.ShellSingleQuote(tmpXML)))
	} else {
		utils.ExecShell(fmt.Sprintf("virsh dumpxml --inactive %s > %s", utils.ShellSingleQuote(vmName), utils.ShellSingleQuote(tmpXML)))
	}

	// 构建 graphics 属性（TCP 绑定 127.0.0.1，仅本机可访问，通过后端 WebSocket 代理）
	var graphicsXML string
	if password != "" {
		graphicsXML = fmt.Sprintf(
			"<graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1' passwd='%s'>\\n      <listen type='address' address='127.0.0.1'/>\\n    </graphics>",
			password)
	} else {
		graphicsXML = "<graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'>\\n      <listen type='address' address='127.0.0.1'/>\\n    </graphics>"
	}

	// 检查文件内容判断是替换还是新增
	checkResult := utils.ExecShell(fmt.Sprintf("grep -c \"graphics type='vnc'\" %s", utils.ShellSingleQuote(tmpXML)))
	if strings.TrimSpace(checkResult.Stdout) != "0" {
		// 替换现有 VNC 配置
		utils.ExecShell(fmt.Sprintf(
			`sed -i "/<graphics type='vnc'/,/<\/graphics>/c\    %s" %s`,
			graphicsXML, utils.ShellSingleQuote(tmpXML)))
	} else {
		// 新增 VNC 配置
		utils.ExecShell(fmt.Sprintf(
			`sed -i "/<\/devices>/i\    %s" %s`,
			graphicsXML, utils.ShellSingleQuote(tmpXML)))
	}

	// 保留已有视频模型；如果当前没有视频设备，则按系统类型补一个默认值
	xmlContent := utils.ExecShell(fmt.Sprintf("cat %s", utils.ShellSingleQuote(tmpXML)))
	if xmlContent.Error == nil {
		detectedOSType := detectVMOSType("", xmlContent.Stdout)
		updatedXML := ApplyVMVideoModelToDomainXML(xmlContent.Stdout, ParseVMVideoModelFromDomainXML(xmlContent.Stdout), detectedOSType)
		utils.ExecShell(fmt.Sprintf("cat > %s << 'XMLEOF'\n%s\nXMLEOF", utils.ShellSingleQuote(tmpXML), updatedXML))
	}

	// 应用配置
	if state == "running" {
		utils.ExecCommand("virsh", "destroy", vmName)
		utils.ExecCommand("virsh", "define", tmpXML)
		if err := StartVM(vmName); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(tmpXML)))
			return err
		}
	} else {
		utils.ExecCommand("virsh", "define", tmpXML)
	}

	utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(tmpXML)))
	return nil
}

// DisableVnc 关闭 VNC
func DisableVnc(vmName string) error {
	stateResult := utils.ExecCommand("virsh", "domstate", vmName)
	if stateResult.Error != nil {
		return fmt.Errorf("虚拟机不存在: %s", vmName)
	}
	state := strings.TrimSpace(stateResult.Stdout)

	tmpXML := fmt.Sprintf("/tmp/vnc_setup_%s.xml", vmName)

	// 直接导出 XML 到文件
	if state == "running" {
		utils.ExecShell(fmt.Sprintf("virsh dumpxml %s > %s", utils.ShellSingleQuote(vmName), utils.ShellSingleQuote(tmpXML)))
	} else {
		utils.ExecShell(fmt.Sprintf("virsh dumpxml --inactive %s > %s", utils.ShellSingleQuote(vmName), utils.ShellSingleQuote(tmpXML)))
	}

	// 替换为仅本地监听
	newGraphics := "<graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'>\\n      <listen type='address' address='127.0.0.1'/>\\n    </graphics>"
	utils.ExecShell(fmt.Sprintf(
		`sed -i "/<graphics type='vnc'/,/<\/graphics>/c\    %s" %s`,
		newGraphics, utils.ShellSingleQuote(tmpXML)))

	if state == "running" {
		utils.ExecCommand("virsh", "destroy", vmName)
		utils.ExecCommand("virsh", "define", tmpXML)
		if err := StartVM(vmName); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(tmpXML)))
			return err
		}
	} else {
		utils.ExecCommand("virsh", "define", tmpXML)
	}

	utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(tmpXML)))
	return nil
}

// ChangeVncPassword 修改 VNC 密码（热修改，无需重启）
func ChangeVncPassword(vmName, newPassword string) error {
	stateResult := utils.ExecCommand("virsh", "domstate", vmName)
	if stateResult.Error != nil {
		return fmt.Errorf("虚拟机不存在: %s", vmName)
	}
	if strings.TrimSpace(stateResult.Stdout) != "running" {
		return fmt.Errorf("虚拟机未运行，无法热修改密码")
	}

	if len(newPassword) > 8 {
		newPassword = newPassword[:8]
	}

	// 热修改密码
	result := utils.ExecCommand("virsh", "qemu-monitor-command", vmName, "--hmp",
		fmt.Sprintf("set_password vnc %s", newPassword))
	if result.Error != nil {
		return fmt.Errorf("修改密码失败: %s", result.Stderr)
	}

	// 同步持久化配置
	xmlResult := utils.ExecCommand("virsh", "dumpxml", vmName)
	if xmlResult.Error == nil {
		tmpXML := fmt.Sprintf("/tmp/vnc_pwd_%s.xml", vmName)
		utils.ExecShell(fmt.Sprintf("virsh dumpxml %s > %s", utils.ShellSingleQuote(vmName), utils.ShellSingleQuote(tmpXML)))
		utils.ExecShell(fmt.Sprintf(
			`sed -i "s|passwd='[^']*'|passwd='%s'|" %s`,
			newPassword, utils.ShellSingleQuote(tmpXML)))
		utils.ExecCommand("virsh", "define", tmpXML)
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(tmpXML)))
	}

	return nil
}

// VncConnInfo VNC 连接信息（供 WebSocket 代理使用）
type VncConnInfo struct {
	Network string // "unix" 或 "tcp"
	Address string // socket 路径 或 host:port
}

// GetVncConnInfo 获取 VNC 连接信息（自动检测 Unix Socket / TCP 模式）
func GetVncConnInfo(vmName string) (*VncConnInfo, error) {
	// 1. 先从 XML 尝试获取 socket 路径
	xmlResult := utils.ExecCommand("virsh", "dumpxml", vmName)
	if xmlResult.Error != nil {
		return nil, fmt.Errorf("获取 XML 失败")
	}

	// 尝试解析 socket 路径
	for _, line := range strings.Split(xmlResult.Stdout, "\n") {
		if strings.Contains(line, "socket=") && strings.Contains(line, "vnc") {
			start := strings.Index(line, "socket='")
			if start >= 0 {
				start += len("socket='")
				end := strings.Index(line[start:], "'")
				if end >= 0 {
					socketPath := line[start : start+end]
					// 检查 socket 文件是否存在
					checkResult := utils.ExecCommand("test", "-S", socketPath)
					if checkResult.Error == nil {
						return &VncConnInfo{Network: "unix", Address: socketPath}, nil
					}
				}
			}
		}
	}

	// 2. Socket 不可用，尝试从 QEMU Monitor 获取 TCP 地址
	vncResult := utils.ExecCommand("virsh", "qemu-monitor-command", vmName, "--hmp", "info vnc")
	if vncResult.Error == nil {
		for _, line := range strings.Split(vncResult.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Server:") {
				// 格式形如 "Server: 127.0.0.1:5900 (ipv4)" 或 "Server: [::]:5900 (ipv6)"
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					addr := parts[1]
					// 去掉可能的 (ipv4)/(ipv6) 后缀
					addr = strings.TrimSpace(addr)
					if addr != "" && addr != "none" {
						return &VncConnInfo{Network: "tcp", Address: addr}, nil
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("未找到可用的 VNC 连接（请先开启 VNC）")
}

// ExposeVnc 切换 VNC 对外暴露状态（需重启虚拟机生效）
// expose=true: 监听 0.0.0.0（对外暴露）; expose=false: 监听 127.0.0.1（仅本地）
func ExposeVnc(vmName string, expose bool) error {
	stateResult := utils.ExecCommand("virsh", "domstate", vmName)
	if stateResult.Error != nil {
		return fmt.Errorf("虚拟机不存在: %s", vmName)
	}
	state := strings.TrimSpace(stateResult.Stdout)

	tmpXML := fmt.Sprintf("/tmp/vnc_expose_%s.xml", vmName)

	// 直接导出 XML 到文件
	if state == "running" {
		utils.ExecShell(fmt.Sprintf("virsh dumpxml %s > %s", utils.ShellSingleQuote(vmName), utils.ShellSingleQuote(tmpXML)))
	} else {
		utils.ExecShell(fmt.Sprintf("virsh dumpxml --inactive %s > %s", utils.ShellSingleQuote(vmName), utils.ShellSingleQuote(tmpXML)))
	}

	// 检查是否有 VNC 配置
	checkResult := utils.ExecShell(fmt.Sprintf("grep -c \"graphics type='vnc'\" %s", utils.ShellSingleQuote(tmpXML)))
	if strings.TrimSpace(checkResult.Stdout) == "0" {
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(tmpXML)))
		return fmt.Errorf("VNC 未开启，请先开启 VNC")
	}

	var listenAddr string
	if expose {
		listenAddr = "0.0.0.0"
	} else {
		listenAddr = "127.0.0.1"
	}

	// 替换 listen 地址（仅修改 graphics 和 listen 行，避免误改 mac address 等）
	utils.ExecShell(fmt.Sprintf(
		`sed -i "/<graphics type='vnc'/s|listen='[^']*'|listen='%s'|; /<listen type='address'/s|address='[^']*'|address='%s'|" %s`,
		listenAddr, listenAddr, utils.ShellSingleQuote(tmpXML)))

	// 应用配置
	if state == "running" {
		utils.ExecCommand("virsh", "destroy", vmName)
		utils.ExecCommand("virsh", "define", tmpXML)
		if err := StartVM(vmName); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(tmpXML)))
			return err
		}
	} else {
		utils.ExecCommand("virsh", "define", tmpXML)
	}

	utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(tmpXML)))
	return nil
}
