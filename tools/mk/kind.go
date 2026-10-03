package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	defaultKindCluster = "sub2apidev"
	// 不用 8080：本机的 buildmaxdev 集群已经占了它。
	defaultKindPort = "18080"

	// kind 是普通的 Go 程序，所以和 buildmax 一样通过 `go run` 固定版本运行，
	// 而不是依赖 PATH 里的 kind：创建、查看、删除集群用的保证是同一个版本。
	// 第一次运行会编译几秒，之后走 Go 的构建缓存。Docker 和 kubectl 没法这样
	// 处理，所以仍然放在 requireCommands 里。
	kindPkg = "sigs.k8s.io/kind@v0.31.0"

	kindConfigPath   = "deploy/kind/kind-config.yaml"
	kindPostgresPath = "deploy/kind/postgres.yaml"
	kindRedisPath    = "deploy/kind/redis.yaml"
	kindSub2APIPath  = "deploy/kind/sub2api.yaml"
	kindExtraEnvPath = "deploy/kind/sub2api.env.local"
	kindNamespace    = "sub2api"
	kindImage        = "sub2api:local"
	kindSecretName   = "sub2api-secret"
	kindExtraEnvName = "sub2api-extra-env"
	kindNodePort     = "30080"

	// annotationSourceCommit 记录 Deployment 当前跑的镜像是从哪份源码构建的。
	// 镜像 tag 固定为 :local，不记下来就无从判断集群里是不是当前工作区的代码。
	annotationSourceCommit = "sub2api.dev/source-commit"
)

var kindServices = []string{"sub2api", "postgres", "redis"}

func runKind(args ...string) error {
	return runCmd("go", append([]string{"run", kindPkg}, args...)...)
}

func captureKind(args ...string) (string, error) {
	return captureErr("go", append([]string{"run", kindPkg}, args...)...)
}

func cmdKind(args []string) error {
	if len(args) == 0 {
		return usageErrorf("kind", "kind 需要一个动作")
	}
	action, rest := args[0], args[1:]
	if action == "logs" {
		return kindLogs(rest)
	}
	actions := map[string]func() error{
		"up":      kindUp,
		"reload":  kindReload,
		"env":     kindEnv,
		"status":  kindStatus,
		"info":    kindInfo,
		"forward": kindForward,
		"psql":    kindPsql,
		"redis":   kindRedisCLI,
		"down":    kindDown,
	}
	run, ok := actions[action]
	if !ok {
		return usageErrorf("kind", "未知的 kind 动作：%s", action)
	}
	if len(rest) > 0 {
		return usageErrorf("kind", "%s 不接受参数", action)
	}
	return run()
}

func kindClusterName() string { return envOr("SUB2API_KIND_CLUSTER", defaultKindCluster) }

func kindPort() string { return envOr("SUB2API_KIND_PORT", defaultKindPort) }

func kindURL() string { return "http://localhost:" + kindPort() }

func kindContext() string { return "kind-" + kindClusterName() }

func kindKubectl(args ...string) error {
	return runCmd("kubectl", append([]string{"--context", kindContext()}, args...)...)
}

func captureKindKubectl(args ...string) (string, error) {
	return capture("kubectl", append([]string{"--context", kindContext()}, args...)...)
}

// kubectl 命令都在 sub2api 命名空间下执行
func kindNS(args ...string) []string {
	return append([]string{"--context", kindContext(), "-n", kindNamespace}, args...)
}

