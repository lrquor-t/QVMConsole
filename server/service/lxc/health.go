package lxc

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"kvm_console/model"
)

const (
	healthHTTPTimeout     = 3 * time.Second
	healthTCPTimeout      = 2 * time.Second
	healthScriptTimeout   = 5 * time.Second
	healthStatusHealthy   = "healthy"
	healthStatusUnhealthy = "unhealthy"
	healthStatusUnknown   = "unknown"
	healthAggHealthy      = "healthy"
	healthAggDegraded     = "degraded"
	healthAggUnhealthy    = "unhealthy"
	healthAggUnknown      = "unknown"
)

// ProbeHealthCheck 执行单条检查。容器未运行/无 IP 时返回 unknown。
func ProbeHealthCheck(h *model.LXCHealthCheck, containerIP string) (status string, latencyMs int, errMsg string) {
	if containerIP == "" {
		return healthStatusUnknown, 0, "容器无 IP"
	}
	start := time.Now()
	var err error
	switch h.Type {
	case "tcp":
		err = probeTCP(h.Target)
	case "http":
		err = probeHTTP(h.Target, h.ExpectedCode)
	case "script":
		err = probeScript(h.Name, h.Target) // lxc-attach
	default:
		return healthStatusUnknown, 0, "未知检查类型 " + h.Type
	}
	latencyMs = int(time.Since(start).Milliseconds())
	if err != nil {
		return healthStatusUnhealthy, latencyMs, err.Error()
	}
	return healthStatusHealthy, latencyMs, ""
}

func probeTCP(target string) error {
	conn, err := net.DialTimeout("tcp", target, healthTCPTimeout)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func probeHTTP(target string, expectedCode int) error {
	client := &http.Client{Timeout: healthHTTPTimeout}
	if !strings.HasPrefix(strings.ToLower(target), "http") {
		target = "http://" + target
	}
	resp, err := client.Get(target)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if expectedCode > 0 && resp.StatusCode != expectedCode {
		return fmt.Errorf("状态码 %d != 期望 %d", resp.StatusCode, expectedCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("状态码 %d", resp.StatusCode)
	}
	return nil
}

// probeScript 进容器执行命令，exit 0 即健康。
// ExecContainer 返回 (ExecResult, error)，已封装 lxc-attach + 超时 + 截断（service/lxc/exec.go:73）。
func probeScript(containerName, command string) error {
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("命令为空")
	}
	res, err := ExecContainer(containerName, command, int(healthScriptTimeout.Seconds()))
	if err != nil {
		return err
	}
	if res.TimedOut {
		return fmt.Errorf("脚本执行超时")
	}
	if res.ExitCode != 0 {
		// 优先用 stderr 作为错误线索，缺失则退回 stdout 截断后的首行
		hint := strings.TrimSpace(res.Stderr)
		if hint == "" {
			hint = strings.TrimSpace(res.Stdout)
		}
		if hint != "" {
			return fmt.Errorf("exit %d: %s", res.ExitCode, truncateOneLine(hint))
		}
		return fmt.Errorf("exit %d", res.ExitCode)
	}
	return nil
}

// truncateOneLine 取首行并截断到 200 字符，避免 last_error 列被海量输出撑爆。
func truncateOneLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// healthProbeResult 单条检查的聚合输入。
type healthProbeResult struct {
	Critical bool
	Status   string
}

// AggregateHealth 按四色规则聚合：全过=healthy；仅非critical失败=degraded；
// critical失败或全部失败=unhealthy；全 unknown（容器停机/无 IP）=unknown。
// 注意：无启用规则的 unknown 由调用方（RunHealthCheckForContainer）在调用前判定。
func AggregateHealth(results []healthProbeResult) string {
	if len(results) == 0 {
		return healthAggUnhealthy
	}
	anyHealthy, anyFailed, anyCriticalFailed := false, false, false
	for _, r := range results {
		switch r.Status {
		case healthStatusHealthy:
			anyHealthy = true
		case healthStatusUnhealthy:
			anyFailed = true
			if r.Critical {
				anyCriticalFailed = true
			}
		// healthStatusUnknown: counts as neither healthy nor failed
		}
	}
	switch {
	case !anyHealthy && !anyFailed:
		return healthAggUnknown // 全 unknown（容器停机/无 IP）
	case anyCriticalFailed:
		return healthAggUnhealthy
	case !anyHealthy:
		return healthAggUnhealthy // 全部失败（无 healthy 项）
	case anyFailed:
		return healthAggDegraded // 仅非 critical 失败，但有 healthy 项
	default:
		return healthAggHealthy
	}
}

// RunHealthCheckForContainer 探测指定容器全部启用规则，写各项结果 + 聚合写 LXCCache。返回聚合状态。
// 无启用规则 → unknown；有规则按四色聚合。
func RunHealthCheckForContainer(name string) (string, error) {
	checks, err := model.ListLXCHealthChecksByContainer(name)
	if err != nil {
		return healthStatusUnknown, err
	}
	ip := ResolveContainerVPCIP(name)
	now := time.Now()

	var enabled []model.LXCHealthCheck
	for _, c := range checks {
		if c.Enabled {
			enabled = append(enabled, c)
		}
	}

	if len(enabled) == 0 {
		_ = model.UpdateLXCCacheHealthStatus(name, healthStatusUnknown, now) // 见 model/lxc_cache.go
		return healthStatusUnknown, nil
	}

	var results []healthProbeResult
	for i := range enabled {
		c := enabled[i]
		status, latency, msg := ProbeHealthCheck(&c, ip)
		results = append(results, healthProbeResult{Critical: c.Critical, Status: status})
		_ = model.UpdateLXCHealthCheckResult(c.ID, status, latency, msg, now) // 见 model/lxc_health_check.go
	}

	agg := AggregateHealth(results)
	_ = model.UpdateLXCCacheHealthStatus(name, agg, now)
	return agg, nil
}
