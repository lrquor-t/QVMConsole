package config

import (
	"os"
	"testing"
)

func TestLXCConfigDefaults(t *testing.T) {
	for _, k := range []string{"KVM_LXC_LXC_PATH", "KVM_LXC_TEMPLATE_IMPORT_DIR", "KVM_LXC_DEFAULT_BACKING", "KVM_LXC_BASE_PREFIX"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	t.Cleanup(func() { Init() })
	Init()
	if GlobalConfig.LXCLxcPath != "/var/lib/lxc" {
		t.Fatalf("LXCLxcPath default = %q, want /var/lib/lxc", GlobalConfig.LXCLxcPath)
	}
	if GlobalConfig.LXCDefaultBacking != "overlay" {
		t.Fatalf("LXCDefaultBacking default = %q, want overlay", GlobalConfig.LXCDefaultBacking)
	}
	if GlobalConfig.LXCBasePrefix != "lxc__tmpl__" {
		t.Fatalf("LXCBasePrefix default = %q, want lxc__tmpl__", GlobalConfig.LXCBasePrefix)
	}
}
