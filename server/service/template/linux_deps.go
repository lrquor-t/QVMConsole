package template

import (
	"kvm_console/logger"
	"kvm_console/utils"
)

// PreinstallLinuxCloudInitDeps 在制作 Linux 模板时预装 cloud-init 和 growpart 依赖
// 使用国内镜像源加速，安装失败仅告警不阻断模板制作
func PreinstallLinuxCloudInitDeps(templatePath string) error {
	logger.App.Info("预装 Linux 克隆依赖包（cloud-init, growpart）", "template", templatePath)

	// 构建 virt-customize 命令，使用国内镜像源并安装依赖
	args := []string{
		"-a", templatePath,
		// 检测并安装 cloud-init 和 growpart（使用国内镜像源加速）
		"--run-command", `
			set -e
			# === DNF 系（Fedora/RHEL/CentOS/openEuler 等）===
			if command -v dnf >/dev/null 2>&1; then
				if ! rpm -q cloud-init cloud-utils-growpart &>/dev/null; then
					echo "[QVM] 检测到 DNF 包管理器，配置国内镜像源..."
					# 配置国内镜像源（阿里云）
					for repo in /etc/yum.repos.d/*.repo; do
						[ -f "$repo" ] || continue
						sed -i 's|^mirrorlist=|#mirrorlist=|g; s|^metalink=|#metalink=|g' "$repo"
						sed -i 's|mirror.centos.org|mirrors.aliyun.com|g; s|dl.fedoraproject.org/pub|mirrors.aliyun.com|g' "$repo"
						sed -i 's|^#baseurl=|baseurl=|g' "$repo"
					done
					echo "[QVM] 安装 cloud-init 和 cloud-utils-growpart..."
					if dnf install -y cloud-init cloud-utils-growpart 2>&1; then
						echo "[QVM] 依赖安装成功"
					else
						echo "[QVM-WARN] DNF 安装失败，磁盘自动扩容功能可能不可用" >&2
					fi
				else
					echo "[QVM] cloud-init 和 cloud-utils-growpart 已安装，跳过"
				fi
			# === APT 系（Debian/Ubuntu 等）===
			elif command -v apt-get >/dev/null 2>&1; then
				if ! dpkg -s cloud-init cloud-guest-utils &>/dev/null; then
					echo "[QVM] 检测到 APT 包管理器，配置国内镜像源..."
					# 配置国内镜像源（阿里云）
					for f in /etc/apt/sources.list /etc/apt/sources.list.d/*.list; do
						[ -f "$f" ] || continue
						sed -i 's|http://archive.ubuntu.com|https://mirrors.aliyun.com|g; s|http://security.ubuntu.com|https://mirrors.aliyun.com|g; s|http://deb.debian.org|https://mirrors.aliyun.com|g; s|http://security.debian.org|https://mirrors.aliyun.com/debian-security|g' "$f"
					done
					echo "[QVM] 更新软件包索引..."
					if apt-get update -qq 2>&1; then
						echo "[QVM] 安装 cloud-init 和 cloud-guest-utils..."
						if DEBIAN_FRONTEND=noninteractive apt-get install -y cloud-init cloud-guest-utils 2>&1; then
							echo "[QVM] 依赖安装成功"
						else
							echo "[QVM-WARN] APT 安装失败，磁盘自动扩容功能可能不可用" >&2
						fi
					else
						echo "[QVM-WARN] APT 更新失败（可能无网络），磁盘自动扩容功能可能不可用" >&2
					fi
				else
					echo "[QVM] cloud-init 和 cloud-guest-utils 已安装，跳过"
				fi
			# === YUM 系（旧版 CentOS 等）===
			elif command -v yum >/dev/null 2>&1; then
				if ! rpm -q cloud-init cloud-utils-growpart &>/dev/null; then
					echo "[QVM] 检测到 YUM 包管理器，配置国内镜像源..."
					# 配置国内镜像源（阿里云）
					for repo in /etc/yum.repos.d/*.repo; do
						[ -f "$repo" ] || continue
						sed -i 's|^mirrorlist=|#mirrorlist=|g' "$repo"
						sed -i 's|mirror.centos.org|mirrors.aliyun.com|g' "$repo"
						sed -i 's|^#baseurl=|baseurl=|g' "$repo"
					done
					echo "[QVM] 安装 cloud-init 和 cloud-utils-growpart..."
					if yum install -y cloud-init cloud-utils-growpart 2>&1; then
						echo "[QVM] 依赖安装成功"
					else
						echo "[QVM-WARN] YUM 安装失败，磁盘自动扩容功能可能不可用" >&2
					fi
				else
					echo "[QVM] cloud-init 和 cloud-utils-growpart 已安装，跳过"
				fi
			else
				echo "[QVM-WARN] 未检测到支持的包管理器（dnf/apt/yum），跳过依赖安装" >&2
			fi
		`,
		"--quiet",
	}

	result := utils.ExecCommandLongRunning("virt-customize", args...)
	if result.Error != nil {
		// 依赖安装失败不阻断模板制作，克隆时仍可使用 virt-customize 离线方式初始化
		logger.App.Warn("Linux 依赖预装失败（不影响模板制作）", "error", result.Stderr)
		return nil
	}

	logger.App.Info("Linux 克隆依赖预装完成", "template", templatePath)
	return nil
}
