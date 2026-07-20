package model

import "time"

// LXCHealthCheck LXC 容器健康检查规则（per-container 多条）。
// 字段命名对齐 LXCSchedule：Name=容器名。
type LXCHealthCheck struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	Name          string     `gorm:"size:128;not null;index:idx_lxc_health_container" json:"name"` // 容器名
	CheckName     string     `gorm:"size:128;not null" json:"check_name"`                            // 检查项名称，如「Nginx 网关」
	Type          string     `gorm:"size:16;not null" json:"type"`                                   // http/tcp/script
	Target        string     `gorm:"size:512;not null" json:"target"`                                // URL / IP:Port / 命令
	ExpectedCode  int        `gorm:"not null;default:200" json:"expected_code"`                      // http 期望状态码
	Critical      bool       `gorm:"not null;default:true" json:"critical"`                          // 是否核心项
	Enabled       bool       `gorm:"not null;default:true;index:idx_lxc_health_enabled" json:"enabled"`
	CreatedBy     string     `gorm:"size:100;not null" json:"created_by"`
	LastStatus    string     `gorm:"size:16" json:"last_status"` // healthy/unhealthy/unknown
	LastLatencyMs int        `json:"last_latency_ms"`
	LastError     string     `gorm:"type:text" json:"last_error"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// TableName 指定表名。
func (LXCHealthCheck) TableName() string {
	return "lxc_health_checks"
}

// CreateLXCHealthCheck 创建健康检查规则。
func CreateLXCHealthCheck(h *LXCHealthCheck) error {
	return DB.Create(h).Error
}

// UpdateLXCHealthCheck 保存健康检查规则。
func UpdateLXCHealthCheck(h *LXCHealthCheck) error {
	return DB.Save(h).Error
}

// GetLXCHealthCheckByIDAndContainer 获取指定容器的健康检查规则。
func GetLXCHealthCheckByIDAndContainer(id uint, name string) (*LXCHealthCheck, error) {
	var h LXCHealthCheck
	if err := DB.Where("id = ? AND name = ?", id, name).First(&h).Error; err != nil {
		return nil, err
	}
	return &h, nil
}

// ListLXCHealthChecksByContainer 列出容器全部规则。
func ListLXCHealthChecksByContainer(name string) ([]LXCHealthCheck, error) {
	var list []LXCHealthCheck
	if err := DB.Where("name = ?", name).Order("enabled DESC").Order("critical DESC").Order("id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ListEnabledLXCHealthChecks 取全部启用规则（调度器用）。
func ListEnabledLXCHealthChecks() ([]LXCHealthCheck, error) {
	var list []LXCHealthCheck
	if err := DB.Where("enabled = ?", true).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// DeleteLXCHealthCheck 删除单个健康检查规则。
func DeleteLXCHealthCheck(id uint) error {
	return DB.Delete(&LXCHealthCheck{}, id).Error
}

// DeleteLXCHealthChecksByContainer 删除容器全部规则（容器删除时调用）。
func DeleteLXCHealthChecksByContainer(name string) error {
	return DB.Where("name = ?", name).Delete(&LXCHealthCheck{}).Error
}

// UpdateLXCHealthCheckResult 写最近一次检查结果。
func UpdateLXCHealthCheckResult(id uint, status string, latencyMs int, errMsg string, at time.Time) error {
	return DB.Model(&LXCHealthCheck{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"last_status":     status,
			"last_latency_ms": latencyMs,
			"last_error":      errMsg,
			"last_checked_at": at,
		}).Error
}
