package lxc

// ContainerListItem 列表项（解析自 lxc-ls --fancy）。
type ContainerListItem struct {
	Name     string
	Status   string // RUNNING/STOPPED/FROZEN/...
	IPv4     string
	Autostart string // YES/NO
	Running  bool
}

// ContainerDetail 详情（解析自 lxc-info + config）。
type ContainerDetail struct {
	Name      string
	Status    string
	IP        string
	PID       string
	Arch      string
	Backing   string
	CPUShares int
	MemoryMB  int
	Autostart bool
}
