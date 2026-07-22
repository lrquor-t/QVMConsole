package lxc

import (
	"sync"
	"time"

	"kvm_console/config"
	"kvm_console/logger"
	"kvm_console/model"
	"kvm_console/utils"
)

// HookIsMaintenanceModeEnabled 维护模式是否开启（由 service 根包 init 注入，避免 service/lxc 反向 import service）。
// 默认 fallback：nil 时视为未开启，调度器正常执行。
var HookIsMaintenanceModeEnabled func() bool

var healthSchedulerOnce sync.Once

// StartLXCHealthCheckScheduler 启动 LXC 健康检查后台调度器（周期轮询所有 present 容器）。
// 结构参照 service/network/probe/scheduler.go 的 StartPortForwardHTTPProbeScheduler：
// sync.Once 守护、goroutine + RecoverAndLog、读 config.GlobalConfig 取间隔/开关，
// 维护模式开启或特性禁用时跳过本轮。
func StartLXCHealthCheckScheduler() {
	healthSchedulerOnce.Do(func() {
		go func() {
			defer utils.RecoverAndLog("lxc-health-check")
			for {
				intervalSeconds := 30
				if config.GlobalConfig != nil && config.GlobalConfig.LXCHealthCheckIntervalSeconds > 0 {
					intervalSeconds = config.GlobalConfig.LXCHealthCheckIntervalSeconds
				}
				enabled := config.GlobalConfig == nil || config.GlobalConfig.LXCHealthCheckEnabled
				maintenance := false
				if HookIsMaintenanceModeEnabled != nil {
					maintenance = HookIsMaintenanceModeEnabled()
				}
				if enabled && !maintenance {
					runHealthCheckCycle()
				}
				time.Sleep(time.Duration(intervalSeconds) * time.Second)
			}
		}()
	})
}

// runHealthCheckCycle 一轮探测：列举所有 present 容器，按并发上限跑各自的启用规则。
// 并发由 LXCHealthCheckConcurrency（默认 10）的 semaphore 通道限制，
// 避免 50 容器 × N 规则同时 fork 大量 lxc-attach 拖垮宿主。
func runHealthCheckCycle() {
	containers, err := model.ListAllLXCCaches()
	if err != nil {
		logger.App.Warn("健康检查列举容器失败", "error", err)
		return
	}
	if len(containers) == 0 {
		return
	}
	concurrency := 10
	if config.GlobalConfig != nil && config.GlobalConfig.LXCHealthCheckConcurrency > 0 {
		concurrency = config.GlobalConfig.LXCHealthCheckConcurrency
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range containers {
		c := containers[i]
		if !c.Present {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer utils.RecoverAndLog("lxc-health-check-worker")
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := RunHealthCheckForContainer(c.Name); err != nil {
				logger.App.Warn("健康检查失败", "name", c.Name, "error", err)
			}
		}()
	}
	wg.Wait()
}
