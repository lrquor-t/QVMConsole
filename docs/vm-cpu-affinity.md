# 虚拟机 CPU 亲和性

## 功能说明

CPU 亲和性（CPU Affinity / CPU Pinning）用于将虚拟机的 vCPU 绑定到宿主机上指定的物理 CPU 核心，减少 vCPU 在不同物理核心间的迁移开销，适合对延迟敏感或需要稳定性能的工作负载。

该功能仅管理员可用，在创建和编辑虚拟机的高级设置中均可配置。

## 输入格式

- 支持用**逗号**或**空格**分隔多个核心编号，如 `0,2,4` 或 `0 2 4`
- 支持**范围格式**，如 `0-3` 表示核心 0、1、2、3
- 支持**混合格式**，如 `0-3,6,8-10`
- 留空表示不设置 CPU 亲和性（默认行为）

## 生效逻辑

### 持久化配置

CPU 亲和性通过 libvirt domain XML 的 `<cputune>` 块中的 `<vcpupin>` 元素持久化：

```xml
<cputune>
  <vcpupin vcpu='0' cpuset='0'/>
  <vcpupin vcpu='1' cpuset='2'/>
  <vcpupin vcpu='2' cpuset='4'/>
</cputune>
```

vCPU 按轮询（round-robin）方式分配到用户指定的物理核心上。例如 vCPU=4，指定核心为 `0,2`，则：
- vCPU 0 → 核心 0
- vCPU 1 → 核心 2
- vCPU 2 → 核心 0
- vCPU 3 → 核心 2

### 新建虚拟机

- 创建 / 克隆 / 导入时，如果指定了 CPU 亲和性，会在生成 domain XML 时直接写入相应的 `<vcpupin>` 配置

### 编辑已有虚拟机

- 持久化配置通过 `virsh define` 更新
- 运行中的虚拟机会额外通过 `virsh vcpupin --live` 同步运行态亲和性
- 同步运行态时使用在线域的 vCPU 数量（而非持久化配置），避免因用户同时修改 vCPU 数量导致 vCPU 索引越界
- 清除 CPU 亲和性（留空提交）时，会通过 `virsh vcpupin --live` 将所有 vCPU 重新绑定到全部可用物理核心

## 接口字段

以下管理员接口新增可选字段 `cpu_affinity`（字符串类型，空字符串表示不设置）：

- `PUT /api/vm/:name` — 编辑虚拟机
- `POST /api/vm/create` — 创建虚拟机
- `POST /api/vm/clone` — 克隆虚拟机
- `POST /api/vm/linked-clone` — 链式克隆
- `POST /api/vm/import` — 导入虚拟机

字段规则：

- 空字符串或不传：不设置 / 清除 CPU 亲和性
- 非空字符串：按格式解析后设置，如 `"0,2,4"` 或 `"0-3"`

## 输入验证

- 前端会校验输入仅包含数字、逗号、空格和连字符
- 后端会校验核心编号不超过系统可用 CPU 核心范围（通过 `nproc --all` 获取）
- 核心编号不能为负数
- 范围格式起始值不能大于结束值
- 单个范围不能超过 256 个核心

## 错误处理

| 场景 | 错误提示 |
|------|----------|
| 输入包含非数字字符 | "CPU 亲和性格式不正确，请使用数字、逗号、空格或连字符" |
| 核心编号超出系统范围 | "CPU 核心编号 N 超出系统可用范围 (0-M)" |
| 范围格式错误 | "核心范围起始值 N 不能大于结束值 M" |
| 系统不支持（nproc 失败） | "无法获取系统 CPU 核心数" |
| 持久化与在线 vCPU 数量不一致 | 自动使用在线域 vCPU 数量进行同步，避免"vcpu X is out of range"错误 |

## 界面位置

- **新建虚拟机向导**：高级选项步骤 → CPU 亲和性（仅管理员可见）
- **编辑虚拟机弹窗**：高级设置标签页 → CPU 亲和性（仅管理员可见）
- **虚拟机详情页**：配置信息卡片 → CPU 亲和性（展示当前配置，未设置显示"未设置"）

## 实现位置

- 前端表单：`web/src/components/VmForm.vue`
- 详情页面：`web/src/views/vm/detail.vue`
- 后端编辑：`server/handler/vm.go`
- CPU 亲和性核心逻辑：`server/service/vm_cpu_affinity.go`
- VM 信息结构：`server/service/libvirt.go`

## 与 CPU 限制的关系

CPU 亲和性和 CPU 限制是两个独立的功能，可以同时使用：

- **CPU 亲和性**：控制 vCPU 运行在哪些物理核心上（空间绑定）
- **CPU 限制**：控制 vCPU 可以使用多少 CPU 时间（时间限速）

两者都写入 domain XML 的 `<cputune>` 块，互不影响。
