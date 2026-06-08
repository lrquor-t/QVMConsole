package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"kvm_console/config"
	"kvm_console/utils"
)

const (
	templateTransferRetention = 24 * time.Hour
	templateCopyTimeout       = 2 * time.Hour
)

var templateNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type ExportTemplateParams struct {
	TemplateName string `json:"template_name"`
	Scope        string `json:"scope,omitempty"` // node/root
}

type TemplateDownloadLink struct {
	Label        string `json:"label"`
	FileName     string `json:"file_name"`
	DownloadPath string `json:"download_path"`
}

type ExportTemplateResult struct {
	TemplateName   string                 `json:"template_name"`
	FileName       string                 `json:"file_name"`
	FileSize       string                 `json:"file_size"`
	DownloadPath   string                 `json:"download_path"`
	MetaFileName   string                 `json:"meta_file_name,omitempty"`
	ExtraDownloads []TemplateDownloadLink `json:"extra_downloads,omitempty"`
}

type ImportTemplateParams struct {
	TemplateName  string `json:"template_name,omitempty"` // 旧 qcow2 导入兼容
	UploadPath    string `json:"upload_path,omitempty"`
	UploadName    string `json:"upload_name,omitempty"`
	SourcePath    string `json:"source_path,omitempty"`
	SourceName    string `json:"source_name,omitempty"`
	CleanupSource bool   `json:"cleanup_source,omitempty"`
	Type          string `json:"type,omitempty"`
	RootPassword  string `json:"root_password,omitempty"`
	TemplateUser  string `json:"template_user,omitempty"`
}

type ImportTemplateResult struct {
	TemplateName string   `json:"template_name,omitempty"`
	Path         string   `json:"path,omitempty"`
	Type         string   `json:"type,omitempty"`
	HasMeta      bool     `json:"has_meta"`
	Mode         string   `json:"mode,omitempty"`
	Imported     []string `json:"imported,omitempty"`
	Skipped      []string `json:"skipped,omitempty"`
}

type TemplatePackageManifest struct {
	Version     int                   `json:"version"`
	ExportedAt  string                `json:"exported_at"`
	Scope       string                `json:"scope"`
	RootNodeID  string                `json:"root_node_id"`
	TemplateUID string                `json:"template_uid"`
	Nodes       []TemplatePackageNode `json:"nodes"`
}

type TemplatePackageNode struct {
	Name     string       `json:"name"`
	DiskFile string       `json:"disk_file"`
	MetaFile string       `json:"meta_file"`
	Meta     TemplateMeta `json:"meta"`
	FileSize int64        `json:"file_size"`
	MD5      string       `json:"md5"`
	SHA256   string       `json:"sha256"`
}

type ImportTemplatePreviewNode struct {
	Name           string       `json:"name"`
	AdminName      string       `json:"admin_name"`
	DisplayName    string       `json:"display_name"`
	Category       string       `json:"category,omitempty"`
	TemplateUID    string       `json:"template_uid"`
	NodeID         string       `json:"node_id"`
	ParentNodeID   string       `json:"parent_node_id"`
	RootNodeID     string       `json:"root_node_id"`
	Type           string       `json:"type"`
	CloneVisible   bool         `json:"clone_visible"`
	Disabled       bool         `json:"disabled"`
	FileSize       int64        `json:"file_size"`
	MD5            string       `json:"md5"`
	SHA256         string       `json:"sha256"`
	Exists         bool         `json:"exists"`
	WillImport     bool         `json:"will_import"`
	ConflictReason string       `json:"conflict_reason,omitempty"`
	Meta           TemplateMeta `json:"meta"`
}

type ImportTemplatePreviewResult struct {
	Token       string                      `json:"token"`
	Mode        string                      `json:"mode"` // create/update
	TemplateUID string                      `json:"template_uid"`
	RootNodeID  string                      `json:"root_node_id"`
	Nodes       []ImportTemplatePreviewNode `json:"nodes"`
	CanImport   bool                        `json:"can_import"`
	Message     string                      `json:"message"`
}

type templateImportPreviewSession struct {
	SourcePath    string
	SourceName    string
	CleanupSource bool
	CreatedAt     time.Time
}

var templateImportPreviewStore = struct {
	sync.Mutex
	items map[string]templateImportPreviewSession
}{items: make(map[string]templateImportPreviewSession)}

func ValidateTemplateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("模板名称不能为空")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("模板名称不能包含连续的点")
	}
	if !templateNamePattern.MatchString(name) {
		return fmt.Errorf("模板名称只能包含字母、数字、点、下划线和短横线")
	}
	return nil
}

func isValidTemplateType(templateType string) bool {
	switch strings.ToLower(strings.TrimSpace(templateType)) {
	case "linux", "windows", "fnos", "other":
		return true
	default:
		return false
	}
}

func normalizeTemplateType(templateType string) string {
	templateType = strings.ToLower(strings.TrimSpace(templateType))
	if isValidTemplateType(templateType) {
		return templateType
	}
	return ""
}

func detectTemplateTypeFromName(name string) string {
	nameLower := strings.ToLower(strings.TrimSpace(name))
	if strings.Contains(nameLower, "win") || strings.Contains(nameLower, "windows") {
		return "windows"
	}
	if strings.Contains(nameLower, "fnos") || strings.Contains(nameLower, "nas") {
		return "fnos"
	}
	return "linux"
}

func isSupportedTemplateImportName(name string) bool {
	nameLower := strings.ToLower(strings.TrimSpace(name))
	return strings.HasSuffix(nameLower, ".tar.gz") || strings.HasSuffix(nameLower, ".tgz") ||
		strings.HasSuffix(nameLower, ".qcow2")
}

func ValidateTemplateImportName(name string) error {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return fmt.Errorf("导入源文件名不能为空")
	}
	if !isSupportedTemplateImportName(trimmedName) {
		return fmt.Errorf("当前仅支持导入 .tar.gz / .tgz 模板包，或兼容导入 .qcow2")
	}
	return nil
}

func resolveImportTemplateSource(params *ImportTemplateParams) (string, string, bool, error) {
	if params == nil {
		return "", "", false, fmt.Errorf("导入参数不能为空")
	}
	sourcePath := strings.TrimSpace(params.SourcePath)
	sourceName := strings.TrimSpace(params.SourceName)
	cleanupSource := params.CleanupSource
	usesLegacyUpload := false
	if sourcePath == "" {
		sourcePath = strings.TrimSpace(params.UploadPath)
		usesLegacyUpload = sourcePath != ""
	}
	if sourceName == "" {
		sourceName = strings.TrimSpace(params.UploadName)
	}
	if sourcePath == "" {
		return "", "", false, fmt.Errorf("导入源路径不能为空")
	}
	if sourceName == "" {
		sourceName = filepath.Base(sourcePath)
	}
	if err := ValidateTemplateImportName(sourceName); err != nil {
		return "", "", false, err
	}
	if strings.TrimSpace(params.SourcePath) != "" && !cleanupSource && !filepath.IsAbs(sourcePath) {
		return "", "", false, fmt.Errorf("主机导入路径必须为绝对路径")
	}
	if usesLegacyUpload {
		cleanupSource = true
	}
	return sourcePath, sourceName, cleanupSource, nil
}

func GetTemplateImportTempDir() string {
	if config.GlobalConfig != nil && strings.TrimSpace(config.GlobalConfig.TemplateImportDir) != "" {
		return strings.TrimSpace(config.GlobalConfig.TemplateImportDir)
	}
	if config.GlobalConfig != nil && strings.TrimSpace(config.GlobalConfig.TemplateDir) != "" {
		return filepath.Join(strings.TrimSpace(config.GlobalConfig.TemplateDir), "_imports")
	}
	return filepath.Join(os.TempDir(), "kvm_console", "template_imports")
}

func GetTemplateExportDir() string {
	if config.GlobalConfig != nil && strings.TrimSpace(config.GlobalConfig.TemplateExportDir) != "" {
		return strings.TrimSpace(config.GlobalConfig.TemplateExportDir)
	}
	if config.GlobalConfig != nil && strings.TrimSpace(config.GlobalConfig.TemplateDir) != "" {
		return filepath.Join(strings.TrimSpace(config.GlobalConfig.TemplateDir), "_exports")
	}
	return filepath.Join(os.TempDir(), "kvm_console", "template_exports")
}

func GetTemplateExportFileName(templateName string) string {
	return fmt.Sprintf("%s-template-export.tar.gz", strings.TrimSpace(templateName))
}

func GetTemplateExportMetaFileName(templateName string) string {
	return fmt.Sprintf("%s-template-export.meta.json", strings.TrimSpace(templateName))
}

