<p align="center">
  <img src="assets/brand/vc-workspace-app-icon.svg" width="96" height="96" alt="VC Workspace">
</p>

<h1 align="center">VC Workspace</h1>

VC Workspace 是构建在 Proxmox VE 之上的开源虚拟桌面平台。当前 MVP 支持本地/OIDC 身份、用户与 AI Agent 的显式桌面分配、真实 PVE 桌面生命周期、Guest 本地权限与首批会话策略、macOS 原生客户端与 FreeRDP 数据面、带独占桌面租约的 MCP 服务，以及首批 Terraform/OpenTofu 资源。

项目公开仓库将发布在 [Veritas-Calculus/vc-workspace](https://github.com/Veritas-Calculus/vc-workspace)。首次 push 前，该地址只作为项目的固定源码入口，不展示虚构的版本、Star 或 Release 状态。

## 当前状态

面向人的核心 MVP 已形成闭环：管理员可以管理真实 PVE 桌面、维护 Personal Owner 与 Shared 用户/组授权、查看 Agent 当前控制的 VM、控制 Guest 本地权限与每桌面的文本剪贴板、驱动器重定向和受管背景，并查询审计；本地账号支持自助改密和管理员重置；用户可以从签名的 macOS 客户端使用独立 Guest 本地账号和可撤销短期凭据进入 Debian 13 或 Windows 桌面。平台 OIDC 已通过真实 Keycloak Authorization Code 流程与组撤权同步测试；Debian 13 SSSD LDAP 已通过真实 StartTLS、NSS 和 PAM 验收，独立 Debian 12 Guest 已通过 Samba AD 实验环境入域和在线撤权。正式模板、生产目录、FreeIPA、Windows 域登录与客户端目录认证交互仍需对应环境验收。当前 MCP Computer Use 已在 Debian 13 按独立 Agent 用户完成端到端实测；Windows 旧共享会话证据不代表当前每用户链路已完成，Windows 默认 MCP 暂不开放。Terraform/OpenTofu Provider 的首批桌面授权资源和桌面数据源已经实现，更多资源、细粒度 Token Scope 和正式发布仍在后续。项目仍处于发布前补齐阶段；会话策略目前只有桌面级哈希快照和 Debian 实机证据，作用域解析、密码学签名、Guest 回报、Windows 实机、动态水印，以及 Web 模板实建、Windows GVT-g、生产身份环境、Kubernetes 运维闭环和开源发布尚未全部完成。

当前状态、优先级与 TODO 统一维护在 [项目状态与 TODO](docs/plan/status.md)；里程碑和实机证据见 [MVP 开发计划](docs/plan/mvp.md)，其他架构、UI 与运维约束从 [文档索引](docs/README.md) 进入。

## 技术栈

- 控制面与 MCP：Go
- Web 管理端：TypeScript、React、Vite
- 数据库：PostgreSQL
- 会话核心与 Guest Agent：Rust
- macOS 客户端：SwiftUI/AppKit；FreeRDP 原生 `NSView` 数据面随 `.app` 一起分发
- Windows Agent 后台连接组件：C/FreeRDP 核心库 + Go 进程监管；当前为独立验收路径，尚未开放默认 Windows MCP

Rust 不进入首个控制面纵向切片。只有 Guest Agent、协议和跨平台会话核心需要它，避免在 CRUD、认证和 PVE 编排上维护两套服务端运行时。

## 仓库结构

```text
apps/
  control-plane/    Go API、认证、Broker 与 PVE Reconciler
  mcp/              面向 AI Agent 的 MCP 服务
  session-gateway/  独立 TLS/WebSocket 网关（默认客户端尚未接线）
  session-worker/   无界面 RDP 保活组件、原生协议测试与容器验收入口
  web/              Web 管理端与用户入口
clients/
  macos/            Tier 1 原生客户端
  windows/          后续客户端
  linux/            后续客户端
crates/
  session-core/     跨平台会话核心
  guest-agent/      Windows/Linux Guest Agent
  windows-session/  Windows SID/WTS、管道与 Job 执行边界（Rust Win32 FFI）
internal/           Go 模块化单体内部包
api/                OpenAPI 契约
assets/brand/       官方标志、应用图标与发布源文件
deploy/             Compose、容器、Kubernetes 清单与 Packer OS 模板
docs/               当前有效的产品、架构、设计与运维文档
tools/              Terraform/OpenTofu Provider 等开发与运维工具
```

## 本地开发

前置依赖：Go 1.27、Rust 1.88、Node.js 22、pnpm 10、Python 3（Guest 显示回归测试）、Docker。构建 PVE OS 模板时另需 Packer 1.15 与 Proxmox 插件 1.2.4。

```bash
cp .env.example .env
docker compose -f deploy/compose.yaml up -d postgres
pnpm install
pnpm dev
```

身份集成可用 `make identity-lab-check` 启动一次性 Keycloak、OpenLDAP、PostgreSQL 与 Debian 13 SSSD 客户端并运行真实协议测试；范围和限制见 [身份集成实验室](docs/operations/identity-lab.md)。

基础回归使用 `go test ./...`、`go vet ./...`、`pnpm check`、`pnpm test` 和 `pnpm build`。数据库测试必须显式设置 `VC_WORKSPACE_TEST_DATABASE_URL`，指向专用 PostgreSQL 测试库；每个测试创建并回收独立 schema，需要建/删 schema 权限，未设置时集成测试会跳过，不能把这类结果算作数据库验收。并发与撤权回归用 `go test -race ./internal/store ./internal/httpapi -count=2`。

Web 恢复回归先执行 `pnpm --filter @vc-workspace/web exec playwright install chromium` 和 `pnpm build`，在一个终端启动 `pnpm --filter @vc-workspace/web exec vite preview --host 127.0.0.1 --port 5188 --strictPort`，另一个终端运行 `pnpm --filter @vc-workspace/web test:browser`；可用 `VC_WORKSPACE_WEB_TEST_URL` 指定其他预览地址。测试使用独立浏览器上下文和拦截的 API fixture，截图写入系统临时目录并输出路径，不访问真实 PVE。它验证过期重登和任务恢复，不替代原生 RDP 实机测试。

平台资源 IaC 的首批能力位于 `tools/terraform-provider-vcworkspace/`。从 Web 的“访问控制 → 账号”创建有期限的 IaC API 凭证，通过环境变量配置 Provider，再用 Terraform 或 OpenTofu 管理显式桌面授权；当前资源、导入格式和源码构建方式见 [Provider README](tools/terraform-provider-vcworkspace/README.md)。

控制面默认监听 `127.0.0.1:8080`，Web 默认监听 `127.0.0.1:5173`。Web 根路径 `/` 是开源项目 Landing Page，管理端从 `/console` 进入。PVE 凭证只通过环境变量或宿主机凭证文件提供，禁止提交到 Git。

服务端可以通过 `deploy/container/` 中的三个非特权容器镜像和 `deploy/kubernetes/base/` 的 Kustomize 清单部署到 Kubernetes；外部 HTTPS、集群内端口、PVE、MCP 与 RDP 数据面边界见 [Kubernetes 部署](docs/operations/kubernetes.md)。

构建 macOS 客户端需要 CMake 和 Ninja；最终用户不需要安装 Homebrew、OpenSSL 或 FreeRDP。运行 `make macos-app` 会下载并校验固定版本的 FreeRDP 与 OpenSSL LTS 源码，面向 macOS 14 构建原生数据面，再把桥接层、运行库、证书对话框和官方应用图标一并签入 `clients/macos/.build/app/VC Workspace.app`。如更新 1024px 品牌源图，运行 `clients/macos/scripts/build-app-icon.sh` 可重复生成 ICNS。产物使用 Bundle ID `ac.plz.vc-workspace` 并注册 `vc-workspace://` OIDC/App Link；Web 可用 `vc-workspace://connect?vmid=<VMID>` 打开当前控制面中的授权桌面。客户端在迁移期仍能读取旧 URL Scheme 与旧钥匙串项。真实验收不使用裸 `swift run` 代替。MCP 启动方式见 `apps/mcp/README.md`。

## 文档维护规则

- `docs/plan/status.md` 是当前进度和 TODO 的唯一来源；`docs/plan/mvp.md` 保存里程碑退出条件与验收证据。
- `docs/architecture/` 只描述当前采用的系统，不保留已否决方案的长篇副本。
- 被替代的决定在 ADR 中标记 `Superseded`，不复制一份“新版说明”。
- 每个功能变更必须在同一提交中更新对应文档和 OpenAPI。

## 许可证

[Apache License 2.0](LICENSE)。该许可证允许商业使用、修改和闭源分发，同时包含明确的专利授权和商标限制条款。贡献方式见 [贡献指南](CONTRIBUTING.md)，漏洞报告见 [安全策略](SECURITY.md)。
