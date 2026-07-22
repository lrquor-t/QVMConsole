package model

import "time"

// LXCCache 保存用于列表类接口的 LXC 容器缓存投影（与 VMCache 同步模式一致）。
type LXCCache struct {
	ID             uint      `json:"id" gorm:"primaryKey"`
	Name           string    `json:"name" gorm:"uniqueIndex;size:128;not null"`
	OwnerUsername  string    `json:"owner_username" gorm:"index;size:64;not null;default:'admin'"`
	Status         string    `json:"status" gorm:"index;size:64"` // running/stopped/frozen/...
	Template       string    `json:"template" gorm:"size:255"`    // 来源模板名
	CPUShares      int       `json:"cpu_shares"`                  // cgroup cpu.weight（展示）
	MemoryMB       int       `json:"memory_mb"`                   // cgroup memory.max（展示）
	RootfsSizeText string    `json:"rootfs_size_text" gorm:"size:64"`
	Backing        string    `json:"backing" gorm:"size:32;default:'overlay'"` // overlay/dir/btrfs/zfs/lvm
	SnapshotCount  int       `json:"snapshot_count"`
	Autostart      bool      `json:"autostart"`
	Remark         string    `json:"remark" gorm:"size:255"`
	GroupName      string    `json:"group_name" gorm:"index;size:128"`
	MacAddress     string    `json:"mac_address" gorm:"size:64"`
	VethName       string    `json:"veth_name" gorm:"size:64"` // host 侧 veth（运行态解析回填）
	CachedIP       string    `json:"cached_ip" gorm:"size:64"`
	HealthStatus   string    `json:"health_status" gorm:"size:16;index"` // healthy/degraded/unhealthy/unknown
	LastHealthAt   time.Time `json:"last_health_at" gorm:"index"`
	BandwidthIn    int       `json:"bandwidth_in"`
	BandwidthOut   int       `json:"bandwidth_out"`
	Present        bool      `json:"present" gorm:"index;not null;default:true"`
	LastSyncedAt   time.Time `json:"last_synced_at" gorm:"index"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (LXCCache) TableName() string { return "lxc_containers" }

// UpdateLXCCacheHealthStatus 更新容器聚合健康状态。
func UpdateLXCCacheHealthStatus(name, status string, at time.Time) error {
	return DB.Model(&LXCCache{}).Where("name = ?", name).
		Updates(map[string]interface{}{"health_status": status, "last_health_at": at}).Error
}

// ListAllLXCCaches 列出所有 present 容器的缓存投影（健康检查后台调度使用）。
func ListAllLXCCaches() ([]LXCCache, error) {
	var list []LXCCache
	if err := DB.Where("present = ?", true).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// GetLXCCacheByName 取指定容器的缓存投影（含聚合 HealthStatus）。
// handler 读聚合健康用；不存在返 gorm.ErrRecordNotFound。
func GetLXCCacheByName(name string) (*LXCCache, error) {
	var c LXCCache
	if err := DB.Where("name = ?", name).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}
