package lxc

import (
	"path/filepath"
	"strings"
)

// moveStep 表示单个容器目录的迁移源/目标。
type moveStep struct {
	From string
	To   string
}

// CascadeImportDir 计算迁移后模板导入临时目录的级联值。
// 规则：若当前 import dir 等于 <oldLxcPath>/_imports（默认跟随），则切到 <newLxcPath>/_imports；否则保持现值。
func CascadeImportDir(oldLxcPath, newLxcPath, curImportDir string) string {
	if curImportDir == filepath.Join(oldLxcPath, "_imports") {
		return filepath.Join(newLxcPath, "_imports")
	}
	return curImportDir
}

// rewriteLxcConf 把 lxc.conf 文本中 lxc.lxcpath 行替换为新值（无则追加），保留其它行。
// 仅匹配紧邻空白或 '=' 的 lxc.lxcpath 键，避免误伤 lxc.lxcpath.xxx 之类。
func rewriteLxcConf(content, newLxcPath string) string {
	const key = "lxc.lxcpath"
	lines := strings.Split(content, "\n")
	found := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if !strings.HasPrefix(trim, key) {
			continue
		}
		rest := strings.TrimPrefix(trim, key)
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '=' {
			lines[i] = "lxc.lxcpath = " + newLxcPath
			found = true
		}
	}
	if !found {
		lines = append(lines, "lxc.lxcpath = "+newLxcPath)
	}
	return strings.Join(lines, "\n")
}

// rewriteContainerConfig 把容器 config 文本中 <oldLxcPath>/ 前缀替换为 <newLxcPath>/。
// 用「带尾斜线匹配」避免 /var/lib/lxc 与 /var/lib/lxc2 前缀碰撞叠加。
func rewriteContainerConfig(content, oldLxcPath, newLxcPath string) string {
	if oldLxcPath == "" {
		return content
	}
	oldDir := strings.TrimRight(oldLxcPath, "/") + "/"
	newDir := strings.TrimRight(newLxcPath, "/") + "/"
	return strings.ReplaceAll(content, oldDir, newDir)
}

// planRelocateMoves 列出每个容器目录的 {from,to} 迁移对（跳过空名）。
// 幂等判定（目标已存在则跳过实际搬移）在执行处 moveDir 处理。
func planRelocateMoves(oldLxcPath, newLxcPath string, names []string) []moveStep {
	steps := make([]moveStep, 0, len(names))
	for _, n := range names {
		if n == "" {
			continue
		}
		steps = append(steps, moveStep{
			From: filepath.Join(oldLxcPath, n),
			To:   filepath.Join(newLxcPath, n),
		})
	}
	return steps
}
