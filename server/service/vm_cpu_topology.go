package service

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"kvm_console/utils"
)

const (
	VMCPUTopologyAuto         = "auto"
	VMCPUTopologySingleSocket = "single_socket"
	VMCPUTopologyHostDefault  = "host_default"
)

var (
	vmCPUBlockRegexp       = regexp.MustCompile(`(?s)<cpu\b[^>]*(?:/>|>.*?</cpu>)`)
	vmCPUTopologyRegexp    = regexp.MustCompile(`(?s)<topology\b[^>]*/>`)
	vmVCPUValueRegexp      = regexp.MustCompile(`(?s)<vcpu\b[^>]*>\s*([0-9]+)\s*</vcpu>`)
	vmSelfClosingCPUExpr   = regexp.MustCompile(`^<cpu\b[^>]*/>$`)
	vmTopologySocketsRegex = regexp.MustCompile(`\bsockets=['"]([0-9]+)['"]`)
	vmTopologyCoresRegex   = regexp.MustCompile(`\bcores=['"]([0-9]+)['"]`)
	vmTopologyThreadsRegex = regexp.MustCompile(`\bthreads=['"]([0-9]+)['"]`)
)

// NormalizeVMCPUTopologyMode 规范化 CPU 拓扑模式。
func NormalizeVMCPUTopologyMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case VMCPUTopologySingleSocket, VMCPUTopologyHostDefault:
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return VMCPUTopologyAuto
	}
}

// ApplyCPUTopologyModeToDomainXML 按模式写入 domain XML 的 CPU 拓扑。
func ApplyCPUTopologyModeToDomainXML(xmlStr, mode, osType string, vcpu int) string {
	switch NormalizeVMCPUTopologyMode(mode) {
	case VMCPUTopologySingleSocket:
		return ApplyWindowsCPUTopologyToDomainXML(xmlStr, vcpu)
	case VMCPUTopologyHostDefault:
		return RemoveCPUTopologyFromDomainXML(xmlStr)
	default:
		if strings.EqualFold(strings.TrimSpace(osType), "windows") {
			return ApplyWindowsCPUTopologyToDomainXML(xmlStr, vcpu)
		}
		return xmlStr
	}
}

// RemoveCPUTopologyFromDomainXML 移除显式 CPU 拓扑，让 libvirt/QEMU 使用默认拓扑。
func RemoveCPUTopologyFromDomainXML(xmlStr string) string {
	return vmCPUTopologyRegexp.ReplaceAllString(xmlStr, "")
}

// ParseVMCPUTopologyModeFromDomainXML 从 domain XML 中识别可回填的 CPU 拓扑模式。
func ParseVMCPUTopologyModeFromDomainXML(xmlStr string) string {
	topology := vmCPUTopologyRegexp.FindString(xmlStr)
	if strings.TrimSpace(topology) == "" {
		return VMCPUTopologyAuto
	}
	sockets := parseTopologyAttr(topology, vmTopologySocketsRegex)
	cores := parseTopologyAttr(topology, vmTopologyCoresRegex)
	threads := parseTopologyAttr(topology, vmTopologyThreadsRegex)
	vcpu := ParseVCPUCountFromDomainXML(xmlStr)
	if sockets == 1 && threads == 1 && (vcpu <= 0 || cores == vcpu) {
		return VMCPUTopologySingleSocket
	}
	return VMCPUTopologyHostDefault
}

