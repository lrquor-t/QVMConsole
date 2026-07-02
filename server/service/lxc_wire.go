package service

import "kvm_console/service/lxc"

// LXCSyncContainerCache 同步 LXC 容器缓存。
func LXCSyncContainerCache() error { return lxc.SyncContainerCache() }

// LXCListContainers 列出可见容器。
func LXCListContainers(username string, isAdmin bool) ([]any, error) {
	rows, err := lxc.ListContainers(username, isAdmin)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	return out, nil
}

// LXCGetContainerDetail 取容器详情。
func LXCGetContainerDetail(name string) (lxc.ContainerDetail, error) {
	return lxc.GetContainerDetail(name)
}
