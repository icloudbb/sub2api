package main

import (
	"errors"
	"fmt"
	"strings"
)

// help 分两层：`help` 列出全部命令，`help <command>` 是单个命令的完整说明。
// 单个命令的说明同时也是参数出错时打印的 usage 的唯一来源，避免两处各写一份
// 然后对不上。

type helpRow struct {
	name        string
	description string
}

type helpSection struct {
	name string
	rows []helpRow
}

// helpTopic 是一个命令的说明页：参数形态、作用、每个参数的含义和示例。
type helpTopic struct {
	name     string
	usage    string   // 参数形态，不带 ./make 前缀
	summary  string   // 一行概述，打印在 usage 下面
	details  []string // 段落
	args     []helpRow
	examples []string // 参数列表，打印时加上 ./make 前缀
	see      string   // 更详细的文档
}

func allHelpSections() []helpSection {
	return []helpSection{
		{"本地部署", []helpRow{
			{"kind <action>", "管理本地 kind 集群上的 sub2api 部署；`help kind` 查看全部动作"},
		}},
		{"其它", []helpRow{
			{"help [command]", "列出命令，或查看单个命令的说明"},
		}},
	}
}

func helpTopics() []helpTopic {
	return []helpTopic{
		{
			name:    "kind",
			usage:   "kind <up|reload|env|status|info|logs [service]|forward|psql|redis|down>",
			summary: "在本机 kind 集群里部署 sub2api + PostgreSQL + Redis，用于本地部署测试",
			details: []string{
				"需要 Docker 和 kubectl。kind 本身通过 `go run " + kindPkg + "` 固定版本运行，\n" +
					"不依赖 PATH 里的 kind，保证创建、查看、删除集群用的是同一个版本。",
				"镜像用仓库根目录的 Dockerfile 构建为 " + kindImage + "，通过 kind load 加载进节点；\n" +
					"tag 固定，所以每次 reload 都会 rollout restart，并把源码 commit 记在 Deployment 上，\n" +
					"`kind status` 能看出集群里跑的是不是当前工作区的代码。",
				"首次部署生成 " + kindSecretName + "（数据库/Redis 密码、JWT、TOTP 密钥、管理员账号），之后不再覆盖：\n" +
					"PostgreSQL 数据在 PVC 上，reload 后数据、登录态、2FA 都保留；`kind down` 才会清空。",
				"环境变量：\n" +
					"  SUB2API_KIND_CLUSTER    集群名，默认 " + defaultKindCluster + "\n" +
					"  SUB2API_KIND_PORT       宿主机端口，默认 " + defaultKindPort + "（避开 buildmaxdev 的 8080），建集群时生效\n" +
					"  SUB2API_IMAGE_PLATFORM  传给 docker build --platform\n" +
					"  SUB2API_ADMIN_EMAIL     首次部署时的管理员邮箱，默认 admin@sub2api.local\n" +
					"  SUB2API_ADMIN_PASSWORD  首次部署时的管理员密码，默认随机生成\n" +
					"  SUB2API_KIND_PG_PORT / SUB2API_KIND_REDIS_PORT  forward 的本地端口，默认 15432 / 16379",
			},
			args: []helpRow{
				{"up", "创建集群（已存在则复用），构建镜像并部署 PostgreSQL、Redis、sub2api"},
				{"reload", "重新构建镜像并滚动重启 sub2api（改完代码用这个）"},
				{"env", "把 " + kindExtraEnvPath + " 同步为额外环境变量并重启 sub2api"},
				{"status", "集群、健康检查、部署的代码版本、Pod 状态"},
				{"info", "访问地址和管理员账号"},
				{"logs [service]", "跟踪日志：" + strings.Join(kindServices, "、") + "，默认 sub2api"},
				{"forward", "把 PostgreSQL / Redis 转发到 127.0.0.1，方便本地工具直连"},
				{"psql", "进入集群内 PostgreSQL 的 psql"},
				{"redis", "进入集群内 Redis 的 redis-cli"},
				{"down", "删除集群，数据一并清空"},
			},
			examples: []string{"kind up", "kind reload", "kind info", "kind logs postgres", "kind down"},
			see:      "deploy/kind/README.md",
		},
		{
			name:    "help",
			usage:   "help [command]",
			summary: "列出全部命令，或查看单个命令的说明",
		},
	}
}

func lookupHelpTopic(name string) (helpTopic, bool) {
	for _, topic := range helpTopics() {
		if topic.name == name {
			return topic, true
		}
	}
	return helpTopic{}, false
}

func helpCommandNames() []string {
	var names []string
	for _, topic := range helpTopics() {
		names = append(names, topic.name)
	}
	return names
}

func formatHelpRow(row helpRow) string {
	return fmt.Sprintf("  %-18s %s", row.name, row.description)
}

func printHelpRows(rows []helpRow) {
	for _, row := range rows {
		fmt.Println(formatHelpRow(row))
	}
}

// usageErrorf 把具体错误和该命令的 usage 拼成一个错误返回。
func usageErrorf(name, format string, args ...any) error {
	var b strings.Builder
	if format != "" {
		fmt.Fprintf(&b, format+"\n", args...)
	}
	topic, ok := lookupHelpTopic(name)
	if !ok {
		return errors.New(strings.TrimSuffix(b.String(), "\n"))
	}
	fmt.Fprintf(&b, "usage: %s %s", mk(), topic.usage)
	for _, row := range topic.args {
		b.WriteString("\n")
		b.WriteString(formatHelpRow(row))
	}
	fmt.Fprintf(&b, "\n运行 `%s help %s` 查看完整说明", mk(), name)
	return errors.New(b.String())
}

func cmdHelp(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	if len(args) > 1 {
		return usageErrorf("help", "help 最多接受一个命令")
	}
	topic, ok := lookupHelpTopic(args[0])
	if !ok {
		return unknownCommand(args[0])
	}
	printHelpTopic(topic)
	return nil
}

func printHelpTopic(topic helpTopic) {
	fmt.Printf("usage: %s %s\n\n%s\n", mk(), topic.usage, topic.summary)
	for _, paragraph := range topic.details {
		fmt.Printf("\n%s\n", paragraph)
	}
	if len(topic.args) > 0 {
		fmt.Println()
		printHelpRows(topic.args)
	}
	if len(topic.examples) > 0 {
		fmt.Println("\n示例：")
		for _, example := range topic.examples {
			fmt.Printf("  %s %s\n", mk(), example)
		}
	}
	if topic.see != "" {
		fmt.Printf("\n详见 %s\n", topic.see)
	}
}

func usage() {
	fmt.Printf("sub2api 任务运行器\n\nusage: %s <command> [args]\n", mk())
	for _, section := range allHelpSections() {
		fmt.Printf("\n%s:\n", section.name)
		printHelpRows(section.rows)
	}
	fmt.Printf("\n第一次本地部署：%s kind up\n", mk())
}
