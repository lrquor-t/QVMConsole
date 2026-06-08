package service

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"kvm_console/utils"
)

const (
	VMBootTypeBIOS       = "bios"
	VMBootTypeUEFI       = "uefi"
	VMBootTypeUEFISecure = "uefi-secure"
)

var (
	vmBootTypeOSBlockRegexp       = regexp.MustCompile(`(?s)<os\b[^>]*>.*?</os>`)
	vmBootTypeOSOpenTagRegexp     = regexp.MustCompile(`(?m)^(\s*)<os\b([^>]*)>`)
	vmBootTypeFirmwareAttrRegexp  = regexp.MustCompile(`\s+firmware=['"][^'"]+['"]`)
	vmBootTypeFirmwareBlockRegexp = regexp.MustCompile(`(?s)\n?\s*<firmware\b[^>]*>.*?</firmware>`)
	vmBootTypeLoaderBlockRegexp   = regexp.MustCompile(`(?s)\n?\s*<loader\b[^>]*(?:/>|>.*?</loader>)`)
	vmBootTypeNVRAMBlockRegexp    = regexp.MustCompile(`(?s)\n?\s*<nvram\b[^>]*(?:/>|>.*?</nvram>)`)
	vmBootTypeSecureAttrRegexp    = regexp.MustCompile(`\s+secure=['"][^'"]+['"]`)
	vmBootTypeSecureFeatureRegexp = regexp.MustCompile(`(?is)<feature\b[^>]*name=['"]secure-boot['"][^>]*enabled=['"]yes['"][^>]*/?>|<feature\b[^>]*enabled=['"]yes['"][^>]*name=['"]secure-boot['"][^>]*/?>`)
	vmBootTypeArchRegexp          = regexp.MustCompile(`<type\b[^>]*\barch=['"]([^'"]+)['"]`)
	vmBootTypeMachineRegexp       = regexp.MustCompile(`<type\b[^>]*\bmachine=['"]([^'"]+)['"]`)
	vmBootTypeSMMRegexp           = regexp.MustCompile(`(?s)\n?\s*<smm\b[^>]*/>`)
	vmBootTypeFeaturesRegexp      = regexp.MustCompile(`(?s)<features\b[^>]*>.*?</features>`)
	vmBootTypeTypeCloseRegexp     = regexp.MustCompile(`</type>`)
)

// NormalizeVMBootType 规范化引导方式。
func NormalizeVMBootType(bootType string) string {
	switch strings.ToLower(strings.TrimSpace(bootType)) {
	case VMBootTypeBIOS:
		return VMBootTypeBIOS
	case VMBootTypeUEFI:
		return VMBootTypeUEFI
	case VMBootTypeUEFISecure:
		return VMBootTypeUEFISecure
	default:
		return ""
	}
}

// ParseVMBootTypeFromDomainXML 从 domain XML 中解析当前引导方式。
func ParseVMBootTypeFromDomainXML(xmlContent string) string {
	xmlContent = strings.TrimSpace(xmlContent)
	if xmlContent == "" {
		return ""
	}
	if !strings.Contains(xmlContent, "firmware='efi'") && !strings.Contains(xmlContent, `firmware="efi"`) {
		return VMBootTypeBIOS
	}
	if vmBootTypeSecureFeatureRegexp.MatchString(xmlContent) || vmBootTypeSecureAttrRegexp.MatchString(xmlContent) {
		return VMBootTypeUEFISecure
	}
	return VMBootTypeUEFI
}

// ParseVMArchFromDomainXML 从 domain XML 中解析架构。
func ParseVMArchFromDomainXML(xmlContent string) string {
	matches := vmBootTypeArchRegexp.FindStringSubmatch(xmlContent)
	if len(matches) < 2 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(matches[1]))
}

// ParseVMMachineTypeFromDomainXML 从 domain XML 中解析并归一化机器类型。
func ParseVMMachineTypeFromDomainXML(xmlContent string) string {
	matches := vmBootTypeMachineRegexp.FindStringSubmatch(xmlContent)
	if len(matches) < 2 {
		return ""
	}
	return normalizeVMMachineType(matches[1])
}

