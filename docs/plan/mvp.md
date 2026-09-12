# MVP 开发计划

更新日期：2026-09-05

当前阶段、优先级和 TODO 统一维护在 [项目状态与 TODO](status.md)。本文保留既有里程碑、实施约束和实机验收证据，不作为第二份任务清单。

## 目标

交付可运行的 VDI MVP：管理员可初始化本地账号、管理真实 PVE 桌面；用户可在 macOS 客户端登录、启动桌面并通过 RDP 进入 Linux 图形界面；AI Agent 可通过 MCP 独占租用桌面。

## 范围

MVP 包含：

- 本地管理员初始化、登录和注销。
- 本地账号自助改密与管理员重置；OIDC/目录密码继续由上游身份系统管理。
- 单个 OIDC Provider 的 Authorization Code + PKCE 登录。
- 单 PVE 集群清单、模板发现、Clone、Start、Stop 与异步任务跟踪。
- 简洁 Web 控制台，不显示无法操作的能力标签或装饰性指标。
- macOS 客户端本地登录、Keychain Session、桌面列表、启动与 FreeRDP 建联；OIDC 保留但不阻塞数据面验收。
- Debian 13 XFCE、Windows 10/11 的 Packer 模板定义，以及跨平台 VC Workspace Guest Agent。
- 管理端可维护镜像来源、构建节点、模板 VMID、Guest Agent、构建状态和默认 GPU 档位。
- PVE GPU 实时发现；Intel GVT-g 按 mdev 类型和剩余实例调度，完整直通档位继续以 IOMMU 可用为启用前提。
- 控制面通过 Guest Agent 发现桌面 IP，为每个授权平台用户维护独立的稳定 Guest 本地账号，并在签发持久化的短期 RDP Connection Session 时轮换一次性密码。
- 管理员可把每台桌面设为标准用户或本地管理员；控制面在建联前通过 QEMU Guest Agent 对全部已绑定账号收敛 Linux sudo / Windows Administrators 组，未应用则拒绝连接。
- 管理员可创建、停用本地用户和 AI Agent，维护 Personal 唯一 Owner、Shared 用户/组授权以及 Agent 分配；Web、Native 与 MCP 只返回授权后的清单。
- Debian 13 提供 SSSD AD/FreeIPA/LDAP 配置档案，Windows 10/11 提供 AD Domain Join 档案；SSSD OIDC 与 Windows Entra RDP 以默认关闭的实验能力交付。
- 每个 Agent 使用独立可轮换凭证；MCP 的桌面清单、Agent Lease 生命周期和租约内电源控制都绑定到认证身份。
- 通过有期限的 IaC API 凭证和 Terraform/OpenTofu Provider 管理首批平台资源，不把 PVE Token 或一次性密码写入 IaC State。
- PostgreSQL、审计事件、Docker Compose 和关键测试。

MVP 不包含：自研视频协议、跨网络 Session Gateway、Warm Pool、Guacamole、通用厂商 vGPU/SR-IOV 适配与自动 NUMA 优化、多租户、计费、Windows/Linux 完整客户端、生产级 Secret Manager。

## 里程碑

