#!/usr/bin/env bash
# sub2api 本地任务运行器入口（参照 buildmax 的 ./make）。
# 所有任务都在 tools/mk（Go，只依赖标准库）里实现，macOS / Linux / Windows
# 跑的是同一份代码；make.bat 是 cmd.exe 下的同款入口。运行 `./make help` 查看命令。
#
# tools/mk 是独立的 Go module：仓库根目录没有 go.mod，而 backend/go.mod 要求的
# Go 版本可能比本机新，独立 module 让任务运行器不受它影响。
set -e
cd "$(dirname "${BASH_SOURCE[0]:-$0}")"

# Go 是这个仓库唯一没法替你安装的前置依赖，缺了它连 `./make help` 都跑不起来，
# 所以在这里给出能照着做的提示，而不是一句 `go: command not found`。
if ! command -v go >/dev/null 2>&1; then
	want=$(sed -n 's/^go \([0-9].*\)$/\1/p' tools/mk/go.mod)
	echo "sub2api 的任务运行器需要 Go ${want:-（见 tools/mk/go.mod）}，但 PATH 里没有 go。" >&2
	echo "从 https://go.dev/dl/ 安装；macOS 也可以 brew install go" >&2
	exit 1
fi

exec go run -C tools/mk . "$@"
