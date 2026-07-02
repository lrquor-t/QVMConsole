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

func TestMergeCacheRows_AllGone(t *testing.T) {
	db := setupDB(t)
	db.Create(&model.LXCCache{Name: "c1", Present: true})
	db.Create(&model.LXCCache{Name: "c2", Present: true})

	// lxc-ls 返回空：所有缓存容器应被标为离线
	merged, err := mergeCacheRows(db, []ContainerListItem{})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(merged) != 0 {
		t.Fatalf("expected 0 online, got %d", len(merged))
	}
	var rows []model.LXCCache
	db.Find(&rows)
	for _, r := range rows {
		if r.Present {
			t.Fatalf("container %s should be Present=false", r.Name)
		}
	}
}