| ID | 交付物 | 状态 | 退出条件 |
|---|---|---|---|
| M0 | 仓库与契约 | 已完成 | Go/TS/Rust/Swift 工作区可解析；目录、设计语言、OpenAPI、Compose 和 CI 入口就位 |
| M1 | 本地身份 | 已完成 | 全新数据库只能通过 Setup Token 创建首个管理员；本地账号可自助改密或由管理员重置；Web 与 Native Session 可撤销 |
| M2 | PVE 只读接入 | 已完成 | 页面显示真实集群、节点、存储、模板与 VM；凭证不进入响应或日志 |
| M3 | 受控桌面创建 | 已完成 | 从模板幂等 Clone；UPID 可追踪；重复请求不会产生第二台 VM |
| M4 | OIDC | 已实现，待接 IdP | Web/Native 使用 Code + PKCE、state 与 nonce；未提供真实 IdP 配置，尚未做外部登录验收 |
| M5 | macOS 客户端控制面 | 已完成 | SwiftUI 可构建；本地/OIDC 登录、Keychain Session 和真实桌面列表可用 |
| M6 | MCP/Agent Lease | 已完成 | AI 可领取、查询、启动、停止和释放专属桌面；同一桌面只允许一个活动租约 |
| M7 | MVP 验收 | 已完成 | 自动测试通过；真实 PVE 创建/启动/停止验证；文档无重复计划或失效入口 |
| M8 | macOS RDP 闭环 | 已完成 | 客户端可启动桌面、等待 Guest Agent、获取逐次轮换凭据的 RDP 描述符并打开实际 XFCE 桌面；VM 158 已完成 macOS FreeRDP 实机验收 |
| M9 | OS 模板与 GPU 基线 | 进行中 | 三个 OS 模板均完成克隆/QGA/Agent/RDP 验收；控制面已接入完整直通与 GVT-g 两条路径，6 个 PVE 9.2.10 节点已建 mediated Resource Mapping。Debian 13 的 V5_4 克隆、冷启动、i915/render node、Agent 和 RDP 端口已实机验收；生产启用前还需完成 Windows Guest Intel 驱动与实际 RDP 图形帧验收 |
| M10 | 镜像 Bootstrap WebUI | 已实现，待独立 VMID 实建验收 | 管理员可在 Web 配置 Debian 13、Windows 10/11 的节点、未占用 VMID、ISO/校验和、内网镜像源、存储、网络和硬件，使用一次性构建凭据启动固定 Packer 定义并观察持久化进度；完成后只进入 `testing`，不自动宣称可用 |
| M11 | 会话策略基础 | 进行中 | 每桌面策略、revision、哈希快照、Web 管理、macOS FreeRDP 参数、Debian/Windows 执行命令和拒绝建联已实现；Debian 13 VM 158 已通过真实 Web→API→QGA→Guest 收敛。退出仍需多作用域解析、快照签名/Guest 回报、Windows 与 macOS 行为实测 |
| M12 | 水印与扩展防泄漏 | 已排期 | 会话水印包含用户、设备、会话和时间；补齐打印、USB、音频和拖放通道控制、策略漂移告警及 Windows/Linux 客户端一致性验证 |
| M13 | Web 产品工作区闭环 | 已完成 | 管理端以“桌面 / 近期任务 / 基础设施 / 桌面镜像 / GPU 加速 / 访问控制 / 审计”七个真实工作区组织现有能力，普通用户只显示自己的桌面；服务端按 PVE `vc-workspace` 标签签发显式 `managed` 能力，并在迁移期只读识别旧标签，不按名称猜测；1280px/320px、明暗主题、中英文和键盘操作完成浏览器验收 |
| M14 | macOS 客户端真实闭环 | 已完成 | Bundle ID `ac.plz.vc-workspace`、可执行文件 `VCWorkspace` 且注册 `vc-workspace://` 回调的 `.app` 已完成本地登录、旧 Keychain Session 迁移、可搜索桌面卡片、启动/取消/重试、Guest Agent 等待、FreeRDP 异常回传、会话聚焦/断开、Dock 窗口恢复、退出清理和自适应网络；本机客户端已覆盖真实 PVE 冷启动和直接连接 |
| M15 | 统一桌面分配与客户端授权 | 已完成 | 管理员维护本地/OIDC 用户、同步或本地组、独立 Agent 凭证、受管桌面注册表，以及 Personal Owner、Shared 用户/组和 Agent 三类显式授权；Native、Web 与 MCP 在服务端统一鉴权并记录审计；停用身份、轮换凭证和移除 Agent 分配立即阻断访问或吊销租约 |
| M16 | 开源项目 Landing Page | 已完成 | `/` 用中英文和明暗主题说明 PVE、原生客户端、镜像/GPU 与 MCP 的当前实现，唯一主操作进入 `/console`，源码操作指向固定 GitHub 仓库；无假指标、路线图占位或未发布安装命令 |
| M17 | Web 到 macOS App Link | 已完成 | Web 桌面名称可通过 `vc-workspace://connect?vmid=<VMID>` 唤起签名后的客户端；链接不含秘密，客户端使用 Keychain Session 重新读取可见桌面后才连接，冷启动与未登录状态可延后消费；非法/重复参数被拒绝 |
| M18 | macOS 原生会话与自包含运行时 | 已完成 | 签名、自包含的 `.app` 已对真实 Debian 13 与 Windows 11 完成图像、键鼠、双向剪贴板、自适应网络、全屏、主动断开和退出清理验收；Pre-session、取消、断开恢复和原位重连状态已在 Debian 实机回归；Debian 已完成 Retina 2× Guest 动态分辨率及 96/192 DPI 自动联动，Windows 动态分辨率/DPI 回归列入发布加固；FreeRDP Mac 剪贴板修复以幂等源码补丁随构建应用 |
| M19 | Guest 本地权限策略 | 已完成 | Web 可配置标准用户/本地管理员；关机桌面保留 pending，运行中立即收敛，连接前强制复核；Debian 13 sudo/NOPASSWD 和中文 Windows 10/11 Administrators 组均完成真实 PVE 加入、移除、独立验证与状态恢复 |
| M20 | 管理审计闭环 | 已完成 | 安全事件追加写入并在持久化前脱敏；登录成功/失败、退出、配置、Guest 权限、PVE 任务结果、原生连接与 AI Lease 可按结果和用户/事件/目标搜索；管理员游标查询和 Web 详情完成实际数据库与浏览器验收 |
| M21 | 统一应用品牌标志 | 已完成 | 从六个不同方向完成尺寸对比，最终确定第六稿 `Agent Fabric`；Web、favicon、README 与 macOS Bundle/登录页使用同一品牌源，签名应用完成实际启动验收 |
| M22 | Kubernetes 服务端部署 | 已实现，待集群部署验收 | Web、控制面、远程 MCP 与 Bootstrap PostgreSQL 具备非特权容器、Kustomize、探针、Secret 分界和默认拒绝网络策略；公网只暴露 `443/TCP`，PVE、OIDC、RDP、QGA 与镜像构建端口有明确边界；待确定镜像仓库、域名/TLS、StorageClass 和命名空间后完成真实集群 rollout |
| M23 | MCP Computer Use | 已完成 | 五类 MCP 工具、Agent 身份、独占 Lease/TTL/`control_epoch`、QGA spool、交互用户 helper、有界 worker、Linux AT-SPI/Windows UI Automation、管理端活动租约、人工接管失效和脱敏审计已实现；Debian 13 VM 158 与 Windows 11 VM 9113 的真实端到端任务均通过 |
| M24 | 统一平台与 Guest OS 身份 | 已实现，部分目录实测 | P0 的每用户 Guest 本地账号、唯一 Personal Owner、Shared 组授权、可撤销短期连接和 macOS 主动释放已通过自动/数据库测试；受管桌面持久化 PVE OS 类型，API 强制 Profile 平台匹配且 Web 只展示兼容配置；P1 的 SSSD AD 已在独立 Debian 12 PVE Guest→Samba AD 完成直接入域、PAM/Kerberos、允许组撤权和幂等复验，LDAP 已通过 Docker 真实目录；正式 Debian 13、生产 AD/LDAP、FreeIPA、Windows AD，以及 P2 IdP Guest 登录和 macOS 目录凭据交互仍待验收 |
| M25 | 平台资源 IaC | 已实现首批，待发布与扩展 | Web 管理员可签发/撤销有期限且只显示一次的 API 凭证；Terraform/OpenTofu Provider 通过公开 API 管理桌面授权并读取受管桌面，支持 Import 和漂移读取，Terraform 真实 apply/漂移/destroy 已通过；更多资源、Token Scope、OpenTofu CLI 与签名发布验收继续由 IAC-01 跟踪 |

