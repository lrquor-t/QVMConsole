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
