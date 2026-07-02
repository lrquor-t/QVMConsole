package lxc

import (
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"kvm_console/model"
)

func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "t.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.LXCCache{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	model.DB = db
	return db
}

// mergeCacheRows 是 SyncContainerCache 内部把 lxc-ls 结果合并进 DB 的纯逻辑（便于单测）。
func TestMergeCacheRows_RemovesGone(t *testing.T) {
	db := setupDB(t)
	db.Create(&model.LXCCache{Name: "old", Present: true})

	// 当前 lxc-ls 仅返回 c1
	merged, err := mergeCacheRows(db, []ContainerListItem{{Name: "c1", Status: "RUNNING", IPv4: "10.0.0.1", Running: true}})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(merged) != 1 || merged[0].Name != "c1" {
		t.Fatalf("merged = %+v", merged)
	}
	var old model.LXCCache
	db.First(&old, "name = ?", "old")
	if old.Present {
		t.Fatalf("gone container should be Present=false")
	}
}