## 实施顺序

1. 先交付 M0→M2 的纵向切片，Web 使用真实 PVE 数据而不是样例卡片。
2. 再交付 M1，所有后续写操作必须经过本地管理员 Session。
3. M3 只支持从已存在模板克隆，并以 `Idempotency-Key` 和数据库 Job 双重防重。
4. M4 与 M5 共用身份模型；Native 经系统浏览器登录，再用一次性授权码换取独立 Session。
5. M6 复用 Broker，不允许 MCP 直接持有 PVE 凭证。
6. M8 先采用客户端可直达 Guest IP 的 RDP；Session Gateway 在跨网段和公网部署前补齐。
7. M9 已完成 Debian 13 正式模板与 Windows 10/11 技术管线实机验收；Windows 生产启用前换用受支持且授权合规的介质。HD 530 优先使用集群现有 GVT-g，在每次创建桌面前读取实际 `available`；完整直通保留给需要独占 GPU 的工作负载，独显继续复用 PCI Resource Mapping/IOMMU 模型。
8. M10 的构建入口只运行仓库内允许列表中的 Packer 定义，密码和 Windows 产品密钥不写入数据库、Job 或命令行。Web 构建完成后必须再做克隆、Guest Agent、RDP 和授权验收，才能由管理员切换为 `ready`。
9. M11 先落统一策略和强制执行闭环，再做 M12 水印。所谓“禁止 copy”必须同时区分剪贴板、拖放、磁盘映射、打印和截图；单独关闭剪贴板不是完整防泄漏能力。
10. M13 先让已实现的控制面能力形成清晰、可操作的 Web 工作区，再增加用户、OIDC、策略和 AI 管理页面。没有稳定 API 与可执行动作的模块不以占位页出现在导航中。
11. M14 必须从 `.app` 启动实际客户端完成验收，不能用 Swift 单元测试或直接调用 FreeRDP 代替。连接验收至少覆盖一次停止桌面的启动链路、一次已运行桌面的直接连接、自适应网络、RDP 图像帧、最小/默认/宽窗口缩放、全屏往返、关闭/隐藏/最小化后的 Dock 恢复、用户主动断开和异常退出反馈；测试 Session 与一次性凭据不得写入文档或命令行。
12. M15/M24 把分配关系和权限判定落在控制面：Personal 只允许一个 Owner；Shared 才接受直接用户或组授权；Agent 仍使用独立分配和 Lease。普通用户的 Web/Native 清单与连接操作、Agent 的清单/Lease/电源操作都使用同一桌面注册表。目录身份不复用平台 OIDC Token，也不把域加入 Secret 持久化。
13. M16 只陈述仓库内已实现并有验收记录的能力。仓库首次 push 后再从真实 GitHub 状态补充许可证、版本和安装入口；公开部署前将语言切换升级为可索引的稳定语言路径与 `hreflang`。
14. M17 使用自定义 URL Scheme 覆盖任意自托管地址的 MVP，不把 VMID 链接当成授权票据。Universal Links 需要项目稳定域名、站点关联文件和发布签名中的 Associated Domains，不能承诺为未知部署域名动态生效。
15. M18 不接受“把 `sdl-freerdp` 隐藏在 App Bundle 中”作为内嵌完成。数据面必须与 SwiftUI 共享同一应用生命周期和 AppKit 视图树；发布包不得解析到 Homebrew、MacPorts 或 `/usr/local` 的第三方动态库。FreeRDP 更新必须同时固定版本和 SHA-256，并复测自适应策略及受管的带宽诊断参数。
16. M19 的本地权限是 Guest 内系统权限，不等同于应用白名单。标准用户阻止 sudo/Administrators 级安装；若要禁止 AppImage、便携 EXE 或用户目录安装，后续必须增加应用允许列表和执行控制，不能用文案扩大当前安全边界。
17. M20 当前提供应用层 append-only 查询与统一脱敏，不等同于法规级不可抵赖审计。独立写入角色、保留期、SIEM/WORM 和密码学防篡改在生产合规阶段单独验收。
18. M21 的唯一主标志和应用图标源位于 `assets/brand/`。业务页面不得自行派生替代图形，也不得把品牌标志当作功能图标；macOS ICNS 由仓库脚本从 1024px 品牌源图生成。
19. M22 的基线只让 Web Ingress 对外；控制面、MCP 和 PostgreSQL 必须保留集群内 Service。远程 MCP 调用方使用逐 Agent 签发的凭证，不能复用控制面内部 Token。镜像构建因 Packer 安装器反向 HTTP 端口尚未固定，在 Kubernetes 基线中保持关闭，后续以专用 Worker/Job 和受控路由实现，不使用 `hostNetwork` 扩大边界。
20. M23 延续 M6 的授权边界，MCP 不直接接收 PVE 凭证、用户密码、任意 shell 或文件路径。QGA 只承载短期请求和固定命令，桌面观察/输入必须在 `vdi` 交互会话 helper 内执行；人工连接、Lease 释放、身份停用或分配移除都会使旧 epoch 失效。首版真实验收已完成，后续文件/Artifact、高风险动作审批和连续视频作为独立扩展推进。
21. M24 的平台身份、桌面授权和 Guest OS 身份是三个独立边界。默认 `managed_local` 可由 Broker 完整托管；目录档案只负责 Guest 加域/SSSD 收敛，不能在 macOS 尚未具备目录密码、Kerberos 或设备码交互时宣称最终用户闭环。实验模式必须保持 fail-closed。
22. M25 的部署 IaC 与平台资源 IaC 分开维护。Kustomize 管理 Kubernetes 部署，VC Workspace Provider 只调用公开控制面 API；Provider 不直连数据库/PVE，不管理密码或一次性 Secret，并且每个资源在扩展前都要具备 Read、Import、漂移、权限和审计测试。

