package service

import (
	"strings"
	"testing"
)

func TestApplyWindowsCPUTopologyToDomainXMLExpandsSelfClosingCPU(t *testing.T) {
	xmlContent := `<domain type='kvm'>
  <vcpu>6</vcpu>
  <cpu mode='host-passthrough' check='none' migratable='on'/>
</domain>`

	updated := ApplyWindowsCPUTopologyToDomainXML(xmlContent, 6)
	if !strings.Contains(updated, "<topology sockets='1' dies='1' cores='6' threads='1'/>") {
		t.Fatalf("未写入 Windows CPU 拓扑:\n%s", updated)
	}
	if strings.Contains(updated, "<cpu mode='host-passthrough' check='none' migratable='on'/>") {
		t.Fatalf("自闭合 CPU 节点未展开:\n%s", updated)
	}
}

func TestApplyWindowsCPUTopologyToDomainXMLReplacesExistingTopology(t *testing.T) {
	xmlContent := `<domain type='kvm'>
  <vcpu>6</vcpu>
  <cpu mode='host-passthrough'>
    <topology sockets='6' cores='1' threads='1'/>
  </cpu>
</domain>`

	updated := ApplyWindowsCPUTopologyToDomainXML(xmlContent, 6)
	if strings.Contains(updated, "sockets='6'") {
		t.Fatalf("旧 CPU 拓扑未被替换:\n%s", updated)
	}
	if !strings.Contains(updated, "<topology sockets='1' dies='1' cores='6' threads='1'/>") {
		t.Fatalf("未写入单插槽多核心拓扑:\n%s", updated)
	}
}

func TestApplyWindowsCPUTopologyToDomainXMLUsesVCPUFromXML(t *testing.T) {
	xmlContent := `<domain type='kvm'>
  <vcpu placement='static'>4</vcpu>
  <features><acpi/></features>
  <devices></devices>
</domain>`

	updated := ApplyWindowsCPUTopologyToDomainXML(xmlContent, 0)
	if !strings.Contains(updated, "<topology sockets='1' dies='1' cores='4' threads='1'/>") {
		t.Fatalf("未从 vcpu 节点推导 CPU 拓扑:\n%s", updated)
	}
}

func TestApplyCPUTopologyModeToDomainXMLHostDefaultRemovesTopology(t *testing.T) {
	xmlContent := `<domain type='kvm'>
  <vcpu>6</vcpu>
  <cpu mode='host-passthrough'>
    <topology sockets='1' dies='1' cores='6' threads='1'/>
  </cpu>
</domain>`

	updated := ApplyCPUTopologyModeToDomainXML(xmlContent, "host_default", "windows", 6)
	if strings.Contains(updated, "<topology") {
		t.Fatalf("host_default 应移除显式 CPU 拓扑:\n%s", updated)
	}
}

func TestApplyCPUTopologyModeToDomainXMLAutoUsesSingleSocketForWindows(t *testing.T) {
	xmlContent := `<domain type='kvm'>
  <vcpu>6</vcpu>
  <cpu mode='host-passthrough' check='none' migratable='on'/>
</domain>`

	updated := ApplyCPUTopologyModeToDomainXML(xmlContent, "auto", "windows", 6)
	if !strings.Contains(updated, "<topology sockets='1' dies='1' cores='6' threads='1'/>") {
		t.Fatalf("auto 模式未为 Windows 写入单插槽拓扑:\n%s", updated)
	}
}

func TestParseVMCPUTopologyModeFromDomainXMLSingleSocketWithDies(t *testing.T) {
	xmlContent := `<domain type='kvm'>
  <vcpu>8</vcpu>
  <cpu mode='host-passthrough'>
    <topology sockets='1' dies='1' cores='8' threads='1'/>
  </cpu>
</domain>`

	mode := ParseVMCPUTopologyModeFromDomainXML(xmlContent)
	if mode != VMCPUTopologySingleSocket {
		t.Fatalf("dies=1 应识别为单插槽模式，实际: %s", mode)
	}
}

func TestParseVMCPUTopologyModeFromDomainXMLMultiDiesNotSingleSocket(t *testing.T) {
	xmlContent := `<domain type='kvm'>
  <vcpu>8</vcpu>
  <cpu mode='host-passthrough'>
    <topology sockets='1' dies='2' cores='4' threads='1'/>
  </cpu>
</domain>`

	mode := ParseVMCPUTopologyModeFromDomainXML(xmlContent)
	if mode == VMCPUTopologySingleSocket {
		t.Fatalf("dies=2 不应识别为单插槽模式")
	}
}

func TestMergeTopologyFromLiveToInactiveAddsTopology(t *testing.T) {
	inactiveXML := `<domain type='kvm'>
  <vcpu>4</vcpu>
  <features><acpi/></features>
  <devices></devices>
</domain>`

	liveXML := `<domain type='kvm'>
  <vcpu>4</vcpu>
  <cpu mode='host-passthrough'>
    <topology sockets='1' dies='1' cores='4' threads='1'/>
  </cpu>
  <features><acpi/></features>
  <devices></devices>
</domain>`

	merged := mergeTopologyFromLiveToInactive(inactiveXML, liveXML)
	if !strings.Contains(merged, "<topology sockets='1' dies='1' cores='4' threads='1'/>") {
		t.Fatalf("未将在线 topology 合并到持久化 XML:\n%s", merged)
	}
}

func TestMergeTopologyFromLiveToInactiveReplacesExistingTopology(t *testing.T) {
	inactiveXML := `<domain type='kvm'>
  <vcpu>4</vcpu>
  <cpu mode='host-passthrough'>
    <topology sockets='2' dies='1' cores='2' threads='1'/>
  </cpu>
  <devices></devices>
</domain>`

	liveXML := `<domain type='kvm'>
  <vcpu>4</vcpu>
  <cpu mode='host-passthrough'>
    <topology sockets='1' dies='1' cores='4' threads='1'/>
  </cpu>
  <devices></devices>
</domain>`

	merged := mergeTopologyFromLiveToInactive(inactiveXML, liveXML)
	if strings.Contains(merged, "sockets='2'") {
		t.Fatalf("旧 topology 未被替换:\n%s", merged)
	}
	if !strings.Contains(merged, "<topology sockets='1' dies='1' cores='4' threads='1'/>") {
		t.Fatalf("未合并在线 topology:\n%s", merged)
	}
}

func TestMergeTopologyFromLiveToInactiveNoLiveTopology(t *testing.T) {
	inactiveXML := `<domain type='kvm'>
  <vcpu>4</vcpu>
  <devices></devices>
</domain>`

	liveXML := `<domain type='kvm'>
  <vcpu>4</vcpu>
  <devices></devices>
</domain>`

	merged := mergeTopologyFromLiveToInactive(inactiveXML, liveXML)
	if merged != inactiveXML {
		t.Fatalf("无在线 topology 时不应修改 XML")
	}
}
