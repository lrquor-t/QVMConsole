package service

import (
	"fmt"
	"regexp"
	"strings"

	"kvm_console/utils"
)

const (
	VMVideoModelVirtio = "virtio"
	VMVideoModelVGA    = "vga"
	VMVideoModelVMVGA  = "vmvga"
	VMVideoModelCirrus = "cirrus"
)

var (
	vmVideoBlockRegexp       = regexp.MustCompile(`(?s)<video>.*?</video>`)
	vmVideoModelRegexp       = regexp.MustCompile(`<video\b[^>]*>\s*<model\b[^>]*type=['"]([^'"]+)['"]`)
	vmHyperVBlockRegexp      = regexp.MustCompile(`(?s)<hyperv\b[^>]*>.*?</hyperv>`)
	vmClockBlockRegexp       = regexp.MustCompile(`(?s)<clock\b[^>]*(?:/>|>.*?</clock>)`)
	vmSelfClosingClockRegexp = regexp.MustCompile(`^<clock\b[^>]*/>$`)
	vmHyperVClockTimerRegexp = regexp.MustCompile(`<timer\b[^>]*\bname=['"]hypervclock['"][^>]*/>`)
)

// ResolveVMVideoModel 规范化视频模型，并根据系统类型给出默认值。
func ResolveVMVideoModel(videoModel, osType string) string {
	normalized := strings.ToLower(strings.TrimSpace(videoModel))
	switch normalized {
	case VMVideoModelVirtio, VMVideoModelVGA, VMVideoModelVMVGA, VMVideoModelCirrus:
		return normalized
	}

	if strings.EqualFold(strings.TrimSpace(osType), "windows") {
		return VMVideoModelVGA
	}
	return VMVideoModelVirtio
}

// ParseVMVideoModelFromDomainXML 从 domain XML 中解析当前视频模型。
func ParseVMVideoModelFromDomainXML(xmlStr string) string {
	matches := vmVideoModelRegexp.FindStringSubmatch(xmlStr)
	if len(matches) < 2 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(matches[1]))
}

func renderVMVideoBlock(videoModel string) string {
	modelXML := "<model type='vga'/>"
	switch ResolveVMVideoModel(videoModel, "") {
	case VMVideoModelVirtio:
		modelXML = "<model type='virtio' heads='1' primary='yes'/>"
	case VMVideoModelVMVGA:
		modelXML = "<model type='vmvga'/>"
	case VMVideoModelCirrus:
		modelXML = "<model type='cirrus'/>"
	}

	return fmt.Sprintf("    <video>\n      %s\n    </video>", modelXML)
}

func renderWindowsHyperVBlock() string {
	return `    <hyperv mode='custom'>
      <relaxed state='on'/>
      <vapic state='on'/>
      <spinlocks state='on' retries='8191'/>
      <vpindex state='on'/>
      <runtime state='on'/>
      <synic state='on'/>
      <stimer state='on'>
        <direct state='on'/>
      </stimer>
      <frequencies state='on'/>
      <tlbflush state='on'>
        <direct state='on'/>
        <extended state='on'/>
      </tlbflush>
      <ipi state='on'/>
    </hyperv>`
}