## 验收命令

```bash
make check
make images-check
docker compose -f deploy/compose.yaml up --build
```

macOS 构建机需要 CMake 和 Ninja；数据面运行时由 `build-native-rdp.sh` 固定 FreeRDP 3.31.0 与 OpenSSL 3.5.8 LTS 的版本和校验和，统一面向 macOS 14 构建并随 `.app` 分发，验收机不安装 FreeRDP 或 OpenSSL。连接凭据直接写入当前进程的 FreeRDP settings，不进入进程参数、环境变量或持久文件。

2026-09-01 实机验收证明：控制面在 VM 158 迁移到 `infra-node6` 后仍能动态定位节点，读取 Guest Agent 就绪标记、轮换 `vdi` 凭据并签发 RDP 描述符；macOS FreeRDP 完成 TLS/RDP 协商、进入 Active 状态并接收桌面图像帧，VM 侧同时确认 Xorg `:10`、XFCE 窗口管理器与重连会话正常。

2026-09-02 正式模板证明：Packer 使用 Debian 13.6.0 官方 netinst ISO 与管理员配置的内网 APT 源完成 VMID 9100，并转换为 PVE 模板。完整克隆 VM 9101 首启后通过 QEMU Guest Agent、VC Workspace 就绪标记、Debian 13/XFCE 配方和 macOS FreeRDP 一次性密码认证。