func GetTemplateExportFilePath(templateName string) string {
	return filepath.Join(GetTemplateExportDir(), GetTemplateExportFileName(templateName))
}

func GetTemplateExportMetaFilePath(templateName string) string {
	return filepath.Join(GetTemplateExportDir(), GetTemplateExportMetaFileName(templateName))
}

func HasExportedTemplate(templateName string) bool {
	if strings.TrimSpace(templateName) == "" {
		return false
	}
	_, err := os.Stat(GetTemplateExportFilePath(templateName))
	return err == nil
}

func DeleteExportedTemplate(templateName string) error {
	if err := ValidateTemplateName(templateName); err != nil {
		return err
	}
	exportPath := GetTemplateExportFilePath(templateName)
	if _, err := os.Stat(exportPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("模板导出文件不存在: %s", templateName)
		}
		return fmt.Errorf("检查模板导出文件失败: %w", err)
	}
	if err := os.Remove(exportPath); err != nil {
		return fmt.Errorf("删除模板导出文件失败: %w", err)
	}
	return nil
}

func cleanupExpiredTransferFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	expireBefore := time.Now().Add(-templateTransferRetention)
	for _, entry := range entries {
		fullPath := filepath.Join(dir, entry.Name())
		info, statErr := entry.Info()
		if statErr != nil {
			continue
		}
		if info.ModTime().Before(expireBefore) {
			_ = os.RemoveAll(fullPath)
		}
	}
}

func getTemplateDownloadPath(fileName string) string {
	return "/api/template/download/" + url.PathEscape(fileName)
}

func copyFileSparseWithContext(ctx context.Context, sourcePath, targetPath string) error {
	result := utils.ExecCommandContextWithTimeout(ctx, "cp", templateCopyTimeout, "--sparse=always", sourcePath, targetPath)
	if result.Error != nil {
		return fmt.Errorf("复制文件失败: %s", result.Stderr)
	}
	return nil
}

func copyFileWithContext(ctx context.Context, sourcePath, targetPath string) error {
	result := utils.ExecCommandContextWithTimeout(ctx, "cp", 10*time.Minute, sourcePath, targetPath)
	if result.Error != nil {
		return fmt.Errorf("复制文件失败: %s", result.Stderr)
	}
	return nil
}

func validateTemplateDiskFormat(ctx context.Context, diskPath string) error {
	result := utils.ExecCommandContextWithTimeout(ctx, "qemu-img", 30*time.Second, "info", "--output=json", "-U", diskPath)
	if result.Error != nil {
		return fmt.Errorf("读取模板磁盘信息失败: %s", result.Stderr)
	}
	var info struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &info); err != nil {
		return fmt.Errorf("解析模板磁盘信息失败: %w", err)
	}
	if strings.TrimSpace(info.Format) != "qcow2" {
		return fmt.Errorf("当前仅支持导入 qcow2 模板磁盘")
	}
	return nil
}

func getTarArgsForExtract(archivePath, targetDir string) []string {
	return []string{"-xzf", archivePath, "-C", targetDir, "--no-same-owner"}
}

func buildTempExportPath(targetPath string) string {
	return fmt.Sprintf("%s.tmp-%d", targetPath, time.Now().UnixNano())
}

func safePackageFileName(nodeID, suffix string) string {
	clean := regexp.MustCompile(`[^A-Za-z0-9_-]`).ReplaceAllString(nodeID, "_")
	return clean + suffix
}

func buildTemplateManifest(templates []TemplateInfo, scope string) (*TemplatePackageManifest, error) {
	if len(templates) == 0 {
		return nil, fmt.Errorf("没有可导出的模板节点")
	}
	manifest := &TemplatePackageManifest{
		Version:     1,
		ExportedAt:  time.Now().Format(time.RFC3339),
		Scope:       scope,
		TemplateUID: templates[0].TemplateUID,
		RootNodeID:  templates[0].RootNodeID,
		Nodes:       make([]TemplatePackageNode, 0, len(templates)),
	}
	for _, tpl := range templates {
		meta := GetTemplateMeta(tpl.Name)
		if meta == nil || meta.NodeID == "" {
			return nil, fmt.Errorf("模板 %s 缺少程序元数据，无法导出", tpl.Name)
		}
		hash, err := CalculateFileHashes(tpl.Path)
		if err != nil {
			return nil, err
		}
		meta.MD5 = hash.MD5
		meta.SHA256 = hash.SHA256
		meta.FileSize = hash.FileSize
		if normalizeTemplateBootType(meta.BootType) == "" {
			meta.BootType = DetectTemplateBootType(tpl.Path)
		}
		if err := saveTemplateMeta(tpl.Path, meta); err != nil {
			return nil, err
		}
		diskFile := safePackageFileName(meta.NodeID, ".qcow2")
		metaFile := safePackageFileName(meta.NodeID, ".meta.json")
		manifest.Nodes = append(manifest.Nodes, TemplatePackageNode{
			Name:     tpl.Name,
			DiskFile: diskFile,
			MetaFile: metaFile,
			Meta:     *meta,
			FileSize: hash.FileSize,
			MD5:      hash.MD5,
			SHA256:   hash.SHA256,
		})
	}
	return manifest, nil
}

