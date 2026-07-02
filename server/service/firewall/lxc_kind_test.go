package firewall

import "testing"

// TestKindDispatchDefaultVM 保证历史 binding（无 DB / kind=空 / kind=vm）走原 VM 逻辑，
// 唯有显式 kind=lxc 才分派到容器路径。
func TestKindDispatchDefaultVM(t *testing.T) {
	if kind := dispatchKindForName("somevm"); kind != "vm" {
		t.Fatalf("default kind = %q, want vm", kind)
	}
}