func kindUp() error {
	if err := requireCommands("docker", "kubectl", "git"); err != nil {
		return err
	}
	if !succeeds("docker", "info") {
		return errors.New("docker 已安装但引擎未就绪")
	}

	cluster := kindClusterName()
	exists, err := kindClusterExists(cluster)
	if err != nil {
		return err
	}
	if !exists {
		// 在建集群之前检查：建集群这一步才会占用端口，后面的镜像构建很慢，
		// 在这里发现冲突只花一秒。
		if err := checkKindHostPort(cluster); err != nil {
			return err
		}
		if err := createKindCluster(cluster); err != nil {
			return err
		}
	} else {
		logf("kind", "使用已有的 kind 集群 %q", cluster)
		if err := validateKindPortMapping(cluster); err != nil {
			return err
		}
	}
	if err := kindKubectl("wait", "--for=condition=Ready", "nodes", "--all", "--timeout=120s"); err != nil {
		return err
	}

	// 镜像构建最慢，先做
	if err := buildAndLoadKindImage(cluster); err != nil {
		return err
	}
	if err := ensureKindNamespace(); err != nil {
		return err
	}
	if err := ensureKindSecret(); err != nil {
		return err
	}
	if _, err := syncKindExtraEnv(); err != nil {
		return err
	}

	logf("kind", "部署 PostgreSQL 和 Redis...")
	if err := kindKubectl("apply", "-f", kindPostgresPath, "-f", kindRedisPath); err != nil {
		return err
	}
	if err := waitForKindDeployment("postgres", "300s"); err != nil {
		return err
	}
	if err := waitForKindDeployment("redis", "180s"); err != nil {
		return err
	}

	logf("kind", "部署 sub2api...")
	existed := succeeds("kubectl", kindNS("get", "deployment/sub2api")...)
	if err := kindKubectl("apply", "-f", kindSub2APIPath); err != nil {
		return err
	}
	if existed {
		if err := restartKindSub2API(); err != nil {
			return err
		}
	} else {
		// 新建的 Deployment 直接用刚加载的镜像，不需要再 restart 一次
		if err := waitForKindDeployment("sub2api", "300s"); err != nil {
			return err
		}
		if err := stampKindDeploymentIdentity(); err != nil {
			return err
		}
	}

	if err := waitForKindHealth(); err != nil {
		return err
	}
	logf("kind", "sub2api 已就绪")
	return printKindAccess()
}

func createKindCluster(cluster string) error {
	previousContext, _ := capture("kubectl", "config", "current-context")
	configPath, cleanup, err := renderKindConfig()
	if err != nil {
		return err
	}
	defer cleanup()
	logf("kind", "创建 kind 集群 %q（宿主机端口 %s）...", cluster, kindPort())
	if err := runKind("create", "cluster", "--name", cluster, "--config", configPath); err != nil {
		return err
	}
	// kind 会把新集群设为全局 current-context。这里所有命令都显式带 --context，
	// 所以恢复用户原来的选择，包括“原来没有选中任何 context”。
	return restoreKubectlContext(previousContext)
}

func restoreKubectlContext(previous string) error {
	switch previous {
	case kindContext():
		return nil
	case "":
		if err := runCmd("kubectl", "config", "unset", "current-context"); err != nil {
			return fmt.Errorf("清除 kind 设置的 kubectl context：%w", err)
		}
		return nil
	default:
		if err := runCmd("kubectl", "config", "use-context", previous); err != nil {
			return fmt.Errorf("恢复 kubectl context %q：%w", previous, err)
		}
		return nil
	}
}

func kindReload() error {
	if err := requireCommands("docker", "kubectl", "git"); err != nil {
		return err
	}
	cluster := kindClusterName()
	if err := requireKindCluster(cluster); err != nil {
		return err
	}
	if err := buildAndLoadKindImage(cluster); err != nil {
		return err
	}
	if _, err := syncKindExtraEnv(); err != nil {
		return err
	}
	// 集群在但还没部署过时，restart 一个不存在的 Deployment 会报错；
	// 这种情况镜像已经加载好了，下一次 `kind up` 会直接用上。
	if !succeeds("kubectl", kindNS("get", "deployment/sub2api")...) {
		fmt.Printf("deployment/sub2api 不存在，运行 %s kind up 部署\n", mk())
		return nil
	}
	if err := restartKindSub2API(); err != nil {
		return err
	}
	if err := waitForKindHealth(); err != nil {
		return err
	}
	logf("kind", "sub2api 已重新部署：%s", kindURL())
	return nil
}

// kindEnv 只同步 sub2api.env.local 并重启，不重新构建镜像。
func kindEnv() error {
	if err := requireCommands("kubectl"); err != nil {
		return err
	}
	if err := requireKindCluster(kindClusterName()); err != nil {
		return err
	}
	changed, err := syncKindExtraEnv()
	if err != nil {
		return err
	}
	if !changed {
		logf("kind", "%s 无变化", kindExtraEnvPath)
		return nil
	}
	if err := restartKindSub2API(); err != nil {
		return err
	}
	if exists(kindExtraEnvPath) {
		logf("kind", "已应用 %s", kindExtraEnvPath)
	} else {
		logf("kind", "已移除额外环境变量")
	}
	return nil
}

