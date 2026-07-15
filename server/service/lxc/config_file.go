package lxc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"kvm_console/config"
	"kvm_console/model"
)

// ConfigFileContent 是容器原始 config 文件的内容快照（含元信息）。
type ConfigFileContent struct {
	Content string `json:"content"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime"` // unix 秒
}

// ConfigBackup 是一份历史备份（config.bak-<ts>）的元信息。
type ConfigBackup struct {
	Name    string `json:"name"` // basename: config.bak-<ts>
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime"`
}

const (
	configFileMaxBytes = 1 << 20 // 1MB，config 文件本就很小，防滥用
	configBackupKeep   = 10      // 保留最近 10 份备份
)

var backupNameRE = regexp.MustCompile(`^config\.bak-(\d+)$`)

// backupFileName 生成备份文件名 config.bak-<unix秒>。
func backupFileName(ts int64) string {
	return fmt.Sprintf("config.bak-%d", ts)
}

// isBackupName 判断文件名是否为本特性产生的 config 备份（正则防穿越变体）。
func isBackupName(name string) bool {
	return backupNameRE.MatchString(name)
}

// selectBackupsToPrune 给定备份名与其 mtime、保留数 keep，返回应删除的（最旧的、超出 keep 的）。
// 纯函数，便于单测。输入未排序时内部按 mtime 升序排（旧→新）。
func selectBackupsToPrune(names []string, mtimeByName map[string]int64, keep int) []string {
	type bk struct {
		name  string
		mtime int64
	}
	all := make([]bk, 0, len(names))
	for _, n := range names {
		mt, ok := mtimeByName[n]
		if !ok {
			continue
		}
		all = append(all, bk{n, mt})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mtime < all[j].mtime }) // 升序：旧→新
	if len(all) <= keep {
		return nil
	}
	drop := all[:len(all)-keep]
	out := make([]string, 0, len(drop))
	for _, b := range drop {
		out = append(out, b.name)
	}
	return out
}

// containerConfigDir 返回容器目录路径（containerConfigPath 已在 mount.go 定义，此处复用）。
func containerConfigDir(name string) string {
	return filepath.Join(config.GlobalConfig.LXCLxcPath, name)
}

// requireContainerRow 查容器 DB 行，不存在返「容器不存在」（与 UpdateContainerConfig 一致）。
func requireContainerRow(name string) (model.LXCCache, error) {
	var row model.LXCCache
	if err := model.DB.Where("name = ?", name).First(&row).Error; err != nil {
		return row, errors.New("容器不存在")
	}
	return row, nil
}

// ReadContainerConfigFile 读取容器原始 config 文件内容 + 元信息（任意状态可读）。
func ReadContainerConfigFile(name string) (ConfigFileContent, error) {
	if _, err := requireContainerRow(name); err != nil {
		return ConfigFileContent{}, err
	}
	p := containerConfigPath(name)
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return ConfigFileContent{}, errors.New("配置文件不存在")
		}
		return ConfigFileContent{}, err
	}
	info, err := os.Stat(p)
	if err != nil {
		return ConfigFileContent{}, err
	}
	return ConfigFileContent{
		Content: string(data),
		Path:    p,
		Size:    info.Size(),
		ModTime: info.ModTime().Unix(),
	}, nil
}

// WriteContainerConfigFile 备份 + 轮转 + 原子写入容器 config。仅 STOPPED 允许写。
// restore 复用此函数，故 STOPPED/大小/备份/原子 校验集中在此。
func WriteContainerConfigFile(name, content string) error {
	row, err := requireContainerRow(name)
	if err != nil {
		return err
	}
	if row.Status != "STOPPED" {
		return errors.New("请先停止容器再修改配置文件")
	}
	if len(content) > configFileMaxBytes {
		return errors.New("配置文件过大（超过 1MB）")
	}
	dir := containerConfigDir(name)
	p := containerConfigPath(name)
	now := time.Now().Unix()

	// 1) 备份当前 config（不存在则跳过备份但仍继续写）
	if old, rerr := os.ReadFile(p); rerr == nil {
		bakPath := filepath.Join(dir, backupFileName(now))
		if werr := os.WriteFile(bakPath, old, 0644); werr != nil {
			return fmt.Errorf("备份配置文件失败: %w", werr)
		}
	} else if !os.IsNotExist(rerr) {
		return fmt.Errorf("读取当前配置文件失败: %w", rerr)
	}

	// 2) 轮转：超出 keep 的最旧备份删除（失败不阻断主写入）
	_ = pruneBackups(dir)

	// 3) 原子写：tmp -> rename（同文件系统原子）
	tmpPath := filepath.Join(dir, fmt.Sprintf("config.tmp-%d", now))
	if werr := os.WriteFile(tmpPath, []byte(content), 0644); werr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("写入配置文件失败: %w", werr)
	}
	if rerr := os.Rename(tmpPath, p); rerr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("替换配置文件失败: %w", rerr)
	}
	return nil
}

// pruneBackups 扫描 dir 下 config.bak-*，按 mtime 降序保留前 keep，删多余。返回删除中的错误。
func pruneBackups(dir string) []error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []error{err}
	}
	names := make([]string, 0)
	mtimeByName := map[string]int64{}
	for _, e := range entries {
		if e.IsDir() || !isBackupName(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		names = append(names, e.Name())
		mtimeByName[e.Name()] = fi.ModTime().Unix()
	}
	drop := selectBackupsToPrune(names, mtimeByName, configBackupKeep)
	var errs []error
	for _, n := range drop {
		if err := os.Remove(filepath.Join(dir, n)); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errs
}

// ListContainerConfigBackups 列出容器 config 的历史备份（新→旧）。
func ListContainerConfigBackups(name string) ([]ConfigBackup, error) {
	if _, err := requireContainerRow(name); err != nil {
		return nil, err
	}
	dir := containerConfigDir(name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]ConfigBackup, 0)
	for _, e := range entries {
		if e.IsDir() || !isBackupName(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, ConfigBackup{
			Name:    e.Name(),
			Size:    fi.Size(),
			ModTime: fi.ModTime().Unix(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime > out[j].ModTime }) // 新→旧
	return out, nil
}

// RestoreContainerConfigFile 用指定备份覆盖当前 config。复用 WriteContainerConfigFile，
// 故恢复前自动备份当前 config（可撤销），且同样要求 STOPPED。
func RestoreContainerConfigFile(name, bakName string) error {
	if _, err := requireContainerRow(name); err != nil {
		return err
	}
	if !isBackupName(bakName) {
		return errors.New("无效的备份文件名")
	}
	data, err := os.ReadFile(filepath.Join(containerConfigDir(name), bakName))
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("备份不存在")
		}
		return err
	}
	return WriteContainerConfigFile(name, string(data))
}

// DeleteContainerConfigFileBackup 删除一份备份（不改 config，不要求 STOPPED）。
func DeleteContainerConfigFileBackup(name, bakName string) error {
	if _, err := requireContainerRow(name); err != nil {
		return err
	}
	if !isBackupName(bakName) {
		return errors.New("无效的备份文件名")
	}
	if err := os.Remove(filepath.Join(containerConfigDir(name), bakName)); err != nil {
		if os.IsNotExist(err) {
			return errors.New("备份不存在")
		}
		return err
	}
	return nil
}
