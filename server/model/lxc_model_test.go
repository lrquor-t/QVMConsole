package model

import (
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&LXCCache{}, &LXCTemplate{}, &VPCVMBinding{}, &User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	DB = db
	return db
}

func TestLXCCacheRoundTrip(t *testing.T) {
	db := newTestDB(t)
	c := LXCCache{Name: "c1", OwnerUsername: "admin", Status: "running", Backing: "overlay", Present: true}
	if err := db.Create(&c).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var got LXCCache
	if err := db.First(&got, c.ID).Error; err != nil {
		t.Fatalf("first: %v", err)
	}
	if got.Name != "c1" || got.Backing != "overlay" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestLXCTemplateRoundTrip(t *testing.T) {
	db := newTestDB(t)
	tpl := LXCTemplate{Name: "ubuntu22", Distro: "ubuntu", Release: "22.04", Arch: "amd64", BaseContainerName: "lxc__tmpl__ubuntu22", Backing: "overlay", CloneVisible: true}
	if err := db.Create(&tpl).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var got LXCTemplate
	if err := db.First(&got, tpl.ID).Error; err != nil {
		t.Fatalf("first: %v", err)
	}
	if got.BaseContainerName != "lxc__tmpl__ubuntu22" {
		t.Fatalf("base name mismatch: %s", got.BaseContainerName)
	}
}

func TestVPCVMBindingKindDefault(t *testing.T) {
	db := newTestDB(t)
	b := VPCVMBinding{VMName: "vm1", Username: "admin", SwitchID: 1, SecurityGroupID: 1, InterfaceOrder: 0}
	if err := db.Create(&b).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if b.Kind != "vm" {
		t.Fatalf("default Kind = %q, want vm", b.Kind)
	}
}

func TestUserLXCQuotaDefaults(t *testing.T) {
	db := newTestDB(t)
	u := User{Username: "u1", Role: "user"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.MaxLXCCount != 0 || u.MaxLXCCPU != 0 || u.MaxLXCRAMMB != 0 {
		t.Fatalf("lxc quota defaults nonzero: %+v", u)
	}
}
