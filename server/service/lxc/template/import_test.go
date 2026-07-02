package template

import "testing"

func TestBaseContainerName(t *testing.T) {
	got := baseContainerName("ubuntu22")
	want := "lxc__tmpl__ubuntu22"
	if got != want {
		t.Fatalf("baseContainerName = %q, want %q", got, want)
	}
}

func TestIsBaseContainer(t *testing.T) {
	if !isBaseContainer("lxc__tmpl__ubuntu22") {
		t.Fatal("should detect base container")
	}
	if isBaseContainer("c1") {
		t.Fatal("c1 is not a base container")
	}
}

func TestValidateImportParams(t *testing.T) {
	if err := validateImportParams(&ImportParams{Name: "ubuntu22", Arch: "amd64", SourcePath: "/tmp/x.tar.gz"}); err != nil {
		t.Fatalf("valid params err: %v", err)
	}
	if err := validateImportParams(&ImportParams{Name: "", SourcePath: "/tmp/x.tar.gz"}); err == nil {
		t.Fatal("empty name should fail")
	}
}