func normalizeVMMachineType(machine string) string {
	value := strings.ToLower(strings.TrimSpace(machine))
	switch {
	case strings.Contains(value, "q35"):
		return "q35"
	case strings.Contains(value, "i440fx"):
		return "i440fx"
	case strings.HasPrefix(value, "virt"):
		return "virt"
	default:
		return value
	}
}

func resolveVMNVRAMPath(name, xmlContent string) string {
	if path := strings.TrimSpace(extractDomainNVRAMPath(xmlContent)); path != "" {
		return path
	}
	cleanName := strings.TrimSpace(name)
	if cleanName == "" {
		cleanName = "vm"
	}
	return fmt.Sprintf("/var/lib/libvirt/qemu/nvram/%s_VARS.fd", cleanName)
}

func resolveOVMFLoaderPath(secure bool) string {
	candidates := []string{
		"/usr/share/OVMF/OVMF_CODE_4M.fd",
		"/usr/share/OVMF/OVMF_CODE.fd",
	}
	fallback := "/usr/share/OVMF/OVMF_CODE_4M.fd"
	if secure {
		candidates = []string{
			"/usr/share/OVMF/OVMF_CODE_4M.ms.fd",
			"/usr/share/OVMF/OVMF_CODE_4M.secboot.fd",
			"/usr/share/OVMF/OVMF_CODE.secboot.fd",
		}
		fallback = "/usr/share/OVMF/OVMF_CODE_4M.ms.fd"
	}
	return pickFirstExistingPath(candidates, fallback)
}

func resolveOVMFVarsTemplatePath(secure bool) string {
	candidates := []string{
		"/usr/share/OVMF/OVMF_VARS_4M.fd",
		"/usr/share/OVMF/OVMF_VARS.fd",
	}
	fallback := "/usr/share/OVMF/OVMF_VARS_4M.fd"
	if secure {
		candidates = []string{
			"/usr/share/OVMF/OVMF_VARS_4M.ms.fd",
			"/usr/share/OVMF/OVMF_VARS.ms.fd",
			"/usr/share/OVMF/OVMF_VARS_4M.secboot.fd",
		}
		fallback = "/usr/share/OVMF/OVMF_VARS_4M.ms.fd"
	}
	return pickFirstExistingPath(candidates, fallback)
}

func pickFirstExistingPath(candidates []string, fallback string) string {
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return fallback
}

func replaceOSOpenTagFirmware(osBlock string, useEFI bool) string {
	return vmBootTypeOSOpenTagRegexp.ReplaceAllStringFunc(osBlock, func(tag string) string {
		matches := vmBootTypeOSOpenTagRegexp.FindStringSubmatch(tag)
		if len(matches) < 3 {
			return tag
		}
		indent := matches[1]
		attrs := vmBootTypeFirmwareAttrRegexp.ReplaceAllString(matches[2], "")
		attrs = strings.TrimSpace(attrs)
		if useEFI {
			if attrs == "" {
				attrs = "firmware='efi'"
			} else {
				attrs += " firmware='efi'"
			}
		}
		if attrs == "" {
			return indent + "<os>"
		}
		return indent + "<os " + attrs + ">"
	})
}

func buildUEFIFirmwareXML(secure bool, loaderPath, varsTemplate, nvramPath string) string {
	lines := []string{}
	if secure {
		lines = append(lines,
			"    <firmware>",
			"      <feature enabled='yes' name='enrolled-keys'/>",
			"      <feature enabled='yes' name='secure-boot'/>",
			"    </firmware>",
		)
	}
	loaderAttrs := " readonly='yes' type='pflash'"
	if secure {
		loaderAttrs = " readonly='yes' secure='yes' type='pflash'"
	}
	lines = append(lines,
		fmt.Sprintf("    <loader%s>%s</loader>", loaderAttrs, loaderPath),
		fmt.Sprintf("    <nvram template='%s' templateFormat='raw' format='qcow2'>%s</nvram>", varsTemplate, nvramPath),
	)
	return strings.Join(lines, "\n")
}

