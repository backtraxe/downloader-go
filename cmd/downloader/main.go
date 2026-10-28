package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// 版本信息，发布时通过 -ldflags "-X main.version=..." 注入。
var version, commit, date = "dev", "none", "unknown"

// dependencies 汇总命令依赖；Task 1 尚无内部包，先留空。
type dependencies struct{}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run 使用默认依赖执行命令，返回进程退出码。
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runWith(args, stdin, stdout, stderr, dependencies{})
}

// runWith 解析全局 --data-dir 后按子命令分派。
func runWith(args []string, stdin io.Reader, stdout, stderr io.Writer, deps dependencies) int {
	var dataDir string
	if len(args) > 0 {
		if value, ok := strings.CutPrefix(args[0], "--data-dir="); ok {
			dataDir = value
			args = args[1:]
		} else if args[0] == "--data-dir" && len(args) > 1 {
			dataDir = args[1]
			args = args[2:]
		}
	}
	// 数据目录暂未使用，但先解析以固定优先级：flag > DOWNLOADER_HOME > 当前目录。
	_ = resolveDataDir(dataDir)

	if len(args) == 0 {
		fmt.Fprintln(stderr, "用法: downloader [--data-dir DIR] <命令>")
		return 2
	}

	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "downloader %s (commit %s, built %s)\n", version, commit, date)
		return 0
	default:
		fmt.Fprintf(stderr, "命令 %q 尚未接入\n", args[0])
		return 2
	}
}

// resolveDataDir 按 flag > 环境变量 > 当前目录的顺序确定数据目录。
func resolveDataDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if home := os.Getenv("DOWNLOADER_HOME"); home != "" {
		return home
	}
	return "."
}
