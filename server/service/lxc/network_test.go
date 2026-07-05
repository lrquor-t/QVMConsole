package lxc

import (
	"testing"

	"kvm_console/model"
)

// TestParseLxcInfoIP 已在 command_test.go 覆盖；此处测 host veth 名解析。
func TestFindVethByMACFromText(t *testing.T) {
	in := "1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 link/loopback 00:00:00:00:00:00 brd 00:00:00:00:00:00\n" +
		"5: vethABC@if4: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 state UP mode DEFAULT group default qlen 1000 link/ether 02:11:22:33:44:55 brd ff:ff:ff:ff:ff:ff link-netnsid 0\n"
	got := findVethByMACFromText(in, "02:11:22:33:44:55")
	if got != "vethABC" {
		t.Fatalf("veth = %q, want vethABC", got)
	}
	// case-insensitive + no match → ""
	if findVethByMACFromText(in, "02:11:22:33:44:99") != "" {
		t.Fatalf("expected empty for non-matching mac")
	}
}

// TestNicMACForBindingFallbackToDerived 覆盖纯逻辑回退路径：
// config 文件不存在时，nicMACForBinding 应回退到 NICMAC 派生（确保
// ReconcileContainerNICs 在容器 config 不可读时仍能用确定性 MAC 找到 veth）。
func TestNicMACForBindingFallbackToDerived(t *testing.T) {
	// 无 config 文件时回退到 NICMAC 派生
	got := nicMACForBinding("__not_exist__", model.VPCVMBinding{InterfaceOrder: 2})
	if got != NICMAC("__not_exist__", 2) {
		t.Fatalf("回退到 NICMAC 失败: %s", got)
	}
}