func kindStatus() error {
	if err := requireCommands("kubectl"); err != nil {
		return err
	}
	cluster := kindClusterName()
	fmt.Printf("集群:   %s（context %s）\n", cluster, kindContext())
	exists, err := kindClusterExists(cluster)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("集群不存在，运行 %s kind up\n", mk())
		return nil
	}
	if err := validateKindPortMapping(cluster); err != nil {
		fmt.Printf("警告: %v\n", err)
	}
	deployed, _ := captureKindKubectl("-n", kindNamespace, "get", "deployment/sub2api",
		"-o", "jsonpath={.metadata.annotations.sub2api\\.dev/source-commit}")
	if deployed == "" {
		deployed = "未知"
	}
	fmt.Printf("地址:   %s（%s）\n", kindURL(), httpHealth(kindURL()+"/health"))
	fmt.Printf("代码:   集群 %s / 工作区 %s\n\n", deployed, resolveCommitSHA())
	if err := kindKubectl("-n", kindNamespace, "get", "deployments,pods,pvc", "-o", "wide"); err != nil {
		fmt.Printf("命名空间 %s 还没有部署\n", kindNamespace)
	}
	fmt.Printf("\n运行 %s kind logs 查看日志\n", mk())
	return nil
}

func kindInfo() error {
	if err := requireCommands("kubectl"); err != nil {
		return err
	}
	if err := requireKindCluster(kindClusterName()); err != nil {
		return err
	}
	return printKindAccess()
}

func printKindAccess() error {
	email, err := kindSecretValue("ADMIN_EMAIL")
	if err != nil {
		return err
	}
	password, err := kindSecretValue("ADMIN_PASSWORD")
	if err != nil {
		return err
	}
	fmt.Printf("\n  地址:     %s\n  管理员:   %s\n  密码:     %s\n  kubectl:  kubectl --context %s -n %s get pods\n\n",
		kindURL(), email, password, kindContext(), kindNamespace)
	return nil
}

func kindLogs(args []string) error {
	if len(args) > 1 {
		return usageErrorf("kind", "logs 最多接受一个服务名（%s）", strings.Join(kindServices, "、"))
	}
	service := "sub2api"
	if len(args) == 1 {
		service = args[0]
		if !slices.Contains(kindServices, service) {
			return usageErrorf("kind", "未知服务 %q，可选：%s", service, strings.Join(kindServices, "、"))
		}
	}
	if err := requireCommands("kubectl"); err != nil {
		return err
	}
	if err := requireKindCluster(kindClusterName()); err != nil {
		return err
	}
	return kindKubectl("-n", kindNamespace, "logs", "deployment/"+service, "--tail=200", "-f")
}