func ensureWindowsHyperVClockTimer(xmlStr string) string {
	if vmHyperVClockTimerRegexp.MatchString(xmlStr) {
		return xmlStr
	}

	const timerTag = "<timer name='hypervclock' present='yes'/>"

	if vmClockBlockRegexp.MatchString(xmlStr) {
		return vmClockBlockRegexp.ReplaceAllStringFunc(xmlStr, func(clockBlock string) string {
			indent := leadingWhitespace(clockBlock)
			childIndent := indent + "  "
			trimmed := strings.TrimSpace(clockBlock)

			if vmSelfClosingClockRegexp.MatchString(trimmed) {
				openTag := strings.TrimSuffix(trimmed, "/>")
				openTag = strings.TrimRight(openTag, " ")
				return fmt.Sprintf("%s%s>\n%s%s\n%s</clock>", indent, openTag, childIndent, timerTag, indent)
			}

			closingLine := "\n" + indent + "</clock>"
			insertedClosingLine := "\n" + childIndent + timerTag + closingLine
			if strings.Contains(clockBlock, closingLine) {
				return strings.Replace(clockBlock, closingLine, insertedClosingLine, 1)
			}
			return strings.Replace(clockBlock, "</clock>", "\n"+childIndent+timerTag+"\n"+indent+"</clock>", 1)
		})
	}

	clockXML := "  <clock offset='localtime'>\n    " + timerTag + "\n  </clock>\n"
	if strings.Contains(xmlStr, "<on_poweroff>") {
		return strings.Replace(xmlStr, "<on_poweroff>", clockXML+"  <on_poweroff>", 1)
	}
	if strings.Contains(xmlStr, "<devices>") {
		return strings.Replace(xmlStr, "<devices>", clockXML+"  <devices>", 1)
	}
	if strings.Contains(xmlStr, "</features>") {
		return strings.Replace(xmlStr, "</features>", "</features>\n"+clockXML, 1)
	}
	return xmlStr
}

// ApplyVMVideoModelToDomainXML 将视频模型写入 domain XML。
func ApplyVMVideoModelToDomainXML(xmlStr, videoModel, osType string) string {
	block := renderVMVideoBlock(ResolveVMVideoModel(videoModel, osType))
	if vmVideoBlockRegexp.MatchString(xmlStr) {
		return vmVideoBlockRegexp.ReplaceAllString(xmlStr, block)
	}
	if strings.Contains(xmlStr, "</devices>") {
		return strings.Replace(xmlStr, "</devices>", block+"\n  </devices>", 1)
	}
	return xmlStr
}

// ApplyWindowsGuestOptimizationsToDomainXML 为 Windows 来宾补充更完整的 Hyper-V 优化配置。
func ApplyWindowsGuestOptimizationsToDomainXML(xmlStr string) string {
	block := renderWindowsHyperVBlock()
	if vmHyperVBlockRegexp.MatchString(xmlStr) {
		return ensureWindowsHyperVClockTimer(vmHyperVBlockRegexp.ReplaceAllString(xmlStr, block))
	}
	if strings.Contains(xmlStr, "</features>") {
		return ensureWindowsHyperVClockTimer(strings.Replace(xmlStr, "</features>", block+"\n  </features>", 1))
	}
	return ensureWindowsHyperVClockTimer(xmlStr)
}

// SetVMVideoModel 设置虚拟机视频模型。运行中的虚拟机需要先关机后再修改。
func SetVMVideoModel(name, videoModel string) error {
	stateResult := utils.ExecCommand("virsh", "domstate", name)
	if stateResult.Error != nil {
		return fmt.Errorf("获取虚拟机状态失败: %s", stateResult.Stderr)
	}
	state := strings.TrimSpace(stateResult.Stdout)
	if state == "running" || state == "paused" {
		return fmt.Errorf("请先关机后再修改显示设备")
	}

	xmlResult := utils.ExecCommand("virsh", "dumpxml", name, "--inactive")
	if xmlResult.Error != nil {
		return fmt.Errorf("获取虚拟机 XML 失败: %s", xmlResult.Stderr)
	}

	xmlStr := xmlResult.Stdout
	osType := detectVMOSType("", xmlStr)
	xmlStr = ApplyVMVideoModelToDomainXML(xmlStr, videoModel, osType)
	if osType == "windows" {
		xmlStr = ApplyWindowsGuestOptimizationsToDomainXML(xmlStr)
	}

	xmlPath := fmt.Sprintf("/tmp/_video-%s.xml", name)
	utils.ExecShell(fmt.Sprintf("cat > %s << 'XMLEOF'\n%s\nXMLEOF", utils.ShellSingleQuote(xmlPath), xmlStr))
	defineResult := utils.ExecCommand("virsh", "define", xmlPath)
	utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(xmlPath)))
	if defineResult.Error != nil {
		return fmt.Errorf("修改显示设备失败: %s", defineResult.Stderr)
	}
	return nil
}
