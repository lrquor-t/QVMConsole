package utils

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CmdResult 命令执行结果
type CmdResult struct {
	Stdout   string // 标准输出
	Stderr   string // 标准错误
	ExitCode int    // 退出码
	Error    error  // 错误信息
}

// ExecCommand 执行系统命令
func ExecCommand(name string, args ...string) *CmdResult {
	return ExecCommandWithTimeout(name, 30*time.Second, args...)
}

// ExecCommandWithTimeout 执行系统命令（带超时）
func ExecCommandWithTimeout(name string, timeout time.Duration, args ...string) *CmdResult {
	return ExecCommandContextWithTimeout(context.Background(), name, timeout, args...)
}

// ExecCommandContextWithTimeout 执行系统命令（支持取消和超时）
func ExecCommandContextWithTimeout(ctx context.Context, name string, timeout time.Duration, args ...string) *CmdResult {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.Command(name, args...)
	// 强制使用 C 语言环境，确保 virsh 等命令输出英文便于解析
	cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	prepareProcessGroup(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// 记录命令执行日志
	log.Printf("[CMD] %s %s", name, strings.Join(args, " "))

	// 启动命令
	if err := cmd.Start(); err != nil {
		return &CmdResult{
			Stderr:   err.Error(),
			ExitCode: -1,
			Error:    fmt.Errorf("启动命令失败: %w", err),
		}
	}

	// 超时控制
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		result := &CmdResult{
			Stdout: strings.TrimSpace(stdout.String()),
			Stderr: strings.TrimSpace(stderr.String()),
		}
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				result.ExitCode = exitErr.ExitCode()
			} else {
				result.ExitCode = -1
			}
			result.Error = fmt.Errorf("命令执行失败: %w, stderr: %s", err, result.Stderr)
		}
		return result

	case <-time.After(timeout):
		killProcessTree(cmd)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		return &CmdResult{
			Stderr:   "命令执行超时",
			ExitCode: -1,
			Error:    fmt.Errorf("命令执行超时: %s %s", name, strings.Join(args, " ")),
		}

	case <-ctx.Done():
		killProcessTree(cmd)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		return &CmdResult{
			Stderr:   "命令已取消",
			ExitCode: -1,
			Error:    fmt.Errorf("命令已取消: %s %s: %w", name, strings.Join(args, " "), ctx.Err()),
		}
	}
}

// ExecCommandLongRunning 执行长时间运行的命令（超时 10 分钟）
func ExecCommandLongRunning(name string, args ...string) *CmdResult {
	return ExecCommandWithTimeout(name, 10*time.Minute, args...)
}

// ExecShell 执行 Shell 命令（通过 bash -c）
func ExecShell(command string) *CmdResult {
	return ExecCommand("bash", "-c", command)
}

// ExecShellWithTimeout 执行 Shell 命令（带超时）
func ExecShellWithTimeout(command string, timeout time.Duration) *CmdResult {
	return ExecCommandWithTimeout("bash", timeout, "-c", command)
}

// ExecShellContextWithTimeout 执行 Shell 命令（支持取消和超时）
func ExecShellContextWithTimeout(ctx context.Context, command string, timeout time.Duration) *CmdResult {
	return ExecCommandContextWithTimeout(ctx, "bash", timeout, "-c", command)
}

// ShellSingleQuote 对 shell 参数做单引号转义，防止命令注入。
// 将单引号替换为 '"'"'（结束引号、转义单引号、开始引号），
// 使参数在 shell 单引号上下文中安全使用。
func ShellSingleQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