func insertUEFIFirmwareXML(osBlock, firmwareXML string) string {
	if strings.TrimSpace(firmwareXML) == "" {
		return osBlock
	}
	if vmBootTypeTypeCloseRegexp.MatchString(osBlock) {
		return vmBootTypeTypeCloseRegexp.ReplaceAllString(osBlock, "</type>\n"+firmwareXML)
	}
	if strings.Contains(osBlock, "</os>") {
		return strings.Replace(osBlock, "</os>", firmwareXML+"\n  </os>", 1)
	}
	return osBlock
}

func ensureVMSecureBootSMM(xmlContent string) string {
	if vmBootTypeSMMRegexp.MatchString(xmlContent) {
		return vmBootTypeSMMRegexp.ReplaceAllStringFunc(xmlContent, func(node string) string {
			if strings.Contains(node, "state='on'") || strings.Contains(node, `state="on"`) {
				return node
			}
			node = strings.ReplaceAll(node, `state="off"`, `state="on"`)
			node = strings.ReplaceAll(node, `state='off'`, `state='on'`)
			if !strings.Contains(node, "state='") && !strings.Contains(node, `state="`) {
				node = strings.Replace(node, "/>", " state='on'/>", 1)
			}
			return node
		})
	}
	if vmBootTypeFeaturesRegexp.MatchString(xmlContent) {
		return vmBootTypeFeaturesRegexp.ReplaceAllStringFunc(xmlContent, func(block string) string {
			return strings.Replace(block, "</features>", "    <smm state='on'/>\n  </features>", 1)
		})
	}
	featuresXML := "  <features>\n    <smm state='on'/>\n  </features>\n"
	switch {
	case strings.Contains(xmlContent, "<clock "):
		return strings.Replace(xmlContent, "<clock ", featuresXML+"  <clock ", 1)
	case strings.Contains(xmlContent, "<clock>"):
		return strings.Replace(xmlContent, "<clock>", featuresXML+"  <clock>", 1)
	case strings.Contains(xmlContent, "<devices/>"):
		return strings.Replace(xmlContent, "<devices/>", featuresXML+"  <devices/>", 1)
	case strings.Contains(xmlContent, "<devices />"):
		return strings.Replace(xmlContent, "<devices />", featuresXML+"  <devices />", 1)
	case strings.Contains(xmlContent, "<devices>"):
		return strings.Replace(xmlContent, "<devices>", featuresXML+"  <devices>", 1)
	case strings.Contains(xmlContent, "<on_poweroff>"):
		return strings.Replace(xmlContent, "<on_poweroff>", featuresXML+"  <on_poweroff>", 1)
	default:
		return xmlContent
	}
}

// ApplyVMBootTypeToDomainXML 将引导方式写入 domain XML。
func ApplyVMBootTypeToDomainXML(name, xmlContent, bootType string) (string, error) {
	normalized := NormalizeVMBootType(bootType)
	if normalized == "" {
		return "", fmt.Errorf("不支持的引导方式: %s", bootType)
	}

	arch := ParseVMArchFromDomainXML(xmlContent)
	machineType := ParseVMMachineTypeFromDomainXML(xmlContent)
	if normalized == VMBootTypeBIOS && arch == "aarch64" {
		return "", fmt.Errorf("ARM 架构虚拟机不支持 BIOS 引导")
	}
	if normalized == VMBootTypeUEFISecure {
		if arch == "aarch64" || arch == "riscv64" {
			return "", fmt.Errorf("当前架构暂不支持 UEFI 安全引导")
		}
		if machineType == "i440fx" {
			return "", fmt.Errorf("i440fx 机型不支持 UEFI 安全引导")
		}
	}

	osBlock := vmBootTypeOSBlockRegexp.FindString(xmlContent)
	if strings.TrimSpace(osBlock) == "" {
		return "", fmt.Errorf("未找到虚拟机的 <os> 配置段")
	}

	cleanedOS := vmBootTypeFirmwareBlockRegexp.ReplaceAllString(osBlock, "")
	cleanedOS = vmBootTypeLoaderBlockRegexp.ReplaceAllString(cleanedOS, "")
	cleanedOS = vmBootTypeNVRAMBlockRegexp.ReplaceAllString(cleanedOS, "")
	cleanedOS = replaceOSOpenTagFirmware(cleanedOS, normalized != VMBootTypeBIOS)

	if normalized != VMBootTypeBIOS {
		secure := normalized == VMBootTypeUEFISecure
		nvramPath := resolveVMNVRAMPath(name, xmlContent)
		loaderPath := resolveOVMFLoaderPath(secure)
		varsTemplate := resolveOVMFVarsTemplatePath(secure)
		cleanedOS = insertUEFIFirmwareXML(cleanedOS, buildUEFIFirmwareXML(secure, loaderPath, varsTemplate, nvramPath))
	}

	updated := strings.Replace(xmlContent, osBlock, cleanedOS, 1)
	if normalized == VMBootTypeUEFISecure {
		updated = ensureVMSecureBootSMM(updated)
	}
	return updated, nil
}