func ExportTemplate(ctx context.Context, params *ExportTemplateParams, progressFn func(int, string)) (*ExportTemplateResult, error) {
	if progressFn == nil {
		progressFn = func(int, string) {}
	}
	if params == nil || strings.TrimSpace(params.TemplateName) == "" {
		return nil, fmt.Errorf("模板名称不能为空")
	}
	tree, err := buildTemplateTreeData()
	if err != nil {
		return nil, err
	}
	start, ok := tree.byName[params.TemplateName]
	if !ok {
		return nil, fmt.Errorf("模板不存在: %s", params.TemplateName)
	}
	scope := strings.ToLower(strings.TrimSpace(params.Scope))
	startID := start.NodeID
	if scope == "root" {
		startID = start.RootNodeID
		if startID == "" {
			startID = start.NodeID
		}
	}
	templates := collectTemplateSubtree(tree, startID)
	if scope == "" {
		scope = "node"
	}
	progressFn(10, "正在准备模板导出包...")
	manifest, err := buildTemplateManifest(templates, scope)
	if err != nil {
		return nil, err
	}

	exportDir := GetTemplateExportDir()
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建导出目录失败: %w", err)
	}
	stageDir, err := os.MkdirTemp(exportDir, "package-")
	if err != nil {
		return nil, fmt.Errorf("创建导出临时目录失败: %w", err)
	}
	defer os.RemoveAll(stageDir)

	for i, node := range manifest.Nodes {
		progressFn(15+(i*60/maxInt(len(manifest.Nodes), 1)), fmt.Sprintf("正在写入节点 %s ...", node.Meta.AdminName))
		if err := copyFileSparseWithContext(ctx, filepath.Join(config.GlobalConfig.TemplateDir, node.Name+".qcow2"), filepath.Join(stageDir, node.DiskFile)); err != nil {
			return nil, err
		}
		metaData, _ := json.MarshalIndent(node.Meta, "", "  ")
		if err := os.WriteFile(filepath.Join(stageDir, node.MetaFile), metaData, 0o644); err != nil {
			return nil, fmt.Errorf("写入节点元数据失败: %w", err)
		}
	}
	manifestData, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(stageDir, "manifest.json"), manifestData, 0o644); err != nil {
		return nil, fmt.Errorf("写入导出清单失败: %w", err)
	}

	exportFileName := GetTemplateExportFileName(params.TemplateName)
	exportPath := filepath.Join(exportDir, exportFileName)
	tempExportPath := buildTempExportPath(exportPath)
	progressFn(82, "正在压缩模板导出包...")
	result := utils.ExecCommandContextWithTimeout(ctx, "tar", templateCopyTimeout, "-czf", tempExportPath, "-C", stageDir, ".")
	if result.Error != nil {
		_ = os.Remove(tempExportPath)
		return nil, fmt.Errorf("压缩模板导出包失败: %s", result.Stderr)
	}
	if err := os.Rename(tempExportPath, exportPath); err != nil {
		_ = os.Remove(tempExportPath)
		return nil, fmt.Errorf("替换模板导出包失败: %w", err)
	}
	_ = utils.ExecCommand("chown", "libvirt-qemu:kvm", exportPath)
	sizeResult := utils.ExecShell(fmt.Sprintf("du -h %s | awk '{print $1}'", utils.ShellSingleQuote(exportPath)))
	fileSize := "未知"
	if sizeResult.Error == nil && strings.TrimSpace(sizeResult.Stdout) != "" {
		fileSize = strings.TrimSpace(sizeResult.Stdout)
	}
	progressFn(100, "模板导出完成，可前往任务中心下载")
	return &ExportTemplateResult{
		TemplateName: params.TemplateName,
		FileName:     exportFileName,
		FileSize:     fileSize,
		DownloadPath: getTemplateDownloadPath(exportFileName),
	}, nil
}

