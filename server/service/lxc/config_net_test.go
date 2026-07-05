package lxc

import (
	"strings"
	"testing"
)

func TestSplitNICBlocksParsesAndPreservesOther(t *testing.T) {
	in := "lxc.uts.name = a\n" +
		"lxc.net.0.type = veth\n" +
		"lxc.net.0.link = br-ovs\n" +
		"lxc.net.0.hwaddr = 02:11:22:33:44:55\n" +
		"lxc.cgroup2.cpu.weight = 100\n" +
		"lxc.net.1.type = veth\n" +
		"lxc.net.1.link = br-direct\n"
	other, blocks := SplitNICBlocks(in)
	if !strings.Contains(other, "lxc.uts.name = a") || !strings.Contains(other, "lxc.cgroup2.cpu.weight = 100") {
		t.Fatalf("other 应保留非 lxc.net.* 行, got: %q", other)
	}
	if blocks[0]["type"] != "veth" || blocks[0]["link"] != "br-ovs" || blocks[0]["hwaddr"] != "02:11:22:33:44:55" {
		t.Fatalf("order0 解析错误: %+v", blocks[0])
	}
	if blocks[1]["link"] != "br-direct" {
		t.Fatalf("order1 解析错误: %+v", blocks[1])
	}
}

func TestRenderNICBlocksSortedAndKeyed(t *testing.T) {
	blocks := map[int]map[string]string{
		1: {"type": "veth", "link": "br-x"},
		0: {"hwaddr": "02:aa:bb:cc:dd:ee", "type": "veth", "link": "br-ovs"},
	}
	out := RenderNICBlocks(blocks)
	// order 升序，每块内 key 升序
	mustContain := []string{
		"lxc.net.0.hwaddr = 02:aa:bb:cc:dd:ee", "lxc.net.0.link = br-ovs", "lxc.net.0.type = veth",
		"lxc.net.1.link = br-x", "lxc.net.1.type = veth",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Fatalf("render 缺少 %q, got: %q", s, out)
		}
	}
	idx0 := strings.Index(out, "lxc.net.0.")
	idx1 := strings.Index(out, "lxc.net.1.")
	if idx0 == -1 || idx1 == -1 || idx0 > idx1 {
		t.Fatalf("order 应升序排列: %q", out)
	}
}

func TestCompactNICBlocksRenumbersContiguously(t *testing.T) {
	// 删掉 order 0 后，原 order 1/2 应重排为 0/1
	blocks := map[int]map[string]string{
		1: {"link": "a"},
		2: {"link": "b"},
	}
	got := CompactNICBlocks(blocks)
	if got[0]["link"] != "a" || got[1]["link"] != "b" {
		t.Fatalf("compact 重排错误: %+v", got)
	}
	if _, exists := got[2]; exists {
		t.Fatalf("不应残留 order 2: %+v", got)
	}
}