// kindForward 把集群内的 PostgreSQL 和 Redis 转发到 127.0.0.1，Ctrl-C 结束。
func kindForward() error {
	if err := requireCommands("kubectl"); err != nil {
		return err
	}
	if err := requireKindCluster(kindClusterName()); err != nil {
		return err
	}
	pgPort := envOr("SUB2API_KIND_PG_PORT", "15432")
	redisPort := envOr("SUB2API_KIND_REDIS_PORT", "16379")
	pgPassword, err := kindSecretValue("POSTGRES_PASSWORD")
	if err != nil {
		return err
	}
	redisPassword, err := kindSecretValue("REDIS_PASSWORD")
	if err != nil {
		return err
	}
	fmt.Printf("PostgreSQL: postgres://sub2api:%s@127.0.0.1:%s/sub2api?sslmode=disable\n", pgPassword, pgPort)
	fmt.Printf("Redis:      redis://:%s@127.0.0.1:%s/0\n", redisPassword, redisPort)
	fmt.Println("Ctrl-C 结束转发")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	forwards := [][]string{
		kindNS("port-forward", "svc/postgres", pgPort+":5432"),
		kindNS("port-forward", "svc/redis", redisPort+":6379"),
	}
	exited := make(chan error, len(forwards))
	for _, args := range forwards {
		cmd := exec.CommandContext(ctx, "kubectl", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { exited <- cmd.Wait() }()
	}
	// 任意一个转发退出（端口被占、Pod 重建）就整体结束，免得留下一半在跑。
	select {
	case <-ctx.Done():
		return nil
	case err := <-exited:
		stop()
		if err == nil {
			err = errors.New("kubectl port-forward 意外退出")
		}
		return fmt.Errorf("端口转发已结束：%w", err)
	}
}

func kindPsql() error {
	if err := requireCommands("kubectl"); err != nil {
		return err
	}
	if err := requireKindCluster(kindClusterName()); err != nil {
		return err
	}
	return kindKubectl("-n", kindNamespace, "exec", "-it", "deployment/postgres", "--", "psql", "-U", "sub2api", "-d", "sub2api")
}

func kindRedisCLI() error {
	if err := requireCommands("kubectl"); err != nil {
		return err
	}
	if err := requireKindCluster(kindClusterName()); err != nil {
		return err
	}
	return kindKubectl("-n", kindNamespace, "exec", "-it", "deployment/redis", "--", "redis-cli")
}

func kindDown() error {
	// 删除集群仍要和容器引擎打交道，所以需要 docker
	if err := requireCommands("docker"); err != nil {
		return err
	}
	cluster := kindClusterName()
	exists, err := kindClusterExists(cluster)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("kind 集群 %q 不存在\n", cluster)
		return nil
	}
	return runKind("delete", "cluster", "--name", cluster)
}

func kindClusterExists(name string) (bool, error) {
	output, err := captureKind("get", "clusters")
	if err != nil {
		return false, fmt.Errorf("列出 kind 集群：%w", err)
	}
	return slices.Contains(strings.Fields(output), name), nil
}

func requireKindCluster(cluster string) error {
	exists, err := kindClusterExists(cluster)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("kind 集群 %q 不存在，先运行 %s kind up", cluster, mk())
	}
	return nil
}

// checkKindHostPort 用连接而不是绑定来探测端口：macOS 上监听方普遍设置了
// SO_REUSEADDR，绑定探测会把正在使用的端口误判为空闲。
func checkKindHostPort(cluster string) error {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+kindPort(), 300*time.Millisecond)
	if err != nil {
		return nil
	}
	_ = conn.Close()
	return fmt.Errorf("宿主机端口 %s 已被占用，kind 集群 %q 需要发布这个端口\n  停掉占用进程，或换一个：SUB2API_KIND_CLUSTER=<name> SUB2API_KIND_PORT=<port> %s kind up",
		kindPort(), cluster, mk())
}

func validateKindPortMapping(cluster string) error {
	output, err := capture("docker", "port", cluster+"-control-plane", kindNodePort+"/tcp")
	if err != nil {
		return fmt.Errorf("查看 kind 集群 %q 的端口映射：%w", cluster, err)
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), ":"+kindPort()) {
			return nil
		}
	}
	return fmt.Errorf("kind 集群 %q 没有把 NodePort %s 发布到宿主机端口 %s（当前：%s）\n  用建集群时的 SUB2API_KIND_PORT 运行，或 %s kind down 后重新 kind up",
		cluster, kindNodePort, kindPort(), strings.TrimSpace(output), mk())
}

