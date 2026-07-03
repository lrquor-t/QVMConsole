package config

import "testing"

func TestLXCSettingsRoundTrip(t *testing.T) {
	// 清掉环境变量，确保 LoadFromDB 的「环境变量优先」不跳过这些键
	t.Setenv("KVM_LXC_LXC_PATH", "")
	t.Setenv("KVM_LXC_TEMPLATE_IMPORT_DIR", "")
	t.Setenv("KVM_LXC_DEFAULT_BACKING", "")

	c := &Config{
		LXCLxcPath:           "/data/lxc",
		LXCTemplateImportDir: "/data/lxc/_imports",
		LXCDefaultBacking:    "dir",
	}
	m := c.ToSettingsMap()
	if m["lxc_lxc_path"] != "/data/lxc" {
		t.Fatalf("ToSettingsMap 缺 lxc_lxc_path: %q", m["lxc_lxc_path"])
	}
	if m["lxc_template_import_dir"] != "/data/lxc/_imports" {
		t.Fatalf("ToSettingsMap 缺 lxc_template_import_dir: %q", m["lxc_template_import_dir"])
	}
	if m["lxc_default_backing"] != "dir" {
		t.Fatalf("ToSettingsMap 缺 lxc_default_backing: %q", m["lxc_default_backing"])
	}
	if _, ok := m["lxc_base_prefix"]; ok {
		t.Fatal("lxc_base_prefix 必须只读，不应出现在 ToSettingsMap")
	}

	// 往返：从 map 重建
	c2 := &Config{}
	c2.LoadFromDB(map[string]string{
		"lxc_lxc_path":            "/srv/lxc",
		"lxc_template_import_dir": "/srv/lxc/_imports",
		"lxc_default_backing":     "overlay",
	})
	if c2.LXCLxcPath != "/srv/lxc" || c2.LXCTemplateImportDir != "/srv/lxc/_imports" || c2.LXCDefaultBacking != "overlay" {
		t.Fatalf("LoadFromDB 未正确回填: %+v", c2)
	}
}
