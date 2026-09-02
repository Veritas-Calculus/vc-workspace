# 项目状态与 TODO

更新日期：2026-09-02

本文是项目“现在做到哪里、接下来做什么”的唯一当前事实源。[MVP 开发计划](mvp.md)保留里程碑、实施顺序和实机证据；架构文档只描述已经采用的边界，不重复维护任务清单。

## 当前结论

VC Workspace 已完成面向人的核心纵向闭环：管理员可以初始化本地账号、接入真实 PVE、创建和分配受管桌面、管理 Guest 本地管理员权限并查询审计；用户可以通过签名、自包含的 macOS 客户端登录、搜索桌面、启动虚拟机并在应用内连接 Debian 13 或 Windows 桌面。

项目当前处于“功能型 MVP 已闭环，发布与关键扩展补齐中”，还不是生产就绪版本。23 个既有里程碑中，17 个满足退出条件，4 个已实现主体但仍需真实环境验收，2 个尚在排期；这个计数只表示里程碑状态，不是按工作量计算的完成百分比。

当前最重要的缺口是：MCP 只能完成 Agent 认证、桌面租约和电源控制，还不能读取屏幕或向桌面输入；会话策略与水印尚未实现；OIDC、Web 镜像实建、Windows GVT-g 和 Kubernetes 仍缺最后的真实环境验收；Windows/Linux 原生客户端尚未开始。

## 能力状态

| 领域 | 状态 | 当前能力 | 未闭环部分 |
|---|---|---|---|
| Go 控制面与 PVE | 已完成 | 真实清单、受管桌面、幂等 Clone/启停、Job 收敛、QGA 状态与连接凭据签发 | 多副本 Job leader election 与跨网络 Session Gateway |
| Web 与 Landing | 已完成 | 七个管理工作区、普通用户桌面入口、中英文、明暗主题、响应式布局和 App Link | 正式下载入口需等待首个 Release |
| 本地身份与授权 | 已完成 | Setup Token、本地用户、Web/Native Session、用户/Agent 显式分配与撤销 | 登录限速等生产加固 |
| OIDC | 已实现，待实测 | Web/Native Authorization Code + PKCE、state、nonce 和一次性 Native code | 接入一个真实 IdP 完成登录、注销、失败与密钥轮换验收 |
| macOS 客户端 | 已完成 | Keychain、桌面卡片与搜索、冷启动建联、内嵌 FreeRDP、缩放、全屏、剪贴板和 Dock 窗口恢复 | 发布签名、Notarization、安装包与自动更新 |
| OS 模板 | 进行中 | Debian 13 正式模板；Windows 10/11 技术模板及 Guest Agent/QGA/RDP 管线 | 从 Web 以独立 VMID 完整实建；Windows 使用授权且仍受支持的生产介质 |
| GPU | 进行中 | PVE PCI/mdev 发现、Resource Mapping、Intel GVT-g 调度与 Debian Guest 验收 | Windows Intel 驱动、加速状态和真实 RDP 图形帧验收；独显实机透传 |
| 镜像 Bootstrap | 已实现，待实测 | Web 参数、内网镜像源、Packer allowlist、一次性秘密和脱敏进度 | Web→构建→testing→克隆→Agent/QGA/RDP→ready 的完整实建 |
| Guest 本地权限 | 已完成 | Debian sudo 与 Windows Administrators 的标准用户/本地管理员切换，连接前强制复核 | 应用允许列表、便携程序与用户目录执行控制 |
| 应用层审计 | 已完成 | 追加写入、脱敏、结果筛选、搜索、游标分页和管理端详情 | 保留期、SIEM/WORM、哈希链、可信代理与合规级防篡改 |
| MCP Agent Lease | 已完成 | 每 Agent 凭证、显式分配、独占租约、TTL、`control_epoch` 和租约内启停 | 无 |
| MCP Computer Use | 未实现 | 已有 Lease 可作为控制权边界 | 截图、可访问性树、键鼠输入、人工接管、审批、Artifact 与逐动作审计 |
| 会话管控 | 已排期 | 已有 Guest 权限策略，不等同于会话数据防泄漏 | M11 剪贴板/文件/磁盘重定向/背景；M12 水印与更多设备通道 |
| Kubernetes | 已实现，待集群验收 | 非特权镜像、Kustomize、探针、Secret 分界、NetworkPolicy 和单一 HTTPS 入口 | 目标集群 rollout、TLS、存储、备份、真实网络策略和 Builder Worker |
| Windows/Linux 客户端 | 未开始 | 仅保留目录和跨平台 Session Core 边界 | WinUI 与 GTK4 客户端的登录、桌面库、数据面和发布链 |
| 开源发布 | 进行中 | 已建立首个 commit；许可证定为 Apache-2.0，`LICENSE`、`CONTRIBUTING.md` 与 `SECURITY.md` 就位；Landing 已指向固定仓库地址 | 尚未 push 到远端，远端 GitHub Actions 未验证，也没有 Release |

