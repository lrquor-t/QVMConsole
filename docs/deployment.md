# QVMConsole 部署指南

## 一、快速安装

### 方式一：在线安装（推荐）

```bash
# 下载最新安装脚本并执行
curl -fsSL https://raw.githubusercontent.com/yxsj245/kvm_console/main/install.sh | bash
```

> **注意**：该方式会自动从 GitHub Releases 下载最新版本。
> 脚本会先检测 CPU 是否开启 Intel VT-x (`vmx`) 或 AMD-V (`svm`) 硬件虚拟化；未开启时会拒绝安装。

### 方式二：离线安装

1. 从 [GitHub Releases](https://github.com/yxsj245/kvm_console/releases) 下载最新的 `kvm-console-linux-amd64.tar.gz`

2. 解压并运行安装脚本：

```bash
tar -xzf kvm-console-linux-amd64.tar.gz
cd kvm-console
sudo bash install.sh
```

## 二、安装过程

安装脚本会自动执行以下步骤：

1. **系统检测** - 检查操作系统类型和 CPU 架构
2. **KVM 检测** - 检查 CPU 硬件虚拟化标记、KVM 内核模块和 `/dev/kvm`
3. **依赖安装** - 检测并安装以下 apt 依赖包：
   - `qemu-system-x86` / `qemu-utils` - KVM 虚拟化与磁盘镜像工具
   - `libvirt-daemon-system` - libvirt 守护进程
   - `libvirt-clients` - virsh 管理工具
   - `openvswitch-switch` - OVS 网络
   - `dnsmasq-base` - OVS/VPC DHCP
   - `virtinst` - virt-install 工具
   - `libguestfs-tools` - 磁盘检测工具
   - `ntfs-3g` - Windows NTFS 处理工具
   - `sshpass` - SSH 自动登录
   - `cloud-image-utils` - cloud-init 工具
   - `ovmf` - UEFI 固件
   - `lvm2` - LVM 逻辑卷管理
   - `cloud-guest-utils` - 磁盘扩容工具
   - `quota` / `e2fsprogs` - Project Quota 与 ext4 工具
   - `nftables` / `iptables` / `ufw` / `conntrack` / `iproute2` - 防火墙、NAT、转发和限速
   - `openssh-client` / `openssh-server` - 用户 SSH 开关与克隆初始化
4. **存储配额** - 创建或挂载 `/var/lib/kvm-user-storage.img`
5. **端口配置** - 让用户输入网页访问端口（默认 8080）
6. **文件安装** - 安装程序到 `/opt/kvm-console/`
7. **配置补齐** - 生成或合并 `/opt/kvm-console/.env`
8. **运行地基** - 补齐目录、OVS dnsmasq、IPv4 转发、本机 DHCP/DNS 入站规则、systemd 服务
9. **服务配置** - 创建 systemd 服务并设置开机自启

## 三、更新

再次运行安装脚本会进入菜单：

```text
1. 更新
2. 卸载
```

选择 `1` 即可更新：

```bash
# 在线更新
curl -fsSL https://raw.githubusercontent.com/yxsj245/kvm_console/main/install.sh | bash

# 或从下载的新版本包中更新
sudo bash install.sh
```

更新时会：
- 自动检测已安装的版本
- 保留已有配置，按需追加新版本缺失的 `.env` 配置项
- 重新检查新增 apt 依赖、系统命令、目录、Project Quota、OVS DHCP、本机 DHCP/DNS 入站规则和 systemd unit
- 停止服务 → 更新文件 → 重启服务

## 四、目录结构

```
/opt/kvm-console/
├── kvm-console          # 后端二进制文件
├── web-dist/            # 前端静态文件
├── data/                # 数据目录
│   └── kvm_console.db   # SQLite 数据库
└── .env                 # 环境变量配置
```

## 五、配置说明

配置文件位于 `/opt/kvm-console/.env`，支持以下配置项：

| 环境变量 | 默认值 | 说明 |
|---------|--------|------|
| `KVM_PORT` | `8080` | 网页访问端口 |
| `KVM_DB_PATH` | `/opt/kvm-console/data/kvm_console.db` | 数据库路径 |
| `KVM_JWT_SECRET` | 随机生成 | JWT 加密密钥 |
| `KVM_VM_CREDENTIAL_SECRET` | 随机生成 | VM 凭据加密密钥，旧版本升级缺失时保持为空并回退到 JWT 密钥 |
| `KVM_SECURITY_SECRET` | 随机生成 | 账户安全加密密钥，旧版本升级缺失时保持为空并回退到 JWT 密钥 |
| `KVM_JWT_EXPIRE_HOURS` | `24` | JWT 过期时间（小时） |
| `KVM_TEMPLATE_DIR` | `/var/lib/libvirt/images/templates` | 模板目录 |
| `KVM_TEMPLATE_IMPORT_DIR` | `/var/lib/libvirt/images/templates/_imports` | 模板导入临时目录 |
| `KVM_TEMPLATE_EXPORT_DIR` | `/var/lib/libvirt/images/templates/_exports` | 模板导出目录 |
| `KVM_CLONE_DIR` | `/var/lib/libvirt/images` | 克隆磁盘目录 |
| `KVM_ISO_DIR` | `/var/lib/libvirt/images/ISO` | 全局 ISO 目录 |
| `KVM_DEFAULT_NETWORK` | `default` | 默认网络名称 |
| `KVM_NETWORK_BACKEND` | `ovs` | 网络后端，当前新平台使用 OVS |
| `KVM_OVS_BRIDGE` | `br-ovs` | OVS 网桥名称 |
| `KVM_OVS_UPLINK` | 自动检测 | OVS NAT 出口网卡 |
| `KVM_OVS_DHCP_START` | 自动 | OVS DHCP 起始地址 |
| `KVM_OVS_DHCP_END` | 自动 | OVS DHCP 结束地址 |
| `KVM_SUBNET_PREFIX` | `192.168.122` | 网段前缀 |
| `KVM_AUTO_PORT_START` | `10000` | 自动端口范围开始 |
| `KVM_AUTO_PORT_END` | `20000` | 自动端口范围结束 |
| `KVM_ADMIN_USER` | `admin` | 默认管理员用户名 |
| `KVM_ADMIN_PASS` | `admin123` | 默认管理员密码 |
| `KVM_HOST_IP` | 自动检测 | 宿主机外网 IP |
| `KVM_EXTERNAL_NIC` | 自动检测 | 外网网卡名称 |
| `KVM_MAX_BURST_INBOUND` | `0` | 外网最大下行速率（Mbps），用于限速突发量计算 |
| `KVM_MAX_BURST_OUTBOUND` | `0` | 外网最大上行速率（Mbps），用于限速突发量计算 |
| `KVM_RESCUE_ISO` | 空 | 救援系统 ISO 路径 |
| `KVM_PUBLIC_BASE_URL` | 空 | 邮件中的面板访问地址 |
| `KVM_SITE_TITLE` | `QVMConsole` | 网站标题 |
| `KVM_DEVELOPMENT_MODE` | `false` | 开发环境模式 |
| `KVM_SERVICE_UNIT_NAME` | `kvm-console.service` | 当前面板 systemd unit 名称 |
| `KVM_MAINTENANCE_MODE` | `false` | 维护模式开关 |
| `KVM_MAINTENANCE_SERVICE_UNITS` | `kvm-console.service,...` | 维护模式涉及的 systemd units |
| `KVM_MAINTENANCE_VM_SHUTDOWN_TIMEOUT_SECONDS` | `40` | 维护模式关闭 VM 等待秒数 |
| `KVM_SMTP_HOST` | 空 | SMTP 主机 |
| `KVM_SMTP_PORT` | `587` | SMTP 端口 |
| `KVM_SMTP_USERNAME` | 空 | SMTP 用户名 |
| `KVM_SMTP_PASSWORD_ENC` | 空 | 加密后的 SMTP 密码 |
| `KVM_SMTP_FROM_NAME` | `QVMConsole` | 邮件发件人名称 |
| `KVM_SMTP_FROM_ADDRESS` | 空 | 邮件发件地址 |
| `KVM_SMTP_SECURITY` | `starttls` | SMTP 安全模式：`none/starttls/ssl` |
| `KVM_SMTP_TIMEOUT_SECONDS` | `15` | SMTP 超时秒数 |
| `KVM_DYNAMIC_MEMORY_SCHEDULER_ENABLED` | `true` | 动态内存调度开关 |
| `KVM_DYNAMIC_MEMORY_INTERVAL_SECONDS` | `30` | 动态内存调度间隔 |
| `KVM_DYNAMIC_MEMORY_HOST_RESERVE_MB` | `2048` | 宿主机保留内存 MB |
| `KVM_DYNAMIC_MEMORY_HOST_RESERVE_PERCENT` | `20` | 宿主机保留内存百分比 |
| `KVM_DYNAMIC_MEMORY_INCREASE_THRESHOLD_PERCENT` | `15` | 动态内存扩容阈值 |
| `KVM_DYNAMIC_MEMORY_RECLAIM_THRESHOLD_PERCENT` | `35` | 动态内存回收阈值 |
| `KVM_DYNAMIC_MEMORY_COOLDOWN_SECONDS` | `120` | 动态内存操作冷却时间 |
| `KVM_DYNAMIC_MEMORY_OBSERVATION_HOURS` | `24` | 动态内存观察窗口 |
| `KVM_SCHEDULER_EVENT_RETENTION_HOURS` | `168` | 调度事件保留小时数 |
| `KVM_VPC_SUBNET_PREFIX` | `10.200` | VPC 自动子网前缀 |
| `KVM_VPC_VLAN_START` | `100` | VPC VLAN 起始 ID |
| `KVM_VPC_VLAN_END` | `4094` | VPC VLAN 结束 ID |
| `KVM_VPC_DNS` | `223.5.5.5,223.6.6.6` | VPC DHCP DNS |
| `KVM_VPC_ACL_TABLE` | `kvm_console_vpc_acl` | VPC ACL nftables 表名 |

修改配置后重启服务生效：

```bash
sudo systemctl restart kvm-console
```

## 六、常用命令

```bash
# 查看服务状态
sudo systemctl status kvm-console

# 查看实时日志
sudo journalctl -u kvm-console -f

# 重启服务
sudo systemctl restart kvm-console

# 停止服务
sudo systemctl stop kvm-console

# 启动服务
sudo systemctl start kvm-console
```

## 七、卸载

再次运行安装脚本并选择 `2. 卸载`。脚本默认保留数据库、配置、虚拟机磁盘、模板、libvirt 定义和用户存储镜像；只有在二次确认时选择删除安装目录，才会删除 `/opt/kvm-console` 中的数据和配置。

手动卸载时可参考：

```bash
sudo systemctl disable --now kvm-console
sudo rm -f /etc/systemd/system/kvm-console.service
sudo systemctl daemon-reload

# 如需彻底删除面板数据库和配置，请提前备份
sudo rm -rf /opt/kvm-console
```

## 八、本地构建打包

项目根目录提供 `build.sh` 脚本，可在本地构建前端和后端并打包为发行文件。

### 基本用法

```bash
# 完整构建（版本号默认为 dev）
bash build.sh

# 指定版本号构建
bash build.sh -v 1.0.0

# 仅构建后端（跳过前端）
bash build.sh --skip-frontend

# 仅构建前端（跳过后端）
bash build.sh --skip-backend
```

### 构建产物

构建完成后，产物位于 `release/` 目录：

```
release/
├── kvm-console-linux-amd64.tar.gz   # 可直接用于离线安装的发行包
└── kvm-console-linux-amd64/         # 解压后的文件
    ├── kvm-console                  # 后端二进制
    ├── web-dist/                    # 前端静态文件
    └── install.sh                   # 安装脚本
```

### 环境要求

- **Node.js** v20+（前端构建）
- **Go** 1.25+（后端构建，需支持 CGO 交叉编译到 linux/amd64）

## 九、GitHub Actions 构建

项目包含 GitHub Actions 工作流，支持自动构建：

- **自动触发**：推送 `v*` 格式的标签时自动构建并创建 Release
- **手动触发**：在 GitHub Actions 页面手动触发构建

### 创建发布版本

```bash
git tag v1.0.0
git push origin v1.0.0
```

这会自动触发 CI 构建并创建包含 `kvm-console-linux-amd64.tar.gz` 的 Release。