// ApplyWindowsCPUTopologyToDomainXML 将 Windows 来宾的 vCPU 暴露为单插槽多核心。
func ApplyWindowsCPUTopologyToDomainXML(xmlStr string, vcpu int) string {
	if vcpu <= 0 {
		vcpu = ParseVCPUCountFromDomainXML(xmlStr)
	}
	if vcpu <= 0 {
		return xmlStr
	}

	topology := fmt.Sprintf("<topology sockets='1' dies='1' cores='%d' threads='1'/>", vcpu)
	if vmCPUBlockRegexp.MatchString(xmlStr) {
		return vmCPUBlockRegexp.ReplaceAllStringFunc(xmlStr, func(cpuBlock string) string {
			return applyTopologyToCPUBlock(cpuBlock, topology)
		})
	}

	cpuBlock := fmt.Sprintf("  <cpu mode='host-passthrough' check='none' migratable='on'>\n    %s\n  </cpu>", topology)
	if strings.Contains(xmlStr, "</features>") {
		return strings.Replace(xmlStr, "</features>", "</features>\n"+cpuBlock, 1)
	}
	if strings.Contains(xmlStr, "<devices>") {
		return strings.Replace(xmlStr, "<devices>", cpuBlock+"\n  <devices>", 1)
	}
	return xmlStr
}

// ParseVCPUCountFromDomainXML 从 domain XML 中读取 vCPU 数量。
func ParseVCPUCountFromDomainXML(xmlStr string) int {
	matches := vmVCPUValueRegexp.FindStringSubmatch(xmlStr)
	if len(matches) < 2 {
		return 0
	}
	value, err := strconv.Atoi(strings.TrimSpace(matches[1]))
	if err != nil {
		return 0
	}
	return value
}

func applyTopologyToCPUBlock(cpuBlock, topology string) string {
	trimmed := strings.TrimSpace(cpuBlock)
	if vmSelfClosingCPUExpr.MatchString(trimmed) {
		openTag := strings.TrimSuffix(trimmed, "/>")
		openTag = strings.TrimRight(openTag, " ")
		indent := leadingWhitespace(cpuBlock)
		return fmt.Sprintf("%s>\n%s  %s\n%s</cpu>", openTag, indent, topology, indent)
	}

	if vmCPUTopologyRegexp.MatchString(cpuBlock) {
		return vmCPUTopologyRegexp.ReplaceAllString(cpuBlock, topology)
	}
	if strings.Contains(cpuBlock, "</cpu>") {
		indent := leadingWhitespace(cpuBlock)
		return strings.Replace(cpuBlock, "</cpu>", "  "+topology+"\n"+indent+"</cpu>", 1)
	}
	return cpuBlock
}

func leadingWhitespace(value string) string {
	for i, r := range value {
		if r != ' ' && r != '\t' {
			return value[:i]
		}
	}
	return ""
}

func parseTopologyAttr(topology string, pattern *regexp.Regexp) int {
	matches := pattern.FindStringSubmatch(topology)
	if len(matches) < 2 {
		return 0
	}
	value, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0
	}
	return value
}