## TODO

优先级含义：P0 是下一阶段闭环或公开发布的阻塞项；P1 是生产部署和安全体验所需；P2 是 MVP 后扩展。任务只有在“完成条件”全部满足后才能改为已完成。

| ID | 优先级 | 状态 | 任务 | 完成条件 |
|---|---|---|---|---|
| RELEASE-01 | P0 | 进行中 | 建立开源仓库基线 | 许可证已定为 Apache-2.0，`LICENSE`、`CONTRIBUTING.md`、`SECURITY.md` 和首个 commit 已完成；剩余条件是确定版权归属主体、清理文档中的内网拓扑、push 到远端并让 GitHub Actions 在远端通过 |
| AI-01 | P0 | 待开发 | MCP Computer Use 最小纵向切片 | Agent 在有效 Lease 和当前 `control_epoch` 内获取截图/可访问性快照并执行受控键盘、鼠标和文本输入；人工接管立即使旧控制权失效；每个动作有审计和有界超时；Debian 13 与 Windows 11 各完成一条真实任务 |
| POLICY-01 | P0 | 待开发 | 完成 M11 会话策略基础 | 数据模型、作用域解析、签名快照、Web 管理、macOS/Guest 执行端和审计完成；剪贴板、文件/磁盘重定向及受管背景在 Debian 13/Windows 上实测，强制策略未应用时拒绝建联 |
| IMAGE-01 | P0 | 待实测 | 通过 Web 完成模板 Bootstrap 闭环 | 使用未占用 VMID 从 Web 构建 Debian 13 与 Windows 11，完成模板转换、完整克隆、Agent/QGA/RDP 验收后手工切到 `ready`；Windows 10 是否生产启用由介质支持与授权决定 |
| GPU-01 | P0 | 待实测 | 完成 Windows Intel GVT-g 验收 | Windows Guest 安装匹配驱动，确认设备与硬件加速状态，并从 macOS 客户端收到实际加速桌面图形帧；失败时模板不得默认启用该档位 |
| WATERMARK-01 | P1 | 待开发 | 完成 M12 水印与扩展防泄漏 | 水印覆盖用户/设备/会话/时间，并通过缩放、全屏、重连、多显示器和 DPI 验收；补齐打印、USB、音频、拖放策略及漂移告警 |
| DEPLOY-01 | P1 | 待环境 | 在目标 Kubernetes 集群部署 | 确定 Registry、不可变镜像 Digest、域名/TLS、StorageClass、Namespace 和 Secret；完成 rollout、探针、NetworkPolicy、数据库持久化/恢复及升级回滚验收 |
| OIDC-01 | P1 | 待环境 | 接入真实 OIDC Provider | 明确 issuer、client、redirect URI 和 claims；Web 与 macOS 完成首次绑定、重复登录、注销、拒绝、过期和 JWKS 轮换测试；本地管理员始终可登录 |
| NET-01 | P1 | 待开发 | 弱网基准与跨网络 Session Gateway | 用可重复配置覆盖带宽、RTT、抖动、丢包和短时断网；记录首帧、交互延迟、重连与资源占用；为客户端不能直达 Guest 的场景交付短期票据和 Gateway，不公开 3389 |
| RELEASE-02 | P1 | 待开发 | macOS 可分发发布链 | 使用正式 Developer ID 签名并 Notarize，生成可校验安装包、SBOM/第三方许可证、版本和下载链接；Web 仅在真实 Release 存在后显示安装兜底 |
| AUDIT-01 | P1 | 待开发 | 生产审计与认证加固 | 配置可信代理、登录/初始化限速、保留期、导出/SIEM、独立写入角色与 WORM 或哈希链；完成备份恢复和权限测试 |
| BUILDER-01 | P1 | 待设计 | 拆分可恢复 Image Builder Worker | 固定安装器 HTTP 端口范围，使用专用 Job/Worker 与最小权限 PVE Token；控制面重启不丢失状态，Kubernetes 不使用 `hostNetwork` |
| CLIENT-01 | P2 | 未开始 | Windows 原生客户端 | WinUI 登录、凭证存储、桌面卡片、App Link、RDP 数据面、会话策略和安装签名形成闭环 |
| CLIENT-02 | P2 | 未开始 | Linux 原生客户端 | GTK4 登录、Secret Service、桌面卡片、RDP 数据面、会话策略及主流发行版打包形成闭环 |
| APPCTRL-01 | P2 | 未开始 | Guest 应用执行控制 | 在 sudo/Administrators 之外增加允许列表或执行控制，明确覆盖 AppImage、便携 EXE 和用户目录安装，不扩大现有权限模式的安全承诺 |
| PROTOCOL-01 | P2 | 待研究 | 高性能串流协议评估 | 用同一组画质、输入、音频、弱网、GPU 和运维指标比较 RDP、WebRTC/QUIC 等候选；有数据后再决定是否替换或并存 |

