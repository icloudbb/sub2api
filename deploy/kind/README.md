# 本地 kind 部署

在本机用 [kind](https://kind.sigs.k8s.io/) 起一个单节点 Kubernetes 集群，部署
sub2api + PostgreSQL + Redis，用于本地部署测试。参照 buildmax 的 `./make kind`：
任务由仓库根目录的 `./make`（Windows 用 `make.bat`）转发给 `tools/mk` 里的 Go
任务运行器执行，这里只放 Kubernetes 清单。

依赖：Docker、kubectl、Go（kind 通过 `go run sigs.k8s.io/kind@v0.31.0` 固定版本运行）。

```bash
./make kind up        # 创建集群 sub2apidev，构建 sub2api:local 镜像并部署
./make kind info      # 访问地址和管理员账号（密码首次部署时随机生成）
./make kind reload    # 改完代码后：重新构建镜像并滚动重启 sub2api
./make kind status    # 健康检查、集群里跑的代码 commit、Pod 状态
./make kind logs      # 跟踪 sub2api 日志；kind logs postgres / redis 看其它服务
./make kind forward   # PostgreSQL -> 127.0.0.1:15432，Redis -> 127.0.0.1:16379
./make kind psql      # 集群内 psql
./make kind redis     # 集群内 redis-cli
./make kind down      # 删除集群，数据一并清空
```

`./make help kind` 查看完整说明。

默认访问地址 `http://localhost:18080`（只监听 127.0.0.1）。没有用 8080，是为了避开
同样跑在 kind 上的 buildmaxdev。

## 说明

- 镜像用仓库根目录的 `Dockerfile` 构建，tag 固定为 `sub2api:local`，通过 `kind load`
  加载进节点（`imagePullPolicy: Never`），因此每次 reload 都会 `rollout restart`，并把
  源码 commit 记在 Deployment 的 `sub2api.dev/source-commit` 注解上。
- 首次部署生成 `sub2api-secret`（DB/Redis 密码、JWT、TOTP 密钥、管理员账号），之后不再
  覆盖：PostgreSQL 数据在 PVC 上，reload 后数据、登录态、2FA 都保留。
- 首次部署前可通过 `SUB2API_ADMIN_EMAIL` / `SUB2API_ADMIN_PASSWORD` 指定管理员账号。
- 额外环境变量：复制 `sub2api.env.example` 为 `sub2api.env.local`（已被 gitignore），
  `./make kind env` 应用。它会覆盖 `sub2api.yaml` 里 `sub2api-defaults` 的同名默认值。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `SUB2API_KIND_CLUSTER` | `sub2apidev` | 集群名，可并行跑多个集群 |
| `SUB2API_KIND_PORT` | `18080` | 宿主机端口（建集群时生效） |
| `SUB2API_IMAGE_PLATFORM` | 空 | 传给 `docker build --platform` |
| `SUB2API_KIND_PG_PORT` / `SUB2API_KIND_REDIS_PORT` | `15432` / `16379` | `kind forward` 的本地端口 |
