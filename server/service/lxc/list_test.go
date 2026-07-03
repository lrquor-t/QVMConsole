package lxc

import (
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"kvm_console/config"
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

// filterVisibleItems 从 lxc-ls 结果里剔除应隐藏的内部容器（金基底模板容器 lxc__tmpl__*），
// 否则模板会混在容器列表里显示。
func TestFilterVisibleItems_HidesBaseContainers(t *testing.T) {
	items := []ContainerListItem{
		{Name: "c1"},
		{Name: "lxc__tmpl__rocky8-tpl"}, // 金基底模板，应隐藏
		{Name: "c2"},
		{Name: config.GlobalConfig.LXCBasePrefix + "ubuntu-tpl"}, // 按 config 前缀构造，应隐藏
	}
	got := filterVisibleItems(items)
	if len(got) != 2 || got[0].Name != "c1" || got[1].Name != "c2" {
		t.Fatalf("filterVisibleItems = %+v, want only [c1,c2]", got)
	}
}
