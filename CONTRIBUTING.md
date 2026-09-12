# 贡献指南

VC Workspace 接受 Issue、缺陷修复和功能贡献。提交前请先读 [文档索引](docs/README.md)，当前进度和待办只在 [项目状态与 TODO](docs/plan/status.md) 维护。

## 开发环境

Go 1.27、Rust 1.85、Node.js 22、pnpm 10、Docker。构建 PVE OS 模板另需 Packer 1.15 与 Proxmox 插件 1.2.4，构建 macOS 客户端另需 CMake 和 Ninja。

```bash
cp .env.example .env
docker compose -f deploy/compose.yaml up -d postgres
pnpm install
pnpm dev
```

控制面监听 `127.0.0.1:8080`，Web 监听 `127.0.0.1:5173`，管理端入口是 `/console`。

## 提交前的检查

```bash
make check
```

`make check` 覆盖 Go test 与 vet、Rust test、Web 类型检查/Vitest/生产构建、Swift test、macOS release 构建和签名校验。改动涉及以下范围时补充对应检查：

- Packer 镜像定义：`make images-check`
- Kubernetes 清单：`make k8s-check`
- 容器镜像：`make container-build`

需要真实 PVE 的 `*-live-*` 目标不在 CI 运行，只在你自己的验收环境执行，结果写入 [MVP 开发计划](docs/plan/mvp.md)。

### 数据库集成回归

设置 `VC_WORKSPACE_TEST_DATABASE_URL` 才会执行数据库集成测试；未设置时跳过，不代表通过。使用专用回归数据库，不要指向开发库、生产库或正在用于实机验收的控制面数据库。测试按随机 schema 隔离并清理数据，但仍共享数据库进程、磁盘、WAL 和连接资源；schema 隔离不等于容量或故障隔离。

```bash
VC_WORKSPACE_TEST_DATABASE_URL='postgres://user:password@127.0.0.1:5432/vcw_test' \
  go test -race -p=1 ./internal/store ./internal/httpapi -count=1 -timeout=5m
```

运行前检查数据盘和 WAL 的可用空间，并使用独立持久化卷。不要在承载验收状态的小容量 tmpfs 上运行全量迁移回归；临时盘写满会影响同库其他 schema，容器退出还可能丢失整个临时数据目录。数据库出现恢复/磁盘错误后先停止测试并检查数据库终态，不重复重跑测试或重启容器。恢复旧备份后，先核对外部 Guest 的账号版本、连接和撤销状态，不能直接启动控制面，也不能降低 Guest 的版本栅栏以适配旧库。

## 变更要求

- 功能变更必须在同一提交中同步更新 [api/openapi.yaml](api/openapi.yaml) 和对应文档。契约与实现分离的提交不会被合并。
- 任务状态只改 `docs/plan/status.md`，不要在其他文档复制一份 TODO。
- 状态改为「已完成」需要同时具备实现、自动测试和该任务要求的真实环境证据；只有代码时用「已实现，待验收」。
- 被替代的决定在 ADR 中标记 `Superseded`，不要新增一份并行说明。
- 架构文档只描述当前采用的系统，不保留已否决方案的长篇副本。

## 不得提交的内容

凭证、私钥、API Token、一次性构建密码、商业 ISO，以及内网地址、集群节点名和本机绝对凭证路径。PVE 凭证只通过环境变量或宿主机凭证文件提供。示例变量文件只表达字段形状。

## 提交信息

说明这次改动解决什么问题、为什么选当前方案，不要写成工作日志或测试流水。正文使用简体中文，标识符、命令和字面量用反引号标注。

## 许可证

向本仓库提交的贡献按 Apache License 2.0 授权，依据该许可证第 5 节，无需另行签署协议。
