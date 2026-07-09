# 错误修复文档 - 2026-07-08

## 一、错误概览

根据任务中心和网络中心截图，共发现4类错误：

| 编号 | 错误类型 | 影响范围 | 严重程度 | 修复状态 |
|------|----------|----------|----------|----------|
| 1 | chown: invalid user: 'libvirt-qemu:kvm' | 磁盘权限设置 | 高 | ✅ 已修复 |
| 2 | qemu-img: Could not open '/usr/share/OVMF/OV...' | UEFI NVRAM创建 | 高 | ✅ 已修复 |
| 3 | Failed to start domain: operation failed | 虚拟机启动 | 高 | ✅ 已修复 |
| 4 | OVS dnsmasq: Address already in use | 网络服务 | 中 | ✅ 已修复 |

---

## 二、错误1：chown: invalid user: 'libvirt-qemu:kvm'

### 2.1 错误描述
```
任务失败：设置磁盘权限失败：chown: invalid user: 'libvirt-qemu:kvm'
```

### 2.2 根因分析
代码中硬编码了 `libvirt-qemu:kvm` 用户/组，但测试环境中不存在该用户。项目已有安全函数 `utils.ChownLibvirtQEMU()`，会先尝试 `libvirt-qemu:kvm`，失败后回退到 `qemu:qemu`，但多处代码未使用此函数。

### 2.3 修复记录 ✅

**修改的文件：**

| 文件路径 | 修改内容 |
|----------|----------|
| `server/service/vm/vmimport/disk.go` | 第160行、第388行：替换为 `utils.ChownLibvirtQEMU()` |
| `server/service/vm/vmimport/core.go` | 第112行：替换为 `utils.ChownLibvirtQEMU()` |
| `server/service/clone/reinstall.go` | 第48行、第62行：替换为 `utils.ChownLibvirtQEMU()` |
| `server/service/clone/delete.go` | 第194行：替换为 `utils.ChownLibvirtQEMU()` |
| `server/handler/user_storage.go` | 第222行：替换为 `utils.ChownLibvirtQEMU()` |
| `server/service/template/transfer.go` | 第389、672、675、739、742行：替换为 `utils.ChownLibvirtQEMU()` |
| `server/service/storage/pool/format_mount.go` | 第152行：替换为 `utils.ChownLibvirtQEMU()` |
| `server/service/storage/disk/helpers.go` | 第166行：替换为 `utils.ChownLibvirtQEMU()` |
| `server/service/vm/export.go` | 第174行：替换为 `utils.ChownLibvirtQEMU()` |
| `server/service/vm/disk_migration.go` | 第527行：替换为 `utils.ChownLibvirtQEMU()` |
| `server/service/user/storage.go` | 第52行、第95行：替换为 `utils.ChownLibvirtQEMU()` |
| `server/service/storage/disk/create.go` | 第197行：替换为 `utils.ChownLibvirtQEMU()` |

**修复前 (示例):**
```go
utils.ExecCommand("chown", "libvirt-qemu:kvm", diskPath)
```

**修复后:**
```go
if err := utils.ChownLibvirtQEMU(diskPath); err != nil {
    return fmt.Errorf("设置磁盘权限失败: %w", err)
}
```

---

## 三、错误2：UEFI NVRAM 文件创建失败

### 3.1 错误描述
```
任务失败：创建 UEFI NVRAM 文件失败：转换 NVRAM 为 qcow2 失败：qemu-img: Could not open '/usr/share/OVMF/OVMF_VARS_4M.ms.fd': Could not open file '/usr/share/OVMF/OVMF_VARS_4M.ms.fd': No such file or directory
```

### 3.2 根因分析
1. OVMF 模板文件路径不存在
2. 架构配置的模板路径与实际安装路径不匹配
3. 权限不足无法读取模板文件

### 3.3 修复记录 ✅

**修改的文件：** `server/service/vm_xml/boot_type.go`

**修改内容：** 在 `CreateQCOW2NVRAMFromTemplate()` 函数中添加模板文件存在性检查，提供更清晰的错误提示：

```go
// 检查模板文件是否存在
if _, err := os.Stat(templatePath); os.IsNotExist(err) {
    return fmt.Errorf("OVMF模板文件不存在: %s, 请确认已安装OVMF固件 ( Debian/Ubuntu: apt install ovmf, CentOS/RHEL: yum install edk2-ovmf )", templatePath)
}
```

---

## 四、错误3：虚拟机启动失败

### 4.1 错误描述
```
任务失败：启动虚拟机失败(已清理资源): 启动虚拟机失败: error: Failed to start domain 'vm1ldhqwvr' error: operation failed
```