2026-09-02 Windows 技术模板证明：Packer 使用 node4 上已校验的 Windows 10 22H2 与 VirtIO ISO 完成 VMID 9110，Sysprep/Cloudbase 泛化成功并转换为 PVE 模板。完整克隆 VM 9112 首启后通过 Windows 10 Pro build 19045、QEMU Guest Agent、VC Workspace Agent 启动任务与就绪标记、3389 监听、密码轮换和 macOS FreeRDP 认证；由于普通 Home/Pro 22H2 已结束支持，后台保持停用和 legacy 标记。

2026-09-02 Windows 11 技术模板证明：Packer 使用经 Microsoft SHA-256 清单校验的 Windows 11 Enterprise 25H2 Evaluation 与 VirtIO ISO 完成 VMID 9111，在 Sysprep 前关闭并等待 BitLocker 完全解密，随后成功泛化并转换为模板。完整克隆 VM 9113 首启后通过 Windows 11 build 26200、Cloudbase-Init、QEMU Guest Agent、VC Workspace Agent 就绪标记、3389、一次性密码轮换与 macOS FreeRDP 图形会话；首次真实桌面登录发现中国区介质仍显示一次隐私/跨境数据提示，因此构建配方已增加设备级 `DisablePrivacyExperience`，下一次模板构建必须用无人值守首登复验。后台保持默认停用，生产启用前还必须替换为授权介质。

2026-09-02 Web 产品工作区证明：浏览器使用包含受管桌面、普通 PVE VM、节点、存储、镜像、GVT-g 设备、访问控制、成功/运行/失败任务和审计事件的完整状态集走查七个工作区；320px 下页面根宽度保持 320px、表格在 286px 容器内独立滚动，1280px 下页面根无横向溢出；简体中文/英文与浅色/深色组合均完成渲染验证。仓库级 `make check` 全部通过。

2026-09-02 统一桌面授权证明：第 13 个迁移在隔离 PostgreSQL 17 上完整应用；真实事务验证用户/Agent 分配、禁用用户撤销 Native Session、移除 Agent 分配吊销活动 Lease 并递增 `control_epoch`、凭证轮换后旧摘要失效。控制面以只读方式同步用户提供的真实 PVE 集群，识别 7 台受管桌面；只向测试用户和 Agent 分配 VM 9101 后，Web、Native API 和 MCP `desktop_list` 都只返回这一台。MCP HTTP 以逐 Agent Bearer 凭证解析身份，`stdio` 在每次工具调用前重验凭证，工具 Schema 不再接受 `agent_id`；无效凭证返回 401，浏览器 Origin 返回 403。访问控制 WebUI 在 1280px/320px、中文/英文、浅色/深色下无页面横向溢出；新增用户和 Agent 弹窗首焦点及 Esc 关闭后的焦点返回完成浏览器验收。全量 `make check`、PostgreSQL 集成测试与 Kubernetes 清单校验通过，验证期间未启用 PVE 写操作。

2026-09-02 macOS 客户端真实闭环证明：从签名后的 `VC Workspace.app` 完成本地登录和跨重启 Keychain Session 恢复；“自动”档位直接连接运行中的 VM 158，“弱网”档位同样建立实际 RDP TCP 会话。随后将 VM 158 正常关机，从客户端执行“启动并连接”，控制面经 Guest Agent 发现冷启动后的新地址并自动建立 RDP 会话。主动断开和退出应用均确认无 FreeRDP 残留进程。实测迭代修复了机器名插值、原始错误淹没界面、主动断开误报、应用退出遗留子进程及开发重签名阻塞 Keychain 等问题。早期试验机 VM 153 的 PVE Agent 通道虽已启用但来宾 Agent 不响应，客户端正确保持等待并允许取消；该机已恢复停止状态且继续排除在调度之外。

2026-09-02 macOS Dock 生命周期证明：签名后的 `VC Workspace.app` 关闭最后一个窗口后进程继续运行；从 Dock 激活可恢复已关闭的主窗口，最小化与隐藏状态也能取消并置前。显式退出仍会终止进程，应用包随后再次通过深度签名和自包含依赖验证。

2026-09-02 Landing Page 证明：真实浏览器覆盖 `/` 和 `/console` 的简体中文/英文、浅色/深色、1280px/320px 与键盘焦点；两个路由分别逐像素扫描 320–1280px 共 961 个宽度，根节点横向溢出均为 0。源码操作解析为 `https://github.com/Veritas-Calculus/vc-workspace`，生产 TypeScript/Vite 构建和 Web 测试通过。

