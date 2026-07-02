package lxc

import "testing"

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
