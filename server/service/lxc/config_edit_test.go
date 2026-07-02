package lxc

import (
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"kvm_console/model"
)

func TestCheckLXCQuota_AdminUnlimited(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "t.db")), &gorm.Config{})
	db.AutoMigrate(&model.User{}, &model.LXCCache{})
	model.DB = db
	db.Create(&model.User{Username: "admin", Role: "admin"})
	if err := CheckLXCQuota("admin", 9999, 999999); err != nil {
		t.Fatalf("admin should be unlimited: %v", err)
	}
}

func TestCheckLXCQuota_UserEnforced(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "t.db")), &gorm.Config{})
	db.AutoMigrate(&model.User{}, &model.LXCCache{})
	model.DB = db
	db.Create(&model.User{Username: "u1", Role: "user", MaxLXCCount: 2, MaxLXCCPU: 1024, MaxLXCRAMMB: 2048})
	db.Create(&model.LXCCache{Name: "c1", OwnerUsername: "u1", CPUShares: 256, MemoryMB: 512, Present: true})
	if err := CheckLXCQuota("u1", 256, 512); err != nil {
		t.Fatalf("within quota err: %v", err)
	}
	if err := CheckLXCQuota("u1", 9999, 512); err == nil {
		t.Fatal("over CPU quota should fail")
	}
}

func TestRenderConfigOverrides(t *testing.T) {
	autoTrue := true
	got := renderConfigOverrides(ContainerConfigUpdate{CPUShares: 512, MemoryMB: 1024, Autostart: &autoTrue, MAC: "02:11:22:33:44:55"})
	wantContains := []string{"lxc.cgroup2.cpu.weight = 512", "lxc.cgroup2.memory.max = 1024M", "lxc.start.auto = 1", "lxc.net.0.hwaddr = 02:11:22:33:44:55"}
	for _, w := range wantContains {
		if !contains(got, w) {
			t.Fatalf("missing %q in:\n%s", w, got)
		}
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || (len(s) > 0 && containsAt(s, sub))) }
func containsAt(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
