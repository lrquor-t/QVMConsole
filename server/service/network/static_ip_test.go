package network

import "testing"

// firstNICMACFromSources 是 firstNICMAC 的纯逻辑核心，便于单测。
// kind="lxc" 时用 lxcMAC；否则用 vmMAC（libvirt 解析结果）。
func TestFirstNICMACDispatch(t *testing.T) {
	cases := []struct{ kind, lxcMAC, vmMAC, want string }{
		{"lxc", "02:aa:bb:cc:dd:ee", "52:54:00:00:00:01", "02:aa:bb:cc:dd:ee"},
		{"lxc", "", "52:54:00:00:00:01", ""}, // lxc 但无 MAC → 空
		{"vm", "02:aa:bb:cc:dd:ee", "52:54:00:00:00:01", "52:54:00:00:00:01"},
		{"", "02:aa:bb:cc:dd:ee", "52:54:00:00:00:01", "52:54:00:00:00:01"}, // 默认 vm
	}
	for _, c := range cases {
		got := firstNICMACFromSources(c.kind, c.lxcMAC, c.vmMAC)
		if got != c.want {
			t.Fatalf("firstNICMACFromSources(%q,%q,%q)=%q want %q", c.kind, c.lxcMAC, c.vmMAC, got, c.want)
		}
	}
}