func readTemplatePackageManifest(ctx context.Context, archivePath string) (*TemplatePackageManifest, string, error) {
	extractDir, err := os.MkdirTemp(GetTemplateImportTempDir(), "preview-")
	if err != nil {
		return nil, "", fmt.Errorf("创建预览目录失败: %w", err)
	}
	result := utils.ExecCommandContextWithTimeout(ctx, "tar", templateCopyTimeout, getTarArgsForExtract(archivePath, extractDir)...)
	if result.Error != nil {
		_ = os.RemoveAll(extractDir)
		return nil, "", fmt.Errorf("解压模板包失败: %s", result.Stderr)
	}
	data, err := os.ReadFile(filepath.Join(extractDir, "manifest.json"))
	if err != nil {
		_ = os.RemoveAll(extractDir)
		return nil, "", fmt.Errorf("模板包缺少 manifest.json")
	}
	var manifest TemplatePackageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		_ = os.RemoveAll(extractDir)
		return nil, "", fmt.Errorf("解析模板包清单失败: %w", err)
	}
	if manifest.Version <= 0 || len(manifest.Nodes) == 0 || strings.TrimSpace(manifest.TemplateUID) == "" {
		_ = os.RemoveAll(extractDir)
		return nil, "", fmt.Errorf("模板包清单不完整")
	}
	for _, node := range manifest.Nodes {
		if filepath.Base(node.DiskFile) != node.DiskFile || filepath.Base(node.MetaFile) != node.MetaFile {
			_ = os.RemoveAll(extractDir)
			return nil, "", fmt.Errorf("模板包包含非法文件路径")
		}
	}
	return &manifest, extractDir, nil
}

func PreviewImportTemplate(ctx context.Context, params *ImportTemplateParams) (*ImportTemplatePreviewResult, error) {
	sourcePath, sourceName, cleanupSource, err := resolveImportTemplateSource(params)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(strings.ToLower(sourceName), ".tar.gz") && !strings.HasSuffix(strings.ToLower(sourceName), ".tgz") {
		return nil, fmt.Errorf("模板链路导入仅支持 .tar.gz / .tgz 模板包")
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return nil, fmt.Errorf("导入文件不存在或不可访问")
	}
	cleanupExpiredTransferFiles(GetTemplateImportTempDir())
	manifest, extractDir, err := readTemplatePackageManifest(ctx, sourcePath)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(extractDir)

	tree, err := buildTemplateTreeData()
	if err != nil {
		return nil, err
	}
	existingUID := false
	for _, tpl := range tree.templates {
		if tpl.TemplateUID == manifest.TemplateUID {
			existingUID = true
			break
		}
	}
	mode := "create"
	if existingUID {
		mode = "update"
	}

	result := &ImportTemplatePreviewResult{
		Mode:        mode,
		TemplateUID: manifest.TemplateUID,
		RootNodeID:  manifest.RootNodeID,
		CanImport:   true,
		Nodes:       make([]ImportTemplatePreviewNode, 0, len(manifest.Nodes)),
	}
	for _, node := range manifest.Nodes {
		meta := node.Meta
		meta.Category = normalizeTemplateCategoryForName(meta.Type, meta.Category, node.Name)
		existingTpl, exists := tree.byNodeID[meta.NodeID]
		previewNode := ImportTemplatePreviewNode{
			Name:         node.Name,
			AdminName:    meta.AdminName,
			DisplayName:  meta.DisplayName,
			Category:     meta.Category,
			TemplateUID:  meta.TemplateUID,
			NodeID:       meta.NodeID,
			ParentNodeID: meta.ParentNodeID,
			RootNodeID:   meta.RootNodeID,
			Type:         meta.Type,
			CloneVisible: meta.CloneVisible,
			Disabled:     meta.Disabled,
			FileSize:     node.FileSize,
			MD5:          node.MD5,
			SHA256:       node.SHA256,
			Exists:       exists,
			WillImport:   !exists,
			Meta:         meta,
		}
		if exists {
			if existingTpl.TemplateUID != manifest.TemplateUID {
				previewNode.ConflictReason = "节点 ID 已被其他模板树占用"
				result.CanImport = false
			}
		}
		if _, nameExists := tree.byName[node.Name]; nameExists && !exists {
			previewNode.ConflictReason = "模板文件名已存在"
			result.CanImport = false
		}
		if _, statErr := os.Stat(filepath.Join(extractDir, node.DiskFile)); statErr != nil {
			previewNode.ConflictReason = "模板包缺少节点磁盘"
			result.CanImport = false
		}
		result.Nodes = append(result.Nodes, previewNode)
	}
	if result.CanImport {
		result.Message = "模板包校验通过，可确认导入"
	} else {
		result.Message = "模板包存在冲突，请处理后重新导入"
	}
	token := generateTemplateID("import")
	templateImportPreviewStore.Lock()
	templateImportPreviewStore.items[token] = templateImportPreviewSession{
		SourcePath:    sourcePath,
		SourceName:    sourceName,
		CleanupSource: cleanupSource,
		CreatedAt:     time.Now(),
	}
	templateImportPreviewStore.Unlock()
	result.Token = token
	return result, nil
}