2026-09-04 App Link 最终证明：Web 对受管桌面生成仅含 VMID 的 `vc-workspace://connect` 链接；浏览器在中英文、明暗主题和 1280px/320px 下验证名称入口与可访问名称，320px 根宽度保持 320px，App 图标无需横向滚动即可看到。Swift 测试覆盖新链接、迁移期旧链接，以及密码、Token、重复参数、路径、片段和伪造协议拒绝；签名后的 `.app` 同时注册新 Scheme 和只读兼容 Scheme，后续不再生成旧链接。

2026-09-02 macOS 应用内会话证明：客户端不再启动 `sdl-freerdp` 子进程，而是通过仓库内 C ABI Bridge 在 SwiftUI 会话页挂载 FreeRDP 原生 `MRDPView`。构建脚本从固定 SHA-256 的 FreeRDP 3.31.0 与 OpenSSL 3.5.8 LTS 源码面向 macOS 14 生成约 12 MB 的自包含 `.app`；8 个内嵌动态库及应用通过深度签名，所有 Mach-O 均无构建机依赖或绝对 RPATH。签名 `.app` 已分别使用“弱网”连接运行中的 Debian 13 VM 9101、使用“自动”从停止状态启动并连接 Windows 11 VM 9113，验证真实图像帧、窗口缩放后的鼠标命中、应用切换后的键盘焦点、双向文本剪贴板、全屏往返、主动断开与退出清理。实测修复了内嵌视图连接后未恢复剪贴板轮询、点击不回收键盘焦点、Smart Sizing 视口尺寸未同步，以及 FreeRDP Mac 文本格式回退和后台线程写 pasteboard 的问题；M18 退出条件已满足。

2026-09-04 macOS Retina 动态显示证明：参考 AWS WorkSpaces 的窗口行为后，客户端通过 RDP Display Control 重新协商 Guest framebuffer，而不是仅用 Smart Sizing 拉伸固定桌面。真实 Debian 13 VM 158 先被测试工具重置为 96 DPI，新版客户端创建连接后经受限控制面通道自动同步为 192 DPI；默认窗口为 1640×972，macOS 系统“缩放”最大化为 5120×2670，原生全屏为 5120×2796，退出全屏回到 5120×2670。分辨率与 DPI 均由 Guest 内 `xrandr` / `xfconf-query` 独立读取，最大化和全屏后的远程终端输入均成功。首帧通道就绪竞态通过远端 resize 确认和最多 5 次、500 ms 间隔的有界重试解决；远端像素按 AppKit 逻辑点 × backing scale（上限 2×）计算。Windows Guest 的 Display Control 与 DPI 仍需单独实机回归。

2026-09-02 原生会话阶段证明：FreeRDP 3.31.0 的原生 Mac Client、仓库内 C ABI Bridge 与 SwiftUI `NSViewRepresentable` 已形成单进程会话生命周期；签名 `.app` 包含 FreeRDP/WinPR/OpenSSL 动态库和证书 NIB，体积约 11 MB，`codesign --verify --deep --strict` 与运行时 `dlopen` 通过，所有非系统依赖均使用 `@rpath`。无真实凭据的失败回收冒烟与后续 Debian/Windows 真实 PVE 图像帧验收均已完成；历史阶段记录不再作为未完成门槛。

2026-09-02 Guest 本地权限证明：第 11 个数据库迁移已在 PostgreSQL 17 实际应用。控制面 API 在运行中的 Debian 13 VM 158 返回 `local_admin`/`standard` 的 applied revision，并独立确认本地管理员可直接运行 `sudo -n`、切回标准用户后该提权失败；最终恢复为标准用户。Windows 实测暴露并修复了中文系统不能按英文 `BUILTIN\\Administrators` 查组的问题，最终统一通过 SID `S-1-5-32-544` 获取本地化组；VM 9113 与 VM 9112 完成管理员加入/移除验证，VM 9112 最终用正常 shutdown 恢复关机。PVE `guest-exec-status` 的瞬时超时采用 60 秒有界重试，QGA 未运行时策略保持失败/待应用且连接被拒绝。Web 在真实 PVE 七台受管桌面清单上完成中文深色 1280px、英文浅色 360px、键盘语义和零控制台错误验收；隔离测试数据库和会话已删除。

2026-09-02 管理审计证明：第 12 个迁移在隔离 PostgreSQL 17 数据库实际应用并确认三个查询索引；真实 API 验证成功/失败筛选、事件/用户/目标搜索、游标分页、未知账号目标保留、初始化 Token 不落库，以及普通用户读取返回 403 并追加 `audit.read_denied`。浏览器使用真实事件完成中文深色、中文浅色和英文浅色列表、筛选、详情与 30→36 条继续加载验证；普通用户重新加载后不显示审计入口。Go 全量测试、Web TypeScript 检查、16 个 Vitest 测试和生产构建通过，隔离数据库及会话在验收后删除。

