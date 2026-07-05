package lxc

import "testing"

func TestParseSnapshotList(t *testing.T) {
	in := "Name        Comment                      Creation time\n" +
		"snap0       -                            2026-07-02 10:00:00\n" +
		"snap1       hello world                  2026-07-02 11:00:00\n"
	got := parseSnapshotList(in)
	if len(got) != 2 {
		t.Fatalf("len = %d, got %+v", len(got), got)
	}
	if got[0].Name != "snap0" || got[0].Comment != "" || got[0].CreatedAt != "2026-07-02 10:00:00" {
		t.Fatalf("got[0] = %+v", got[0])
	}
	if got[1].Name != "snap1" || got[1].Comment != "hello world" || got[1].CreatedAt != "2026-07-02 11:00:00" {
		t.Fatalf("got[1] = %+v", got[1])
	}
}