func ResolveImportPreviewToken(token string) (*ImportTemplateParams, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("导入预览 token 不能为空")
	}
	templateImportPreviewStore.Lock()
	session, ok := templateImportPreviewStore.items[token]
	if ok {
		delete(templateImportPreviewStore.items, token)
	}
	templateImportPreviewStore.Unlock()
	if !ok {
		return nil, fmt.Errorf("导入预览已过期，请重新上传模板包")
	}
	if time.Since(session.CreatedAt) > templateTransferRetention {
		if session.CleanupSource {
			_ = os.Remove(session.SourcePath)
		}
		return nil, fmt.Errorf("导入预览已过期，请重新上传模板包")
	}
	return &ImportTemplateParams{
		SourcePath:    session.SourcePath,
		SourceName:    session.SourceName,
		CleanupSource: session.CleanupSource,
	}, nil
}

func ImportTemplate(ctx context.Context, params *ImportTemplateParams, progressFn func(int, string)) (*ImportTemplateResult, error) {
	if progressFn == nil {
		progressFn = func(int, string) {}
	}
	sourcePath, sourceName, cleanupSource, err := resolveImportTemplateSource(params)
	if err != nil {
		return nil, err
	}
	if cleanupSource {
		defer os.Remove(sourcePath)
	}
	if strings.HasSuffix(strings.ToLower(sourceName), ".qcow2") {
		return importLegacySingleTemplate(ctx, params, sourcePath, progressFn)
	}
	progressFn(5, "正在读取模板包...")
	manifest, extractDir, err := readTemplatePackageManifest(ctx, sourcePath)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(extractDir)
	tree, err := buildTemplateTreeData()
	if err != nil {
		return nil, err
	}
	mode := "create"
	for _, tpl := range tree.templates {
		if tpl.TemplateUID == manifest.TemplateUID {
			mode = "update"
			break
		}
	}
	if mode == "update" {
		progressFn(20, "正在校验本地模板链路完整性...")
		for _, node := range manifest.Nodes {
			if tpl, exists := tree.byNodeID[node.Meta.NodeID]; exists {
				if err := VerifyTemplateFileIntegrity(tpl); err != nil {
					return nil, fmt.Errorf("%s：%w", tpl.AdminName, err)
				}
			}
		}
	}

	templateDir := config.GlobalConfig.TemplateDir
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建模板目录失败: %w", err)
	}
	imported := make([]string, 0)
	skipped := make([]string, 0)
	for i, node := range manifest.Nodes {
		if _, exists := tree.byNodeID[node.Meta.NodeID]; exists {
			skipped = append(skipped, node.Name)
			continue
		}
		if _, exists := tree.byName[node.Name]; exists {
			return nil, fmt.Errorf("模板文件名已存在: %s", node.Name)
		}
		progressFn(30+(i*60/maxInt(len(manifest.Nodes), 1)), fmt.Sprintf("正在导入节点 %s ...", node.Meta.AdminName))
		sourceDisk := filepath.Join(extractDir, node.DiskFile)
		if err := validateTemplateDiskFormat(ctx, sourceDisk); err != nil {
			return nil, err
		}
		hash, err := CalculateFileHashes(sourceDisk)
		if err != nil {
			return nil, err
		}
		if hash.FileSize != node.FileSize || !strings.EqualFold(hash.MD5, node.MD5) || !strings.EqualFold(hash.SHA256, node.SHA256) {
			return nil, fmt.Errorf("模板包节点 %s 哈希不匹配，导入已拒绝", node.Meta.AdminName)
		}
		targetPath := filepath.Join(templateDir, node.Name+".qcow2")
		if err := copyFileSparseWithContext(ctx, sourceDisk, targetPath); err != nil {
			_ = os.Remove(targetPath)
			return nil, err
		}
		meta := node.Meta
		meta.Category = normalizeTemplateCategoryForName(meta.Type, meta.Category, node.Name)
		meta.MD5 = hash.MD5
		meta.SHA256 = hash.SHA256
		meta.FileSize = hash.FileSize
		if normalizeTemplateBootType(meta.BootType) == "" {
			meta.BootType = DetectTemplateBootType(sourceDisk)
		}
		if err := saveTemplateMeta(targetPath, &meta); err != nil {
			_ = os.Remove(targetPath)
			return nil, err
		}
		_ = utils.ExecCommand("chown", "libvirt-qemu:kvm", targetPath)
		_ = utils.ExecCommand("chown", "libvirt-qemu:kvm", getMetaPath(targetPath))
		imported = append(imported, node.Name)
	}
	progressFn(100, "模板包导入完成")
	return &ImportTemplateResult{
		Mode:     mode,
		Imported: imported,
		Skipped:  skipped,
		HasMeta:  true,
	}, nil
}