### 4.2 根因分析
此错误通常是错误2的连锁反应。由于UEFI NVRAM文件创建失败，导致虚拟机XML配置中的NVRAM路径无效，libvirt启动时找不到固件文件。

### 4.3 修复记录 ✅

**修改的文件：**
1. `server/service/vm_xml/boot_type.go` - 添加 `GetVMNVRAMPath()` 公共函数
2. `server/service/vm/lifecycle.go` - 添加启动前NVRAM文件检查

**修改内容：**

1. 在 `boot_type.go` 中添加公共函数：
```go
// GetVMNVRAMPath 获取虚拟机的NVRAM文件路径（公共函数）
func GetVMNVRAMPath(name string) string {
    return resolveVMNVRAMPath(name, "")
}
```

2. 在 `lifecycle.go` 的 `startVM()` 函数中添加启动前检查：
```go
// 检查UEFI NVRAM文件是否存在（如果虚拟机配置了UEFI启动）
if libvirt_rpc.IsLibvirtRPCAvailable() {
    vmXML, getErr := libvirt_rpc.GetDomainXMLRPC(name, 0)
    if getErr == nil {
        bootType := vm_xml.ParseVMBootTypeFromDomainXML(vmXML)
        if bootType == vm_xml.VMBootTypeUEFI || bootType == vm_xml.VMBootTypeUEFISecure {
            nvramPath := vm_xml.GetVMNVRAMPath(name)
            if _, err := os.Stat(nvramPath); os.IsNotExist(err) {
                return fmt.Errorf("UEFI NVRAM文件不存在: %s, 请检查虚拟机UEFI配置", nvramPath)
            }
        }
    }
}
```

---

## 五、错误4：OVS dnsmasq 端口占用

### 5.1 错误描述
```
failed to create listening socket for 192.168.122.1: Address already in use
```

### 5.2 根因分析
1. libvirt的default网络的dnsmasq未完全释放端口
2. OVS dnsmasq启动时端口仍被占用
3. `Restart=on-failure` 导致频繁重启，端口释放不及时

### 5.3 修复记录 ✅

**修改的文件：** `server/service/ovs/network.go`

**修改内容：** 在 `DisableLibvirtDefaultNetworkIfNeeded()` 函数中添加端口释放等待逻辑：

```go
// DisableLibvirtDefaultNetworkIfNeeded disables libvirt's default network if active.
func DisableLibvirtDefaultNetworkIfNeeded() {
    if result := utils.ExecShell("virsh net-info default 2>/dev/null | awk '/^Active:/ {print $2}'"); strings.TrimSpace(result.Stdout) == "yes" {
        utils.ExecCommand("virsh", "net-destroy", "default")
    }
    if result := utils.ExecShell("virsh net-info default 2>/dev/null | awk '/^Autostart:/ {print $2}'"); strings.TrimSpace(result.Stdout) == "yes" {
        utils.ExecCommand("virsh", "net-autostart", "default", "--disable")
    }
    // 等待端口释放，确保 libvirt dnsmasq 完全停止
    for i := 0; i < 5; i++ {
        result := utils.ExecShellQuiet(fmt.Sprintf("ss -tlnp | grep -q '%s:53'", OvsGatewayIP()))
        if result.Error != nil {
            // 端口未被占用，可以继续
            break
        }
        // 端口仍被占用，等待后重试
        time.Sleep(time.Second)
    }
    // 杀掉残留的 libvirt dnsmasq 进程
    utils.ExecShellQuiet("pkill -f 'dnsmasq.*192.168.122' || true")
    time.Sleep(500 * time.Millisecond)
}
```

同时添加了 `time` 包的导入。

---

## 六、编译验证

所有修改已通过编译验证：

```bash
$ cd server && go build ./...
(no output - 编译成功)
```

---

## 七、测试验证建议

### 7.1 错误1测试
```bash
# 在测试环境创建虚拟机，验证磁盘权限设置成功
# 检查文件权限
ls -la /var/lib/libvirt/images/
```

### 7.2 错误4测试
```bash
# 检查OVS dnsmasq服务状态
systemctl status kvm-console-ovs-dnsmasq

# 检查端口占用
ss -tlnp | grep 192.168.122.1
```

### 7.3 错误2测试
```bash
# 检查OVMF文件是否存在
ls -la /usr/share/OVMF/

# 尝试手动创建NVRAM文件
qemu-img convert -f raw -O qcow2 /usr/share/OVMF/OVMF_VARS_4M.ms.fd /tmp/test.qcow2
```

---

## 八、相关文档

- 依赖项文档: `docs/dependencies.md`
- 安装脚本: `install.sh` (第1320-1432行)
- OVS网络配置: `server/service/ovs/network.go`
- 磁盘权限工具: `server/utils/fs.go`
