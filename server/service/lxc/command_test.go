package lxc

import "testing"

func TestParseLxcLsFancy(t *testing.T) {
	in := "NAME    STATE       IPV4       AUTOSTART\n" +
		"c1      RUNNING     10.0.0.5   YES\n" +
		"c2      STOPPED     -          NO\n"
	got, err := ParseLxcLsFancy(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Name != "c1" || got[0].Status != "RUNNING" || got[0].IPv4 != "10.0.0.5" || !got[0].Running {
		t.Fatalf("row0 mismatch: %+v", got[0])
	}
	if got[1].Name != "c2" || got[1].Running {
		t.Fatalf("row1 mismatch: %+v", got[1])
	}
}

func TestParseLxcLsFancy_Empty(t *testing.T) {
	got, err := ParseLxcLsFancy("")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestParseLxcInfo(t *testing.T) {
	in := "Name: c1\n" +
		"State: RUNNING\n" +
		"IP: 10.0.0.5\n" +
		"PID: 1234\n" +
		"Arch: x86_64\n"
	got, err := ParseLxcInfo(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Name != "c1" || got.Status != "RUNNING" || got.IP != "10.0.0.5" || got.PID != "1234" || got.Arch != "x86_64" {
		t.Fatalf("detail mismatch: %+v", got)
	}
}

func TestNICMACOrderZeroMatchesExisting(t *testing.T) {
	// order 0 必须等于现有创建流程用的 genMacByName(name)，保证旧容器 MAC 不变
	if NICMAC("demo", 0) != genMacByName("demo") {
		t.Fatal("order 0 的 MAC 应等于 genMacByName(name)")
	}
}

func TestNICMACPerOrderDistinctAndStable(t *testing.T) {
	m0 := NICMAC("demo", 0)
	m1 := NICMAC("demo", 1)
	m2 := NICMAC("demo", 2)
	if m0 == m1 || m1 == m2 || m0 == m2 {
		t.Fatalf("不同 order 的 MAC 不应重复: %s %s %s", m0, m1, m2)
	}
	if NICMAC("demo", 1) != m1 {
		t.Fatal("同 name+order 的 MAC 应稳定")
	}
	if len(m1) != 17 || m1[:3] != "02:" {
		t.Fatalf("MAC 格式/前缀异常: %s", m1)
	}
}
