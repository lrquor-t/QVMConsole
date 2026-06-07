# 用户存储池功能

## 功能概述

为每个普通用户提供独立的存储空间，支持 ISO 镜像管理和文件共享两种用途，共用一个整体存储配额。

### 存储池结构

| 类别 | 存储路径 | 用途 |
|------|---------|------|
| ISO 镜像 | `/var/lib/kvm-user-storage/<用户名>/iso` | 创建虚拟机时选择的 ISO 来源 |
| 文件共享 | `/var/lib/kvm-user-storage/<用户名>/share` | 通过 9p VirtFS 挂载到虚拟机的共享目录 |

### 配额说明

- 使用 **ext4 Project Quota** 进行 **按目录的内核级配额限制**
- 每个用户的 ISO + 文件共享目录绑定为同一个 project，配额合并计算
- 专用回环文件系统（`/var/lib/kvm-user-storage.img`）隔离配额统计，不影响其他系统文件
- 配额设为 0 表示不限制
- 超出配额后内核阻止写入，前端显示 **只读模式**

### 部署配置

安装脚本会自动完成以下配置：

1. 安装 `quota` 工具包
2. 创建稀疏镜像文件（不实际占用磁盘，按需增长）
3. 格式化为 ext4（启用 project quota）
4. 挂载到 `/var/lib/kvm-user-storage`
5. 添加到 `/etc/fstab` 开机自动挂载

---

## 管理员操作

### 设置存储配额

在 **用户管理** → 点击 **配置** 按钮 → 设置「存储 (GB)」字段。

创建用户时也可以在表单中直接设置存储配额：
- 管理员：默认 0 GB（不限制）
- 普通用户：默认 10 GB

管理员用户同样支持「我的存储」功能，可在侧边栏进入管理 ISO 镜像和文件共享。

---

## 普通用户操作

### 1. 初始化存储池

首次使用时在 **侧边栏** → **我的存储** → 点击 **开通存储池**。

### 2. ISO 管理

进入 **我的存储** → **ISO 镜像** 标签页：

- **上传 ISO**：点击「上传 ISO」按钮选择 `.iso` 文件
- **删除 ISO**：点击文件对应的「删除」按钮
- 系统自动推断 ISO 的操作系统类型（Linux/Windows）

### 3. 文件共享管理

进入 **我的存储** → **文件共享** 标签页：

- **上传文件**：点击「上传文件」按钮选择任意文件
- **下载文件**：点击「下载」按钮
- **删除文件**：点击「删除」按钮

### 4. 挂载存储池到虚拟机

在 **文件共享** 标签页 → 点击 **挂载到虚拟机**：

1. 选择目标虚拟机
2. 选择存储类别（ISO 或文件共享）
3. 选择访问模式（只读/读写）
4. 确认挂载

> **注意**：挂载使用 9p VirtFS 协议，仅支持 Linux 虚拟机。Windows 虚拟机不支持。

挂载后在虚拟机内执行：

```bash
# 创建挂载点
mkdir -p /mnt/user_share

# 挂载共享目录
mount -t 9p -o trans=virtio,version=9p2000.L user_<用户名>_share /mnt/user_share

# 写入 fstab 开机自动挂载
echo 'user_<用户名>_share /mnt/user_share 9p trans=virtio,version=9p2000.L,nofail 0 0' >> /etc/fstab
```

### 5. 创建虚拟机

普通用户在 **虚拟机列表** 页面可以创建虚拟机：

- ISO 下拉框自动显示用户自己存储池中的 ISO 文件，并支持一次挂载多个 ISO；首个 ISO 作为主安装盘，其余 ISO 会作为额外挂载光驱
- 创建后虚拟机自动加入用户的虚拟机列表

---

## API 接口

| 方法 | 路由 | 功能 |
|------|------|------|
| `GET` | `/api/self/storage/info` | 获取存储池信息 |
| `POST` | `/api/self/storage/init` | 初始化存储池 |
| `GET` | `/api/self/storage/files/:category` | 列出文件 |
| `POST` | `/api/self/storage/upload/:category` | 上传文件 |
| `DELETE` | `/api/self/storage/file/:category/:filename` | 删除文件 |
| `GET` | `/api/self/storage/download/:category/:filename` | 下载文件 |
| `GET` | `/api/self/storage/isos` | 获取ISO列表 |
| `POST` | `/api/self/storage/mount` | 挂载到VM |
| `DELETE` | `/api/self/storage/mount/:vmName/:tag` | 卸载挂载 |
| `POST` | `/api/self/vm/create` | 用户自助创建VM |
