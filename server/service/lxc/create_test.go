package lxc

import (
	"strings"
	"testing"

	"kvm_console/model"
)

func TestParseCreateContainerParams(t *testing.T) {
	in := `{"name":"c1","template":"ubuntu22","cpu_shares":512,"memory_mb":1024,"autostart":true}`
	p, err := ParseCreateContainerParams(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p.Name != "c1" || p.Template != "ubuntu22" || p.CPUShares != 512 || p.MemoryMB != 1024 || !p.Autostart {
		t.Fatalf("parsed = %+v", p)
	}
}

func TestValidateContainerName(t *testing.T) {
	if err := validateContainerName("good-name1"); err != nil {
		t.Fatalf("good name err: %v", err)
	}
	if err := validateContainerName("lxc__tmpl__x"); err == nil {
		t.Fatal("reserved base prefix should be rejected")
	}
	if err := validateContainerName("bad name!"); err == nil {
		t.Fatal("invalid chars should be rejected")
	}
}

func TestGenMacByName_UniquePerName(t *testing.T) {
	a := genMacByName("c1")
	b := genMacByName("c2")
	if a == b {
		t.Fatalf("different names produced same MAC: %s", a)
	}
	if genMacByName("c1") != a {
		t.Fatalf("MAC not deterministic for same name")
	}
	if !strings.HasPrefix(a, "02:") {
		t.Fatalf("MAC not locally-administered: %s", a)
	}
}

func TestResolveNIC0LinkPure(t *testing.T) {
	cases := []struct {
		name     string
		switchID uint
		sw       model.VPCSwitch
		found    bool
		fallback string
		want     string
	}{
		{"clone 未选交换机 → 继承(空)", 0, model.VPCSwitch{}, false, "", ""},
		{"download 未选交换机 → br-ovs", 0, model.VPCSwitch{}, false, "br-ovs", "br-ovs"},
		{"选交换机 + 有桥名", 7, model.VPCSwitch{BridgeName: "br-ovs"}, true, "", "br-ovs"},
		{"选交换机 + 直连桥", 7, model.VPCSwitch{BridgeName: "br-direct"}, true, "", "br-direct"},
		{"选交换机 + 桥名为空 → 回退 br-ovs", 7, model.VPCSwitch{BridgeName: ""}, true, "", "br-ovs"},
		{"选交换机但查不到 → 回退 br-ovs", 7, model.VPCSwitch{}, false, "", "br-ovs"},
	}
	for _, c := range cases {
		got := resolveNIC0LinkPure(c.switchID, c.sw, c.found, c.fallback)
		if got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
