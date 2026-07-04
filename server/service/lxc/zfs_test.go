package lxc

import (
	"strings"
	"testing"
)

func TestZfsDatasetNames(t *testing.T) {
	const parent, base, name = "zp01/lxc", "lxc__tmpl__rocky8", "c1"
	if g := zfsBaseDataset(parent, base); g != "zp01/lxc/lxc__tmpl__rocky8" {
		t.Fatalf("base=%s", g)
	}
	if g := zfsBaseSnapshot(parent, base); g != "zp01/lxc/lxc__tmpl__rocky8@base" {
		t.Fatalf("baseSnapshot=%s", g)
	}
	if g := zfsContainerDataset(parent, name); g != "zp01/lxc/c1" {
		t.Fatalf("containerDataset=%s", g)
	}
	if g := zfsContainerMountpoint("/zp01/lxc", name); g != "/zp01/lxc/c1" {
		t.Fatalf("containerMountpoint=%s", g)
	}
}

// 克隆继承基底 config，需把 rootfs.path 从 <lxcpath>/<base>/rootfs 改成 <lxcpath>/<name>/rootfs。
func TestRewriteRootfsPathForClone(t *testing.T) {
	in := "lxc.apparmor.profile = generated\nlxc.rootfs.path = /zp01/lxc/lxc__tmpl__rocky8/rootfs\nlxc.arch = x86_64\n"
	out := rewriteRootfsPathForClone(in, "/zp01/lxc/lxc__tmpl__rocky8/rootfs", "/zp01/lxc/c1/rootfs")
	if strings.Contains(out, "/zp01/lxc/lxc__tmpl__rocky8/rootfs") {
		t.Fatalf("旧 rootfs 路径未替换: %s", out)
	}
	if !strings.Contains(out, "lxc.rootfs.path = /zp01/lxc/c1/rootfs") {
		t.Fatalf("缺少新 rootfs 路径: %s", out)
	}
	if !strings.Contains(out, "lxc.apparmor.profile = generated") || !strings.Contains(out, "lxc.arch = x86_64") {
		t.Fatalf("其它行被破坏: %s", out)
	}
}
