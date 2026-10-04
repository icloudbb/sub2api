package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderKindConfigContentRewritesOnlyNodePortMapping(t *testing.T) {
	template := strings.Join([]string{
		"nodes:",
		"  - role: control-plane",
		"    extraPortMappings:",
		"      - containerPort: 30080",
		"        hostPort: 18080",
		"      - containerPort: 30443",
		"        hostPort: 18443",
	}, "\n")

	got := renderKindConfigContent(template, "28080")

	if !strings.Contains(got, "      - containerPort: 30080\n        hostPort: 28080\n") {
		t.Fatalf("NodePort 映射的 hostPort 没有被替换：\n%s", got)
	}
	if !strings.Contains(got, "        hostPort: 18443") {
		t.Fatalf("其它映射不应被改动：\n%s", got)
	}
}

// 仓库里的 kind-config.yaml 必须含有 tools/mk 能改写的那条映射；有人改了
// NodePort 或格式而忘了同步，SUB2API_KIND_PORT 会悄悄失效。
func TestRepoKindConfigHasRewritableMapping(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(kindConfigPath)))
	if err != nil {
		t.Fatal(err)
	}
	got := renderKindConfigContent(string(data), "65000")
	if !strings.Contains(got, "hostPort: 65000") {
		t.Fatalf("%s 中没有 containerPort %s 的映射可供改写", kindConfigPath, kindNodePort)
	}
}

// 参数校验必须在调用任何外部命令之前完成，所以这些用例不需要 docker / kubectl。
func TestCmdKindRejectsBadArguments(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"无动作", nil, "kind 需要一个动作"},
		{"未知动作", []string{"bogus"}, "未知的 kind 动作：bogus"},
		{"多余参数", []string{"up", "now"}, "up 不接受参数"},
		{"未知服务", []string{"logs", "mysql"}, `未知服务 "mysql"`},
		{"logs 参数过多", []string{"logs", "sub2api", "redis"}, "logs 最多接受一个服务名"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := cmdKind(tc.args)
			if err == nil {
				t.Fatal("期望返回错误")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息 %q 不包含 %q", err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), "usage: ") {
				t.Fatalf("错误信息应附带 usage：%q", err.Error())
			}
		})
	}
}

func TestNearestCommand(t *testing.T) {
	if got, ok := nearestCommand("knid"); !ok || got != "kind" {
		t.Fatalf("nearestCommand(knid) = %q, %v", got, ok)
	}
	if _, ok := nearestCommand("deploy"); ok {
		t.Fatal("差太远的词不应给出候选")
	}
}
