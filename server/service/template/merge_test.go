package template

import (
	"reflect"
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
