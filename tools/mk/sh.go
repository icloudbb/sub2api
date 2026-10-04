package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// repoRoot 从工作目录向上找仓库根目录（同时有 backend/go.mod 和 tools/mk/go.mod
// 的目录）。根目录本身没有 go.mod，所以不能像 buildmax 那样找 go.mod。
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if exists(filepath.Join(dir, "backend", "go.mod")) && exists(filepath.Join(dir, "tools", "mk", "go.mod")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("找不到仓库根目录：请在 sub2api 仓库内运行")
		}
		dir = parent
	}
}

// runCmd 把子进程接到当前的 stdio 上，构建输出实时可见，psql 这类交互程序
// 也和直接运行一样。
func runCmd(name string, args ...string) error {
	return runWith(nil, name, args...)
}

// runWith 在当前环境变量后追加 KEY=value 再运行命令。
func runWith(env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", commandLine(name, args), err)
	}
	return nil
}

// runStdin 把 input 喂给命令的 stdin，代替 shell 管道。
func runStdin(input, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", commandLine(name, args), err)
	}
	return nil
}

// capture 返回命令去掉首尾空白的 stdout，丢弃 stderr：调用方要么有兜底值，
// 要么自己包装错误。
func capture(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

// captureErr 和 capture 一样，但失败时把 stderr 带进错误里。通过 `go run`
// 跑的命令编译失败只会写 stderr，丢掉它就只剩一个退出码。
func captureErr(name string, args ...string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", commandLine(name, args), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// succeeds 只关心命令是否以 0 退出。
func succeeds(name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

func commandLine(name string, args []string) string {
	if len(args) == 0 {
		return name
	}
	return name + " " + strings.Join(args, " ")
}

func have(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func requireCommands(names ...string) error {
	var missing []string
	for _, name := range names {
		if !have(name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少命令：%s", strings.Join(missing, ", "))
	}
	return nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func logf(tag, format string, args ...any) {
	fmt.Printf("[%s] %s\n", tag, fmt.Sprintf(format, args...))
}

// resolveCommitSHA 返回当前工作区的短 commit，有未提交改动时加 -dirty。
func resolveCommitSHA() string {
	if !have("git") {
		return "dev"
	}
	sha, err := capture("git", "rev-parse", "--short=9", "HEAD")
	if err != nil || sha == "" {
		return "dev"
	}
	if status, err := capture("git", "status", "--porcelain"); err == nil && status != "" {
		sha += "-dirty"
	}
	return sha
}
