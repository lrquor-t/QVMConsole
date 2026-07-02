package lxc

import "testing"

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