// setVMCPUWithTopologySync 设置虚拟机 vCPU 数量，同时同步 CPU topology。
// 当 domain XML 中存在 topology 时，必须同时修改 vcpu 和 topology 后 define，
// 因为 virsh setvcpus 和 virsh define 都会校验 sockets×dies×cores×threads == vcpu。
// 不存在 topology 时，使用 virsh setvcpus 命令。
func setVMCPUWithTopologySync(name string, vcpu int) error {
	xmlResult := utils.ExecCommand("virsh", "dumpxml", name, "--inactive")
	if xmlResult.Error != nil {
		return fmt.Errorf("获取虚拟机 XML 失败: %s", xmlResult.Stderr)
	}
	xmlStr := xmlResult.Stdout

	hasTopology := vmCPUTopologyRegexp.MatchString(xmlStr)

	if !hasTopology {
		// 无 topology，使用传统的 virsh setvcpus 命令
		result := utils.ExecCommand("virsh", "setvcpus", name, strconv.Itoa(vcpu), "--config", "--maximum")
		if result.Error != nil {
			return fmt.Errorf("设置 CPU 最大值失败: %s", result.Stderr)
		}
		result = utils.ExecCommand("virsh", "setvcpus", name, strconv.Itoa(vcpu), "--config")
		if result.Error != nil {
			return fmt.Errorf("设置 CPU 失败: %s", result.Stderr)
		}
		return nil
	}

	// 有 topology：同时修改 vcpu 和 topology，然后 define
	xmlStr = vmVCPUValueRegexp.ReplaceAllString(xmlStr, fmt.Sprintf("<vcpu placement='static'>%d</vcpu>", vcpu))

	mode := ParseVMCPUTopologyModeFromDomainXML(xmlStr)
	osType := detectVMOSType("", xmlStr)
	xmlStr = ApplyCPUTopologyModeToDomainXML(xmlStr, mode, osType, vcpu)

	// 兜底：如果 ApplyCPUTopologyModeToDomainXML 未修改 topology（auto 模式非 Windows），
	// 需要直接按单插槽拓扑更新 cores 以保证 sockets×cores×threads == vcpu
	if vmCPUTopologyRegexp.MatchString(xmlStr) {
		topology := vmCPUTopologyRegexp.FindString(xmlStr)
		sockets := parseTopologyAttr(topology, vmTopologySocketsRegex)
		threads := parseTopologyAttr(topology, vmTopologyThreadsRegex)
		if sockets <= 0 {
			sockets = 1
		}
		if threads <= 0 {
			threads = 1
		}
		if sockets*threads > 0 && sockets*parseTopologyAttr(topology, vmTopologyCoresRegex)*threads != vcpu {
			cores := vcpu / (sockets * threads)
			if cores > 0 && sockets*cores*threads == vcpu {
				newTopology := fmt.Sprintf("<topology sockets='%d' dies='1' cores='%d' threads='%d'/>", sockets, cores, threads)
				xmlStr = vmCPUTopologyRegexp.ReplaceAllString(xmlStr, newTopology)
			}
		}
	}

	xmlPath := fmt.Sprintf("/tmp/_cpu-topology-sync-%s.xml", name)
	utils.ExecShell(fmt.Sprintf("cat > %s << 'XMLEOF'\n%s\nXMLEOF", utils.ShellSingleQuote(xmlPath), xmlStr))
	defineResult := utils.ExecCommand("virsh", "define", xmlPath)
	utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(xmlPath)))
	if defineResult.Error != nil {
		return fmt.Errorf("设置 CPU 失败: %s", defineResult.Stderr)
	}
	return nil
}

// SetVMCPUTopologyMode 设置虚拟机 CPU 拓扑模式。运行中的虚拟机需要先关机后再修改。
func SetVMCPUTopologyMode(name, mode string) error {
	stateResult := utils.ExecCommand("virsh", "domstate", name)
	if stateResult.Error != nil {
		return fmt.Errorf("获取虚拟机状态失败: %s", stateResult.Stderr)
	}
	state := strings.TrimSpace(stateResult.Stdout)
	if state == "running" || state == "paused" {
		return fmt.Errorf("请先关机后再修改 CPU 拓扑")
	}

	xmlResult := utils.ExecCommand("virsh", "dumpxml", name, "--inactive")
	if xmlResult.Error != nil {
		return fmt.Errorf("获取虚拟机 XML 失败: %s", xmlResult.Stderr)
	}

	xmlStr := xmlResult.Stdout
	osType := detectVMOSType("", xmlStr)
	updated := ApplyCPUTopologyModeToDomainXML(xmlStr, mode, osType, ParseVCPUCountFromDomainXML(xmlStr))

	xmlPath := fmt.Sprintf("/tmp/_cpu-topology-%s.xml", name)
	utils.ExecShell(fmt.Sprintf("cat > %s << 'XMLEOF'\n%s\nXMLEOF", utils.ShellSingleQuote(xmlPath), updated))
	defineResult := utils.ExecCommand("virsh", "define", xmlPath)
	utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(xmlPath)))
	if defineResult.Error != nil {
		return fmt.Errorf("修改 CPU 拓扑失败: %s", defineResult.Stderr)
	}
	return nil
}