func importLegacySingleTemplate(ctx context.Context, params *ImportTemplateParams, sourcePath string, progressFn func(int, string)) (*ImportTemplateResult, error) {
	if err := ValidateTemplateName(params.TemplateName); err != nil {
		return nil, err
	}
	targetPath, err := ensureTemplateTargetPath(params.TemplateName)
	if err != nil {
		return nil, err
	}
	progressFn(20, "正在校验模板磁盘...")
	if err := validateTemplateDiskFormat(ctx, sourcePath); err != nil {
		return nil, err
	}
	progressFn(60, "正在写入模板磁盘...")
	if err := copyFileSparseWithContext(ctx, sourcePath, targetPath); err != nil {
		_ = os.Remove(targetPath)
		return nil, err
	}
	tplType := normalizeTemplateType(params.Type)
	if tplType == "" {
		tplType = detectTemplateTypeFromName(params.TemplateName)
	}
	hash, err := CalculateFileHashes(targetPath)
	if err != nil {
		_ = os.Remove(targetPath)
		return nil, err
	}
	meta := &TemplateMeta{
		Type:         tplType,
		Category:     normalizeTemplateCategoryForName(tplType, "", params.TemplateName),
		BootType:     DetectTemplateBootType(targetPath),
		RootPassword: params.RootPassword,
		TemplateUser: params.TemplateUser,
		TemplateUID:  generateTemplateID("tpl"),
		NodeID:       generateTemplateID("node"),
		AdminName:    params.TemplateName,
		DisplayName:  params.TemplateName,
		CloneVisible: true,
		CreatedAt:    time.Now().Format(time.RFC3339),
		MD5:          hash.MD5,
		SHA256:       hash.SHA256,
		FileSize:     hash.FileSize,
	}
	meta.RootNodeID = meta.NodeID
	if err := saveTemplateMeta(targetPath, meta); err != nil {
		_ = os.Remove(targetPath)
		return nil, err
	}
	_ = utils.ExecCommand("chown", "libvirt-qemu:kvm", targetPath)
	_ = utils.ExecCommand("chown", "libvirt-qemu:kvm", getMetaPath(targetPath))
	progressFn(100, "模板导入完成")
	return &ImportTemplateResult{
		TemplateName: params.TemplateName,
		Path:         targetPath,
		Type:         meta.Type,
		HasMeta:      true,
		Mode:         "create",
		Imported:     []string{params.TemplateName},
	}, nil
}

func ensureTemplateTargetPath(templateName string) (string, error) {
	if err := ValidateTemplateName(templateName); err != nil {
		return "", err
	}
	templateDir := config.GlobalConfig.TemplateDir
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		return "", fmt.Errorf("创建模板目录失败: %w", err)
	}
	targetPath := filepath.Join(templateDir, templateName+".qcow2")
	if _, err := os.Stat(targetPath); err == nil {
		return "", fmt.Errorf("模板已存在: %s", templateName)
	}
	return targetPath, nil
}