// renderKindConfig 把 kind-config.yaml 里的 hostPort 换成配置的端口，写到临时文件。
func renderKindConfig() (string, func(), error) {
	template, err := os.ReadFile(kindConfigPath)
	if err != nil {
		return "", nil, fmt.Errorf("读取 %s：%w", kindConfigPath, err)
	}
	file, err := os.CreateTemp("", "sub2api-kind-config-*.yaml")
	if err != nil {
		return "", nil, fmt.Errorf("创建渲染后的 kind 配置：%w", err)
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	if _, err := file.WriteString(renderKindConfigContent(string(template), kindPort())); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return file.Name(), cleanup, nil
}

// renderKindConfigContent 只改 containerPort 为 sub2api NodePort 的那条映射的
// hostPort，其余内容原样保留，所以 kind-config.yaml 本身仍是一份可读的有效配置。
func renderKindConfigContent(template, hostPort string) string {
	lines := strings.Split(template, "\n")
	var containerPort string
	for i, line := range lines {
		// 映射是 YAML 列表，containerPort 前面带 "- "，hostPort 没有
		trimmed := strings.TrimPrefix(strings.TrimSpace(line), "- ")
		switch {
		case strings.HasPrefix(trimmed, "containerPort:"):
			containerPort = strings.TrimSpace(strings.TrimPrefix(trimmed, "containerPort:"))
		case strings.HasPrefix(trimmed, "hostPort:") && containerPort == kindNodePort:
			indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
			lines[i] = indent + "hostPort: " + hostPort
		}
	}
	return strings.Join(lines, "\n")
}

func buildAndLoadKindImage(cluster string) error {
	args := []string{"build", "-f", "Dockerfile", "--build-arg", "COMMIT=" + resolveCommitSHA(), "-t", kindImage}
	if platform := os.Getenv("SUB2API_IMAGE_PLATFORM"); platform != "" {
		args = append(args, "--platform", platform)
	}
	logf("kind", "构建镜像 %s（commit %s）...", kindImage, resolveCommitSHA())
	if err := runCmd("docker", append(args, ".")...); err != nil {
		return fmt.Errorf("构建 %s 失败：%w", kindImage, err)
	}
	logf("kind", "加载镜像到 kind 集群 %q...", cluster)
	if err := runKind("load", "docker-image", kindImage, "--name", cluster); err != nil {
		return fmt.Errorf("kind load %s 失败：%w", kindImage, err)
	}
	return nil
}

func ensureKindNamespace() error {
	manifest, err := captureKindKubectl("create", "namespace", kindNamespace, "--dry-run=client", "-o", "yaml")
	if err != nil {
		return fmt.Errorf("渲染命名空间 %s：%w", kindNamespace, err)
	}
	return runStdin(manifest, "kubectl", "--context", kindContext(), "apply", "-f", "-")
}

// ensureKindSecret 只在 Secret 不存在时创建：PostgreSQL 数据在 PVC 上，密码必须
// 稳定；JWT / TOTP 密钥稳定，reload 之后登录态和 2FA 才继续有效。
func ensureKindSecret() error {
	if succeeds("kubectl", kindNS("get", "secret", kindSecretName)...) {
		return nil
	}
	logf("kind", "生成 %s...", kindSecretName)
	values := map[string]string{
		"ADMIN_EMAIL":    envOr("SUB2API_ADMIN_EMAIL", "admin@sub2api.local"),
		"ADMIN_PASSWORD": os.Getenv("SUB2API_ADMIN_PASSWORD"),
	}
	random := map[string]int{"POSTGRES_PASSWORD": 16, "REDIS_PASSWORD": 16, "JWT_SECRET": 32, "TOTP_ENCRYPTION_KEY": 32}
	if values["ADMIN_PASSWORD"] == "" {
		random["ADMIN_PASSWORD"] = 8
	}
	for key, size := range random {
		value, err := randomHex(size)
		if err != nil {
			return err
		}
		values[key] = value
	}
	args := kindNS("create", "secret", "generic", kindSecretName)
	for key, value := range values {
		args = append(args, "--from-literal="+key+"="+value)
	}
	_, err := captureErr("kubectl", args...)
	return err
}

func kindSecretValue(key string) (string, error) {
	encoded, err := captureKindKubectl("-n", kindNamespace, "get", "secret", kindSecretName, "-o", "jsonpath={.data."+key+"}")
	if err != nil {
		return "", fmt.Errorf("读取 %s/%s：%w", kindSecretName, key, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("解码 %s/%s：%w", kindSecretName, key, err)
	}
	return string(decoded), nil
}

// syncKindExtraEnv 把 sub2api.env.local 同步成 ConfigMap，sub2api 通过 envFrom
// 引用它（optional，可以不存在）。返回内容是否有变化，有变化才需要重启。
// 文件解析交给 kubectl 的 --from-env-file，免得和它的规则不一致。
func syncKindExtraEnv() (bool, error) {
	current, currentExists, err := kindConfigMapData(kindExtraEnvName)
	if err != nil {
		return false, err
	}
	if !exists(kindExtraEnvPath) {
		if !currentExists {
			return false, nil
		}
		logf("kind", "%s 已删除，移除 ConfigMap %s", kindExtraEnvPath, kindExtraEnvName)
		return true, kindKubectl("-n", kindNamespace, "delete", "configmap", kindExtraEnvName)
	}
	rendered, err := captureErr("kubectl", kindNS("create", "configmap", kindExtraEnvName,
		"--from-env-file="+kindExtraEnvPath, "--dry-run=client", "-o", "json")...)
	if err != nil {
		return false, fmt.Errorf("解析 %s：%w", kindExtraEnvPath, err)
	}
	var desired struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(rendered), &desired); err != nil {
		return false, err
	}
	if currentExists && maps.Equal(current, desired.Data) {
		return false, nil
	}
	logf("kind", "同步 %s -> ConfigMap %s", kindExtraEnvPath, kindExtraEnvName)
	return true, runStdin(rendered, "kubectl", "--context", kindContext(), "apply", "-f", "-")
}

func kindConfigMapData(name string) (map[string]string, bool, error) {
	if !succeeds("kubectl", kindNS("get", "configmap", name)...) {
		return nil, false, nil
	}
	out, err := captureErr("kubectl", kindNS("get", "configmap", name, "-o", "json")...)
	if err != nil {
		return nil, false, err
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &cm); err != nil {
		return nil, false, err
	}
	return cm.Data, true, nil
}

// restartKindSub2API：:local 是可变 tag，镜像换了 Pod 不会自己换，必须
// rollout restart。重启完成后重新记录源码 commit。
func restartKindSub2API() error {
	logf("kind", "重启 sub2api...")
	if err := kindKubectl("-n", kindNamespace, "rollout", "restart", "deployment/sub2api"); err != nil {
		return err
	}
	if err := waitForKindDeployment("sub2api", "300s"); err != nil {
		return err
	}
	return stampKindDeploymentIdentity()
}

func stampKindDeploymentIdentity() error {
	return kindKubectl("-n", kindNamespace, "annotate", "deployment/sub2api", "--overwrite",
		annotationSourceCommit+"="+resolveCommitSHA())
}

func waitForKindDeployment(name, timeout string) error {
	if err := kindKubectl("-n", kindNamespace, "rollout", "status", "deployment/"+name, "--timeout="+timeout); err != nil {
		dumpKindNamespace()
		return fmt.Errorf("deployment/%s 未就绪，详见上面的事件和日志（或 %s kind logs %s）：%w", name, mk(), name, err)
	}
	return nil
}

// dumpKindNamespace 在部署失败时打印 Pod、最近事件和各服务日志尾部，省得再
// 手动去敲一串 kubectl。
func dumpKindNamespace() {
	fmt.Fprintf(os.Stderr, "\n---- 命名空间 %s ----\n", kindNamespace)
	commands := [][]string{
		kindNS("get", "pods", "-o", "wide"),
		kindNS("get", "events", "--sort-by=.lastTimestamp"),
	}
	for _, service := range kindServices {
		commands = append(commands, kindNS("logs", "deployment/"+service, "--tail=50"))
	}
	for _, args := range commands {
		fmt.Fprintf(os.Stderr, "\n$ kubectl %s\n", strings.Join(args, " "))
		cmd := exec.Command("kubectl", args...)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	}
}

// waitForKindHealth 等到宿主机上的地址真正可用。rollout status 在新 Pod Ready
// 时就返回，但 NodePort 的转发规则要再过一两秒才切到新 Pod，这期间请求会被
// 重置；不等这一步，“已就绪”之后紧跟着的第一个请求可能失败。
func waitForKindHealth() error {
	endpoint := kindURL() + "/health"
	deadline := time.Now().Add(60 * time.Second)
	for {
		status := httpHealth(endpoint)
		if status == "healthy" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s 在 60 秒内没有就绪（%s）；运行 %s kind status 和 %s kind logs 排查", endpoint, status, mk(), mk())
		}
		time.Sleep(time.Second)
	}
}

func httpHealth(endpoint string) string {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return "无响应"
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("/health 返回 %d", resp.StatusCode)
	}
	return "healthy"
}

func randomHex(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
