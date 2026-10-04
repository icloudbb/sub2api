// Command mk 是 sub2api 的本地任务运行器，参照 buildmax 的 tools/mk。
//
// 它是 ./make 和 make.bat 背后的实现，两个入口脚本只负责转发到这里。任务写在
// Go 里而不是 shell 里，macOS、Linux、Windows 跑的是同一份代码，不会出现
// bash 和 batch 两份脚本各写一遍、慢慢走样的问题。
//
// mk 只依赖标准库，并且是独立于 backend 的 module：backend 编不过、或者本机
// Go 版本低于 backend/go.mod 的要求时，./make 照样能用。
package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func dispatch(args []string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	// 入口脚本用 `go run -C tools/mk` 启动，工作目录在 tools/mk；下面所有任务
	// 都假定工作目录是仓库根目录。
	if err := os.Chdir(root); err != nil {
		return err
	}

	if len(args) == 0 {
		usage()
		return nil
	}
	rest := args[1:]
	// `<command> --help` 是大多数人先试的写法，等同于 `help <command>`。
	if len(rest) > 0 && isHelpFlag(rest[0]) {
		return cmdHelp(args[:1])
	}
	switch args[0] {
	case "kind":
		return cmdKind(rest)
	case "help", "-h", "--help":
		return cmdHelp(rest)
	default:
		return unknownCommand(args[0])
	}
}

func isHelpFlag(arg string) bool {
	switch arg {
	case "help", "-h", "--help":
		return true
	}
	return false
}

// unknownCommand 只用一行说清楚哪里错了；拼错的命令给出最接近的候选。
func unknownCommand(name string) error {
	m := mk()
	if closest, found := nearestCommand(name); found {
		return fmt.Errorf("未知命令：%s；是不是想运行 `%s %s`？运行 `%s help` 查看命令列表", name, m, closest, m)
	}
	return fmt.Errorf("未知命令：%s；运行 `%s help` 查看命令列表", name, m)
}

// nearestCommand 从 help 表里找最接近的命令名：不在 help 里的命令本来就没人
// 找得到，所以不需要再单独维护一份候选列表。
func nearestCommand(name string) (string, bool) {
	budget := 1
	if len(name) >= 4 {
		// 两步，这样 `knid` 这类字母对调也能找到 `kind`
		budget = 2
	}
	best, bestDistance := "", budget+1
	for _, candidate := range helpCommandNames() {
		if distance := editDistance(name, candidate); distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	return best, best != ""
}

// editDistance 是按字节计算的 Levenshtein 距离，命令名都是 ASCII。
func editDistance(a, b string) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}

// mk 是 help 和报错里展示的入口名，Windows 用户不会被告知去跑一个 shell 脚本。
func mk() string {
	if runtime.GOOS == "windows" {
		return "make.bat"
	}
	return "./make"
}
