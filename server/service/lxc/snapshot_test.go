package lxc

import "testing"

func TestParseSnapshotList(t *testing.T) {
	in := "Name        Comment                      Creation time\n" +
		"snap0       -                            2026-07-02 10:00:00\n"
	got := parseSnapshotList(in)
	if len(got) != 1 || got[0] != "snap0" {
		t.Fatalf("got = %+v", got)
	}
}
