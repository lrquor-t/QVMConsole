package template

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeMergeMode(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"flatten", "flatten", TemplateMergeModeFlatten, false},
		{"flatten trimmed/space", "  flatten ", TemplateMergeModeFlatten, false},
		{"flatten upper", "FLATTEN", TemplateMergeModeFlatten, false},
		{"commit_to_parent", "commit_to_parent", TemplateMergeModeCommitToParent, false},
		{"commit trimmed", "\tcommit_to_parent\n", TemplateMergeModeCommitToParent, false},
		{"empty", "", "", true},
		{"unknown", "promote", "", true},
		{"bogus", "merge", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := normalizeMergeMode(c.input)
			if (err != nil) != c.wantErr {
				t.Fatalf("normalizeMergeMode(%q) err=%v, wantErr=%v", c.input, err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Fatalf("normalizeMergeMode(%q)=%q, want %q", c.input, got, c.want)
			}
		})
	}
}

func TestParseMergeTemplateParams(t *testing.T) {
	cases := []struct {
		name            string
		json            string
		wantTpl         string
		wantMode        string
		wantExpectedVMs []string // 非 nil 时校验
		wantErr         bool
	}{
		{
			name: "flatten ok",
			json: `{"template_name":"ubu-nginx","mode":"flatten","expected_vms":["vm1"]}`,
			wantTpl: "ubu-nginx", wantMode: TemplateMergeModeFlatten, wantExpectedVMs: []string{"vm1"},
		},
		{
			name: "commit ok + trims name",
			json: `{"template_name":"  ubu-nginx  ","mode":"commit_to_parent"}`,
			wantTpl: "ubu-nginx", wantMode: TemplateMergeModeCommitToParent,
		},
		{name: "empty name", json: `{"template_name":"","mode":"flatten"}`, wantErr: true},
		{name: "missing name", json: `{"mode":"flatten"}`, wantErr: true},
		{name: "bad mode", json: `{"template_name":"x","mode":"nope"}`, wantErr: true},
		{name: "bad json", json: `{not json`, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseMergeTemplateParams(c.json)
			if (err != nil) != c.wantErr {
				t.Fatalf("ParseMergeTemplateParams(%q) err=%v, wantErr=%v", c.json, err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if got.TemplateName != c.wantTpl || got.Mode != c.wantMode {
				t.Fatalf("got name=%q mode=%q, want name=%q mode=%q", got.TemplateName, got.Mode, c.wantTpl, c.wantMode)
			}
			if c.wantExpectedVMs != nil && !reflect.DeepEqual(got.ExpectedVMs, c.wantExpectedVMs) {
				t.Fatalf("got expected_vms=%v, want %v", got.ExpectedVMs, c.wantExpectedVMs)
			}
		})
	}
}

func TestBuildFlattenBlockers(t *testing.T) {
	shutoffVM := func(name string) TemplateRelatedVM { return TemplateRelatedVM{Name: name, Status: "shut off", CloneMode: "linked"} }
	runningVM := func(name string) TemplateRelatedVM { return TemplateRelatedVM{Name: name, Status: "running", CloneMode: "linked"} }
	cases := []struct {
		name       string
		hasBacking bool
		vms        []TemplateRelatedVM
		wantCnt    int
		wantRoot   bool // 第一条是否"已是独立镜像"
	}{
		{"no backing (root)", false, nil, 1, true},
		{"backing + all shutoff", true, []TemplateRelatedVM{shutoffVM("v1"), shutoffVM("v2")}, 0, false},
		{"backing + no vms", true, nil, 0, false},
		{"backing + one running", true, []TemplateRelatedVM{shutoffVM("v1"), runningVM("v2")}, 1, false},
		{"backing + empty status counts as not shutoff", true, []TemplateRelatedVM{{Name: "v3", Status: "", CloneMode: "linked"}}, 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildFlattenBlockers(c.hasBacking, c.vms)
			if len(got) != c.wantCnt {
				t.Fatalf("buildFlattenBlockers got %v, want count %d", got, c.wantCnt)
			}
			if c.wantRoot && (len(got) == 0 || !strings.Contains(got[0], "独立镜像")) {
				t.Fatalf("expected root blocker, got %v", got)
			}
		})
	}
}

func TestBuildCommitBlockers(t *testing.T) {
	shutoff := func(n string) TemplateRelatedVM { return TemplateRelatedVM{Name: n, Status: "shut off", CloneMode: "linked"} }
	running := func(n string) TemplateRelatedVM { return TemplateRelatedVM{Name: n, Status: "running", CloneMode: "linked"} }
	otherChild := func(n string) TemplateInfo { return TemplateInfo{Name: n, NodeID: "node-" + n} }
	cases := []struct {
		name           string
		hasParent      bool
		parentDirectVMs []TemplateRelatedVM
		otherChildren   []TemplateInfo
		subtreeVMs      []TemplateRelatedVM
		wantCnt         int
	}{
		{"all clear", true, nil, nil, []TemplateRelatedVM{shutoff("v1")}, 0},
		{"no parent (root)", false, nil, nil, nil, 1},
		{"parent has direct vm", true, []TemplateRelatedVM{running("pv")}, nil, nil, 1},
		{"parent has other child", true, nil, []TemplateInfo{otherChild("sibling")}, nil, 1},
		{"subtree vm running", true, nil, nil, []TemplateRelatedVM{running("v1")}, 1},
		{"multiple blockers", false, []TemplateRelatedVM{running("pv")}, []TemplateInfo{otherChild("sib")}, nil, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildCommitBlockers(c.hasParent, c.parentDirectVMs, c.otherChildren, c.subtreeVMs)
			if len(got) != c.wantCnt {
				t.Fatalf("buildCommitBlockers(%v) got %v, want count %d", c.name, got, c.wantCnt)
			}
		})
	}
}

func TestBuildFlattenConvertCmd(t *testing.T) {
	got := buildFlattenConvertCmd("/var/lib/libvirt/templates/b.qcow2", "/var/lib/libvirt/templates/b.merge-20260630120000.qcow2")
	want := "qemu-img convert -f qcow2 -O qcow2 '/var/lib/libvirt/templates/b.qcow2' '/var/lib/libvirt/templates/b.merge-20260630120000.qcow2'"
	if got != want {
		t.Fatalf("buildFlattenConvertCmd=\n got=%s\nwant=%s", got, want)
	}
}