2026-09-02 应用标志证明：以 16px、32px、64px 和展示尺寸比较六个不同标志方向，按最终选择切换为第六稿 `Agent Fabric`，并生成 SVG、1024px PNG 和包含 16–1024px 表示的 ICNS。真实浏览器在 Landing 与登录页完成浅色/深色检查；最新签名 `VC Workspace.app` 在退出旧进程后从构建产物实际启动，SwiftUI 登录页读取系统应用图标并显示新标志。Web 类型检查、测试与生产构建、Swift 测试、ICNS 反向解包及应用深度签名检查均通过。

2026-09-02 Kubernetes 部署证明：三个生产容器镜像在本机完整构建；使用隔离 Docker 网络让 Web、控制面和远程 MCP 以容器形态实际运行，验证 Web 同源 `/api/v1/ready`、鉴权后的 `/mcp` 工具目录、未鉴权 `401` 与浏览器 Origin `403`。`make k8s-check` 完成 Kustomize 渲染和客户端 dry-run。该记录证明镜像和清单可解析，不替代目标集群的 Ingress、TLS、持久卷、NetworkPolicy 与 rollout 验收。

2026-09-02 macOS 桌面库证明：签名 `VC Workspace.app` 以隔离控制面和 PostgreSQL 17 会话加载 VM 9101/9113 的真实 PVE 状态，验证桌面卡片在最小、默认和宽窗口下自适应，单卡不拉伸，名称/VMID/节点/状态搜索与搜索无结果恢复动作通过，且界面不暴露带宽调参。同一 App 从停止状态启动 VM 9101 并在 Guest Agent/桌面服务就绪后连接，验证默认→宽→最小窗口、原生全屏往返、各尺寸画面填充及缩放后鼠标命中/键盘输入。过程中修复 PVE 冷启动状态瞬时回退导致连接过早失败，以及全屏按钮可访问名称不跟随窗口状态的问题。主动断开、本地退出、VM 9101 关机、隔离数据库/会话清理和客户端控制面偏好恢复均已完成。

2026-09-04 MCP Computer Use 完整验收：五类 MCP 工具目录及真实 `tools/call` 转发测试通过，确认 Agent ID 只能来自认证上下文且 Lease/epoch/敏感标记不会被调用方覆盖。Guest 协议验证动作类型、单一有界 payload、短期过期时间和按块控制权复核；控制面先用 QGA 暂存请求，再以单次 `computer-dispatch` 固定命令原子发布、等待响应并清理，成功同步的同一 authority 在后续动作中复用，避免每个动作重复写入。Guest 动作仍由最长 15 秒的独立 worker 执行，PVE 瞬时传输错误只在额外的有界 transport budget 内幂等重试。

签名、自包含的 macOS 客户端分别建立 Debian 13 VM 158 与 Windows 11 VM 9113 的真实 RDP 会话。最终 Linux 产物直接验证 1280×800 JPEG、30 个原生 `linux_atspi` 节点、终端启动和文本/按键任务；完整 Agent Bearer→MCP HTTP→控制面→PVE QGA→Guest helper 链路再次完成同类任务，并产生 6 个成功动作审计事件。最终 Windows 产物直接验证 1280×800 JPEG、原生 `windows_uia`、鼠标、按键和长文本任务；完整链路再次通过并产生 8 个成功动作审计事件。两端都验证 Native Connection 人工接管同步写入 revoked authority、epoch 递增、旧 Lease 动作拒绝且审计不包含输入正文。Windows 的交互 helper 最终以隐藏、单实例 Scheduled Task 运行，Unicode 输入使用 20 ms 间隔的原生 SendInput，响应文件在关闭句柄后原子发布；macOS 客户端对两端完成全屏往返、原生窗口缩放、Smart Sizing、主动断开和应用再次激活回归。验收后 Windows VM 9113 正常关机恢复停止，Debian VM 158 保持原运行状态，隔离数据库、临时服务器和测试会话均已清理；M23 退出条件满足。

2026-09-05 统一身份 P0→P2 代码验收：PostgreSQL 17 从空库应用 16 个迁移，并从仅含前 13 个迁移的旧模型升级，确认多用户桌面自动转为 Shared 且两个既有授权都保留。事务测试覆盖 Personal Owner 冲突、Shared 本地组授权、多个本地组共存、OIDC 管理成员不可手工篡改、用户停用触发连接撤销、重复释放幂等，以及已知桌面 OS 不被失败刷新降级。浏览器完成账号、组、授权和 Guest 身份四个页签的创建/编辑/成员/键盘操作；1280px 与 320px、中英文及明暗主题没有页面根横向溢出。Linux 桌面只出现兼容的 AD/LDAP Profile，Windows 桌面只出现 Windows 或默认本地方案；直接绕过 UI 提交平台不匹配 Profile 返回 409。`make check`、Race、Clippy、镜像与 Kubernetes 检查均通过；19 个 Swift 测试包含 Connection Session 的鉴权 DELETE，发布构建继续通过自包含依赖与深度签名验证。真实 PVE 只读复核重新确认 VMID 9100/9110/9111 的 `l26`/`win10`/`win11` 类型，以及六节点 Intel GVT-g 档位容量，全程未启用 PVE 写操作。随后在明确的 Debian 验收机 VM 158 上用一次性数据库执行 opt-in Guest 身份生命周期：QGA 创建稳定的每用户账号，连续两次描述符轮换密码，旧连接自动撤销，首次与重复释放均返回 204，测试用户清理后 VM 保持原运行状态。该代码阶段当时没有外部目录证据，后续 Samba AD 直接入域结果见下一条。

