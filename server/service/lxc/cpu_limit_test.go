package lxc

import (
	"math"
	"strings"
	"testing"
)

func TestCoresToCPUMax(t *testing.T) {
	cases := []struct {
		cores float64
		want  string
	}{
		{0, "max"}, {-1, "max"},
		{1, "100000 100000"}, {2.5, "250000 100000"}, {0.5, "50000 100000"},
	}
	for _, c := range cases {
		if got := coresToCPUMax(c.cores); got != c.want {
			t.Fatalf("coresToCPUMax(%v)=%q want %q", c.cores, got, c.want)
		}
	}
}

func TestParseCPUMax(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"max", 0, true}, {"", 0, true},
		{"100000 100000", 1, true}, {"250000 100000", 2.5, true}, {"150000 100000", 1.5, true},
		{"garbage", 0, false}, {"1 2 3", 0, false}, {"100 0", 0, false},
	}
	for _, c := range cases {
		got, ok := parseCPUMax(c.in)
		if ok != c.ok || (ok && math.Abs(got-c.want) > 1e-9) {
			t.Fatalf("parseCPUMax(%q)=(%v,%v) want (%v,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestValidateCPULimit(t *testing.T) {
	if err := validateCPULimit(-1, "", 8); err == nil {
		t.Fatal("negative cores should reject")
	}
	if err := validateCPULimit(1.2345, "", 8); err == nil {
		t.Fatal(">3 decimals should reject")
	}
	if err := validateCPULimit(10, "", 8); err == nil {
		t.Fatal("over nproc should reject")
	}
	if err := validateCPULimit(2, "0-3,a", 8); err == nil {
		t.Fatal("bad cpuset should reject")
	}
	if err := validateCPULimit(2.5, "0-3,^2", 8); err != nil {
		t.Fatalf("valid limit rejected: %v", err)
	}
	if err := validateCPULimit(0, "", 8); err != nil {
		t.Fatalf("zero cores rejected: %v", err)
	}
}

func TestRenderCPULimit(t *testing.T) {
	s := renderCPULimit(CPULimit{Cores: 0})
	if !strings.Contains(s, "lxc.cgroup2.cpu.max = max") {
		t.Fatalf("cores=0 should render max: %q", s)
	}
	if strings.Contains(s, "cpuset") {
		t.Fatalf("empty cpuset should not render cpuset line: %q", s)
	}
	s = renderCPULimit(CPULimit{Cores: 2.5, CPUSet: "0-1"})
	if !strings.Contains(s, "lxc.cgroup2.cpu.max = 250000 100000") {
		t.Fatalf("cpu.max line missing: %q", s)
	}
	if !strings.Contains(s, "lxc.cgroup2.cpuset.cpus = 0-1") {
		t.Fatalf("cpuset line missing: %q", s)
	}
}
