package template

import (
	"errors"
	"strings"

	"kvm_console/config"
)

// ImportParams 模板导入参数。SourcePath 为已落地的 rootfs tarball 绝对路径。
type ImportParams struct {
	Name              string `json:"name"`
	DisplayName       string `json:"display_name"`
	Distro            string `json:"distro"`
	Release           string `json:"release"`
	Arch              string `json:"arch"`
	Description       string `json:"description"`
	SourcePath        string `json:"source_path"`
	PostCreateCommand string `json:"post_create_command"`
	OwnerUsername     string `json:"owner_username"`
}

func baseContainerName(name string) string {
	return config.GlobalConfig.LXCBasePrefix + name
}

func isBaseContainer(name string) bool {
	return strings.HasPrefix(name, config.GlobalConfig.LXCBasePrefix)
}

func validateImportParams(p *ImportParams) error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("模板名称不能为空")
	}
	if strings.TrimSpace(p.SourcePath) == "" {
		return errors.New("必须提供 rootfs tarball 路径")
	}
	if p.Arch != "" && p.Arch != "amd64" && p.Arch != "arm64" {
		return errors.New("架构仅支持 amd64/arm64")
	}
	return nil
}