func ensureVMUEFINVRAMFile(name, xmlContent, bootType string) error {
	normalized := NormalizeVMBootType(bootType)
	if normalized != VMBootTypeUEFI && normalized != VMBootTypeUEFISecure {
		return nil
	}

	nvramPath := resolveVMNVRAMPath(name, xmlContent)
	if nvramPath == "" {
		return fmt.Errorf("未找到可用的 UEFI NVRAM 路径")
	}
	if _, err := os.Stat(nvramPath); err == nil {
		if detectQemuImageFormat(nvramPath) == "qcow2" {
			return nil
		}
		return convertExistingNVRAMToQCOW2(nvramPath)
	}

	templatePath := resolveOVMFVarsTemplatePath(normalized == VMBootTypeUEFISecure)
	if err := createQCOW2NVRAMFromTemplate(templatePath, nvramPath); err != nil {
		return fmt.Errorf("创建 UEFI NVRAM 文件失败: %w", err)
	}
	return nil
}

func detectQemuImageFormat(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	result := utils.ExecCommand("qemu-img", "info", "-U", "--output=json", path)
	if result.Error != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parseQemuInfoStr(result.Stdout, "format")))
}

func createQCOW2NVRAMFromTemplate(templatePath, nvramPath string) error {
	templatePath = strings.TrimSpace(templatePath)
	nvramPath = strings.TrimSpace(nvramPath)
	if templatePath == "" || nvramPath == "" {
		return fmt.Errorf("NVRAM 模板路径或目标路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(nvramPath), 0755); err != nil {
		return fmt.Errorf("创建 UEFI NVRAM 目录失败: %w", err)
	}
	sourceFormat := detectQemuImageFormat(templatePath)
	if sourceFormat == "" {
		sourceFormat = "raw"
	}
	_ = os.Remove(nvramPath)
	result := utils.ExecCommand("qemu-img", "convert", "-f", sourceFormat, "-O", "qcow2", templatePath, nvramPath)
	if result.Error != nil {
		return fmt.Errorf("转换 NVRAM 为 qcow2 失败: %s", firstNonEmpty(result.Stderr, result.Error.Error()))
	}
	fixResult := utils.ExecShell(fmt.Sprintf(
		"chmod 600 %s && (chown libvirt-qemu:kvm %s 2>/dev/null || chown qemu:qemu %s 2>/dev/null || true)",
		utils.ShellSingleQuote(nvramPath),
		utils.ShellSingleQuote(nvramPath),
		utils.ShellSingleQuote(nvramPath),
	))
	if fixResult.Error != nil {
		return fmt.Errorf("设置 NVRAM 文件权限失败: %s", firstNonEmpty(fixResult.Stderr, fixResult.Error.Error()))
	}
	return nil
}

func convertExistingNVRAMToQCOW2(nvramPath string) error {
	nvramPath = strings.TrimSpace(nvramPath)
	if nvramPath == "" {
		return fmt.Errorf("NVRAM 路径为空")
	}
	if detectQemuImageFormat(nvramPath) == "qcow2" {
		return nil
	}
	tmpPath := nvramPath + ".qcow2.tmp"
	backupPath := nvramPath + ".raw.bak"
	for i := 1; ; i++ {
		if _, err := os.Stat(backupPath); os.IsNotExist(err) {
			break
		}
		backupPath = fmt.Sprintf("%s.raw.bak.%d", nvramPath, i)
	}
	_ = os.Remove(tmpPath)
	result := utils.ExecCommand("qemu-img", "convert", "-f", "raw", "-O", "qcow2", nvramPath, tmpPath)
	if result.Error != nil {
		return fmt.Errorf("转换 NVRAM 为 qcow2 失败: %s", firstNonEmpty(result.Stderr, result.Error.Error()))
	}
	if err := os.Rename(nvramPath, backupPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("备份原 NVRAM 文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, nvramPath); err != nil {
		_ = os.Rename(backupPath, nvramPath)
		_ = os.Remove(tmpPath)
		return fmt.Errorf("替换 NVRAM 文件失败: %w", err)
	}
	fixResult := utils.ExecShell(fmt.Sprintf(
		"chmod 600 %s && (chown libvirt-qemu:kvm %s 2>/dev/null || chown qemu:qemu %s 2>/dev/null || true)",
		utils.ShellSingleQuote(nvramPath),
		utils.ShellSingleQuote(nvramPath),
		utils.ShellSingleQuote(nvramPath),
	))
	if fixResult.Error != nil {
		return fmt.Errorf("设置 NVRAM 文件权限失败: %s", firstNonEmpty(fixResult.Stderr, fixResult.Error.Error()))
	}
	return nil
}

func domainUsesPflashNVRAM(xmlContent string) bool {
	return strings.Contains(xmlContent, "type='pflash'") ||
		strings.Contains(xmlContent, `type="pflash"`)
}

func extractDomainNVRAMFormat(xmlContent string) string {
	matches := regexp.MustCompile(`(?s)<nvram\b([^>]*)>`).FindStringSubmatch(xmlContent)
	if len(matches) < 2 {
		return ""
	}
	attrMatches := regexp.MustCompile(`\bformat=['"]([^'"]+)['"]`).FindStringSubmatch(matches[1])
	if len(attrMatches) < 2 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(attrMatches[1]))
}

func setDomainNVRAMFormat(xmlContent, format string) string {
	format = strings.TrimSpace(format)
	if strings.TrimSpace(xmlContent) == "" || format == "" {
		return xmlContent
	}
	re := regexp.MustCompile(`(?s)<nvram\b([^>]*)>`)
	return re.ReplaceAllStringFunc(xmlContent, func(tag string) string {
		attrRe := regexp.MustCompile(`\bformat=['"][^'"]+['"]`)
		if attrRe.MatchString(tag) {
			return attrRe.ReplaceAllString(tag, "format='"+format+"'")
		}
		return strings.Replace(tag, "<nvram", "<nvram format='"+format+"'", 1)
	})
}

// SetVMBootType 修改虚拟机引导方式。该操作要求虚拟机关机后执行。
func SetVMBootType(name, bootType string) error {
	normalized := NormalizeVMBootType(bootType)
	if normalized == "" {
		return fmt.Errorf("不支持的引导方式: %s", bootType)
	}

	stateResult := utils.ExecCommand("virsh", "domstate", name)
	if stateResult.Error != nil {
		return fmt.Errorf("获取虚拟机状态失败: %s", stateResult.Stderr)
	}
	state := strings.TrimSpace(stateResult.Stdout)
	if state == "running" || state == "paused" {
		return fmt.Errorf("请先关机后再修改引导方式")
	}

	xmlResult := utils.ExecCommand("virsh", "dumpxml", name, "--inactive")
	if xmlResult.Error != nil {
		return fmt.Errorf("获取虚拟机 XML 失败: %s", xmlResult.Stderr)
	}

	currentBootType := ParseVMBootTypeFromDomainXML(xmlResult.Stdout)
	if currentBootType == normalized {
		return nil
	}
	if err := ensureVMUEFINVRAMFile(name, xmlResult.Stdout, normalized); err != nil {
		return err
	}

	updatedXML, err := ApplyVMBootTypeToDomainXML(name, xmlResult.Stdout, normalized)
	if err != nil {
		return err
	}
	if err := SetVMInactiveDomainXML(name, updatedXML); err != nil {
		return fmt.Errorf("设置引导方式失败: %w", err)
	}
	return nil
}