2026-09-05 SSSD AD 直接入域验收：复用 PVE VMID 9300 为 Samba 4.17 AD DC，并从 Debian 12 Cloud-Init 模板 901 完整克隆独立客户端 VMID 9301。客户端通过 VC Workspace 生成的 `linux_sssd_ad` 计划和 PVE QGA 完成首次入域与无 Go 缓存的幂等重应用；验证 NSS 用户/组、Alice PAM 登录与自动 Home、Bob 的有效 Kerberos 凭据但允许组拒绝、域控撤销 Alice 后拒绝以及恢复后重新允许。首次失败定位到控制面把 PVE REST 的原始 `input-data` 提前 Base64；重启复验又定位到 DHCP DNS 覆盖域 DNS并令 SSSD 转为离线缓存。修正 stdin 语义、64 KiB 上限和域 DNS 持久化后，客户端重启仍保持 SSSD Online，完整撤权链路再次通过。两台临时 VM 最终正常关机保留。M24 因正式 Debian 13、生产 AD/LDAP、FreeIPA、Windows 加域和 macOS 目录交互尚未完成，仍保持部分实测。

2026-09-05 密码、背景与 IaC 首批证明：本地用户自助改密和管理员重置已接入 Web/API/OpenAPI；隔离 PostgreSQL 验证当前 Web 会话保留、其他 Web/Native 会话与授权码撤销、桌面连接进入撤销队列，以及 OIDC 密码所有权拒绝。Debian/Windows 模板配方共享同一 3840×2160 品牌背景，5:4、16:9、21:9 填充裁剪均保留完整图形并通过 Packer syntax-only，但尚未据此把 M11 的动态策略闭环标为完成。控制面 API Token 的签发、到期、摘要存储、使用时间、撤销和禁止 Token 自我增殖已实现；Provider 的桌面授权生命周期、Import/漂移模型通过单元测试和 Go vet，真实 PostgreSQL HTTP 验证了 Token 鉴权、无 CSRF 写入、Web-only 路由隔离与禁止凭证自我增殖。Terraform 1.15.6 对真实控制面和数据库完成源码 Provider 安装、授权 `apply`、数据库落地核对、人为删除后的漂移检测与重建，以及 `destroy` 后归零。正式 Registry 发布、完整资源覆盖和 OpenTofu CLI 仍未宣称完成。

2026-09-05 M11.1 桌面级策略证明：第 18 个迁移在真实 PostgreSQL 16 隔离库通过默认值、变更递增、幂等保存和 applied revision 生命周期。Web 将本地权限、文本剪贴板、驱动器重定向和受管背景收敛到同一个原子保存对话框；中文/英文、明暗主题、1280px 与 320px 均通过浏览器检查，320px 根节点无横向溢出且交互热区至少 44px。控制面签发带版本、revision 和 SHA-256 的不可变会话快照；macOS ABI v3 将策略映射为 FreeRDP 剪贴板和驱动器参数。真实 PVE Debian 13 VM 158 先完成可恢复的 allow/deny xrdp 策略审计，再通过 Web→API→QGA→Guest 完整链路下发最终 `clipboard=true`、`drive=false`、`background=managed`，数据库 revision 3/3 为 applied，Guest 内配置、背景资产/helper 和 xrdp 服务经独立只读测试通过。首次实测发现 Debian 现有 `sesman.ini` 缺少 `EnableFuseMount`，实现已改为在正确 section 插入并对 section 缺失失败关闭。Windows 命令与 Swift 解码已有自动测试，但没有把它们扩大为 Windows/macOS 用户行为已实测；哈希也尚未冒充签名。

真实 PVE 写入测试必须显式设置 `VC_WORKSPACE_PVE_MUTATIONS_ENABLED=true`，并使用名称前缀 `vc-workspace-mvp-`。VM 147 是停止状态的控制面基线资源；VM 158 是 Debian 13 XFCE/RDP 完整数据面验收机；VM 9101、9112、9113 分别是 Debian 13、Windows 10、Windows 11 模板的停止状态完整克隆验收机。实时状态记录在 PVE 运维文档。
