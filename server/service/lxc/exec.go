package lxc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"kvm_console/logger"
	"kvm_console/model"
)

const (
	execDefaultTimeoutSec = 30
	execMaxTimeoutSec     = 300
	execStreamCapBytes    = 512 * 1024
	execMaxCommandBytes   = 4 * 1024
)

// boundedWriter 是一个 io.Writer：写入累计到 cap 字节后丢弃多余内容并标记 truncated。
// 始终返回 len(p) 已写，避免管道因消费方不读而阻塞。
type boundedWriter struct {
	buf       []byte
	cap       int
	truncated bool
}

func newBoundedWriter(cap int) *boundedWriter {
	return &boundedWriter{cap: cap}
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if len(w.buf) >= w.cap {
		w.truncated = true
		return len(p), nil
	}
	remaining := w.cap - len(w.buf)
	if len(p) <= remaining {
		w.buf = append(w.buf, p...)
		return len(p), nil
	}
	w.buf = append(w.buf, p[:remaining]...)
	w.truncated = true
	return len(p), nil
}

// clampTimeout 规范化超时秒数：sec<=0 取 def；sec>max 截到 max；否则取 sec。
func clampTimeout(sec, def, max int) int {
	if sec <= 0 {
		return def
	}
	if sec > max {
		return max
	}
	return sec
}

// ExecResult 一次性命令执行的捕获结果。
type ExecResult struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timed_out"`
}

// ExecContainer 在运行中容器内以 root 执行单条命令（lxc-attach -- sh -c），
// 分流捕获 stdout/stderr，带超时（杀整个进程组）与输出截断。
// 容器须 RUNNING；FROZEN/STOPPED 拒绝。
func ExecContainer(name, command string, timeoutSec int) (ExecResult, error) {
	if command == "" {
		return ExecResult{}, errors.New("命令不能为空")
	}
	if len(command) > execMaxCommandBytes {
		return ExecResult{}, fmt.Errorf("命令过长（>%d 字节）", execMaxCommandBytes)
	}
	var row model.LXCCache
	if err := model.DB.Where("name = ?", name).First(&row).Error; err != nil {
		return ExecResult{}, errors.New("容器不存在")
	}
	switch strings.ToUpper(strings.TrimSpace(row.Status)) {
	case "FROZEN":
		return ExecResult{}, errors.New("容器已冻结，请先恢复后再执行命令")
	case "RUNNING":
		// ok
	default:
		return ExecResult{}, errors.New("容器未运行")
	}

	sec := clampTimeout(timeoutSec, execDefaultTimeoutSec, execMaxTimeoutSec)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(sec)*time.Second)
	defer cancel()

	cmd := exec.Command("lxc-attach", "-n", name, "--", "sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // 独立进程组，超时可整组杀
	outW := newBoundedWriter(execStreamCapBytes)
	errW := newBoundedWriter(execStreamCapBytes)
	cmd.Stdout = outW
	cmd.Stderr = errW

	res := ExecResult{}
	if err := cmd.Start(); err != nil {
		return res, fmt.Errorf("启动命令失败: %w", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	timedOut := false
	select {
	case <-ctx.Done():
		timedOut = true
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // 杀进程组
		}
		<-waitCh
	case <-waitCh:
	}

	if outW.truncated {
		res.Truncated = true
	}
	if errW.truncated {
		res.Truncated = true
	}
	res.Stdout = string(outW.buf)
	res.Stderr = string(errW.buf)
	if outW.truncated {
		res.Stdout += "\n[输出已截断]"
	}
	if errW.truncated {
		res.Stderr += "\n[输出已截断]"
	}
	res.TimedOut = timedOut
	res.ExitCode = -1
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if timedOut {
		logger.App.Warn("LXC exec 超时", "name", name, "timeout_sec", sec)
	}
	return res, nil
}
