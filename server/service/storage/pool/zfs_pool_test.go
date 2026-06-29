package pool

import (
	"reflect"
	"testing"
)

func TestValidateZFSPoolName(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"normal", "tank", false},
		{"alphanumeric", "storage0", false},
		{"with dash", "data-pool", false},
		{"with underscore", "vm_disks", false},
		{"with dot", "pool.vmdisks", false},
		{"single letter", "t", false},
		{"empty", "", true},
		{"starts with digit", "0tank", true},
		{"starts with dash", "-tank", true},
		{"starts with underscore", "_tank", true},
		{"reserved mirror", "mirror", true},
		{"reserved raidz", "raidz", true},
		{"reserved raidz1", "raidz1", true},
		{"reserved raidz2", "raidz2", true},
		{"reserved raidz3", "raidz3", true},
		{"reserved spare", "spare", true},
		{"reserved log", "log", true},
		{"reserved cache", "cache", true},
		{"device-like c0", "c0t0d0", true},
		{"device-like c1", "c1d0", true},
		{"slash", "a/b", true},
		{"at", "a@b", true},
		{"percent", "a%b", true},
		{"space", "a b", true},
		{"too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateZFSPoolName(c.input)
			if (err != nil) != c.wantErr {
				t.Fatalf("validateZFSPoolName(%q) err=%v, wantErr=%v", c.input, err, c.wantErr)
			}
		})
	}
}

func TestZFSVdevMinDisks(t *testing.T) {
	cases := []struct {
		vdevType string
		want     int
	}{
		{"stripe", 1},
		{"mirror", 2},
		{"raidz1", 3},
		{"raidz2", 4},
		{"raidz3", 5},
		{"unknown", 0},
		{"", 0},
	}
	for _, c := range cases {
		got := zfsVdevMinDisks(c.vdevType)
		if got != c.want {
			t.Fatalf("zfsVdevMinDisks(%q)=%d, want %d", c.vdevType, got, c.want)
		}
	}
}

func TestIsValidZFSVdevType(t *testing.T) {
	valid := []string{"stripe", "mirror", "raidz1", "raidz2", "raidz3"}
	invalid := []string{"", "raidz", "RAIDZ1", "single", "parity", "striped"}
	for _, v := range valid {
		if !isValidZFSVdevType(v) {
			t.Fatalf("expected %q valid", v)
		}
	}
	for _, v := range invalid {
		if isValidZFSVdevType(v) {
			t.Fatalf("expected %q invalid", v)
		}
	}
}

func TestBuildZpoolCreateArgs(t *testing.T) {
	cases := []struct {
		name     string
		poolName string
		ashift   string
		vdevType string
		devices  []string
		want     []string
	}{
		{
			name:     "single disk stripe",
			poolName: "tank", ashift: "12", vdevType: "stripe",
			devices: []string{"/dev/sdb"},
			want:    []string{"create", "-f", "-o", "ashift=12", "tank", "/dev/sdb"},
		},
		{
			name:     "multi disk stripe (no keyword)",
			poolName: "tank", ashift: "12", vdevType: "stripe",
			devices: []string{"/dev/sdb", "/dev/sdc"},
			want:    []string{"create", "-f", "-o", "ashift=12", "tank", "/dev/sdb", "/dev/sdc"},
		},
		{
			name:     "mirror",
			poolName: "data", ashift: "12", vdevType: "mirror",
			devices: []string{"/dev/sdb", "/dev/sdc"},
			want:    []string{"create", "-f", "-o", "ashift=12", "data", "mirror", "/dev/sdb", "/dev/sdc"},
		},
		{
			name:     "raidz1",
			poolName: "tank", ashift: "9", vdevType: "raidz1",
			devices: []string{"/dev/sdb", "/dev/sdc", "/dev/sdd"},
			want:    []string{"create", "-f", "-o", "ashift=9", "tank", "raidz1", "/dev/sdb", "/dev/sdc", "/dev/sdd"},
		},
		{
			name:     "raidz2",
			poolName: "tank", ashift: "12", vdevType: "raidz2",
			devices: []string{"/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde"},
			want:    []string{"create", "-f", "-o", "ashift=12", "tank", "raidz2", "/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde"},
		},
		{
			name:     "raidz3",
			poolName: "tank", ashift: "12", vdevType: "raidz3",
			devices: []string{"/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde", "/dev/sdf"},
			want:    []string{"create", "-f", "-o", "ashift=12", "tank", "raidz3", "/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde", "/dev/sdf"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildZpoolCreateArgs(c.poolName, c.ashift, c.vdevType, c.devices)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("buildZpoolCreateArgs(%q,%q,%q,%v)\n got=%v\nwant=%v", c.poolName, c.ashift, c.vdevType, c.devices, got, c.want)
			}
		})
	}
}

func TestNormalizeZFSAshift(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "12"},
		{"12", "12"},
		{"9", "9"},
		{"13", "13"},
		{"0", "0"},
		{"bad", "12"},
		{"999", "12"},
	}
	for _, c := range cases {
		got := normalizeZFSAshift(c.in)
		if got != c.want {
			t.Fatalf("normalizeZFSAshift(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeZFSCompression(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "lz4"},
		{"lz4", "lz4"},
		{"zstd", "zstd"},
		{"off", "off"},
		{"gzip", "gzip"},
		{"bogus", "lz4"},
	}
	for _, c := range cases {
		got := normalizeZFSCompression(c.in)
		if got != c.want {
			t.Fatalf("normalizeZFSCompression(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