## 需要用户或环境提供的决定

- 公开发布时的版权归属主体：仓库组织 `Veritas-Calculus`、Go 模块路径中的 `virtual-cable` 与提交作者三者当前不一致，需确定写入版权声明的名称。
- Kubernetes 的镜像仓库、公开域名/TLS、StorageClass、Namespace 和数据库备份方案。
- 用于验收的 OIDC Provider、Client Registration 与回调域名。
- Windows 10/11 的授权介质和生产支持策略，尤其是已经结束普通支持的 Windows 10 22H2。
- macOS 发布所用 Apple Developer Team、Developer ID、Notarization 和更新渠道。
- Session Gateway 的目标网络拓扑：仅内网/VPN，还是需要互联网入口。

## 最近验证

- 2026-09-02：`make check` 通过，包含 Go test/vet、Rust tests、Web TypeScript/Vitest/production build、Swift tests、macOS release build、自包含依赖和深度签名验证。
- 2026-09-02：`make images-check` 通过，Debian 13 与 Windows Packer 定义完成格式和 syntax-only 校验。
- 2026-09-02：`make k8s-check` 通过，Kustomize 基线与 Secret 模板可渲染并通过客户端 dry-run；这不等同于真实集群部署。
- 2026-09-02：签名后的 macOS App 实测关闭、隐藏和最小化后可从 Dock 恢复，显式退出仍正常结束进程。
- 真实 PVE、模板、GPU、macOS RDP、权限和审计的详细证据保存在 [MVP 开发计划](mvp.md)和 [PVE 开发环境](../operations/pve-development.md)。

## 维护规则

- 每次功能变更同时更新本页对应任务状态和相关架构/运维文档；不要在其他文档复制一份 TODO。
- “已完成”必须同时具备实现、自动测试和该任务要求的真实环境证据；只有代码或清单时使用“已实现，待验收”。
- 临时测试凭证、一次性密码、Token、Guest 地址和本机绝对凭证路径不得写入文档。
- 已取消或改变方向的任务必须从本页移除或明确标记，并在 ADR 中记录原因，不能让旧路线继续看起来有效。
