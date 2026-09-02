# MVP 开发计划

更新日期：2026-09-02

当前阶段、优先级和 TODO 统一维护在 [项目状态与 TODO](status.md)。本文保留既有里程碑、实施约束和实机验收证据，不作为第二份任务清单。

## 目标

交付可运行的 VDI MVP：管理员可初始化本地账号、管理真实 PVE 桌面；用户可在 macOS 客户端登录、启动桌面并通过 RDP 进入 Linux 图形界面；AI Agent 可通过 MCP 独占租用桌面。

## 范围

MVP 包含：

- 本地管理员初始化、登录和注销。
- 单个 OIDC Provider 的 Authorization Code + PKCE 登录。
- 单 PVE 集群清单、模板发现、Clone、Start、Stop 与异步任务跟踪。
- 简洁 Web 控制台，不显示无法操作的能力标签或装饰性指标。
- macOS 客户端本地登录、Keychain Session、桌面列表、启动与 FreeRDP 建联；OIDC 保留但不阻塞数据面验收。
- Debian 13 XFCE、Windows 10/11 的 Packer 模板定义，以及跨平台 VC Workspace Guest Agent。
- 管理端可维护镜像来源、构建节点、模板 VMID、Guest Agent、构建状态和默认 GPU 档位。
- PVE GPU 实时发现；Intel GVT-g 按 mdev 类型和剩余实例调度，完整直通档位继续以 IOMMU 可用为启用前提。
- 控制面通过 Guest Agent 发现桌面 IP，并在签发 RDP 描述符时轮换 `vdi` 用户密码。
- 管理员可把每台桌面设为标准用户或本地管理员；控制面在建联前通过 QEMU Guest Agent 强制收敛 Linux sudo / Windows Administrators 组，未应用则拒绝连接。
- 管理员可创建、停用本地用户和 AI Agent，并分别显式分配桌面；Web、Native 与 MCP 只返回授权后的清单。
- 每个 Agent 使用独立可轮换凭证；MCP 的桌面清单、Agent Lease 生命周期和租约内电源控制都绑定到认证身份。
- PostgreSQL、审计事件、Docker Compose 和关键测试。

MVP 不包含：自研视频协议、跨网络 Session Gateway、Warm Pool、Guacamole、通用厂商 vGPU/SR-IOV 适配与自动 NUMA 优化、多租户、计费、Windows/Linux 完整客户端、生产级 Secret Manager。

## 里程碑

| ID | 交付物 | 状态 | 退出条件 |
|---|---|---|---|
| M0 | 仓库与契约 | 已完成 | Go/TS/Rust/Swift 工作区可解析；目录、设计语言、OpenAPI、Compose 和 CI 入口就位 |
| M1 | 本地身份 | 已完成 | 全新数据库只能通过 Setup Token 创建首个管理员；Web 与 Native Session 可撤销 |
| M2 | PVE 只读接入 | 已完成 | 页面显示真实集群、节点、存储、模板与 VM；凭证不进入响应或日志 |
| M3 | 受控桌面创建 | 已完成 | 从模板幂等 Clone；UPID 可追踪；重复请求不会产生第二台 VM |
| M4 | OIDC | 已实现，待接 IdP | Web/Native 使用 Code + PKCE、state 与 nonce；未提供真实 IdP 配置，尚未做外部登录验收 |
| M5 | macOS 客户端控制面 | 已完成 | SwiftUI 可构建；本地/OIDC 登录、Keychain Session 和真实桌面列表可用 |
| M6 | MCP/Agent Lease | 已完成 | AI 可领取、查询、启动、停止和释放专属桌面；同一桌面只允许一个活动租约 |
| M7 | MVP 验收 | 已完成 | 自动测试通过；真实 PVE 创建/启动/停止验证；文档无重复计划或失效入口 |
| M8 | macOS RDP 闭环 | 已完成 | 客户端可启动桌面、等待 Guest Agent、获取逐次轮换凭据的 RDP 描述符并打开实际 XFCE 桌面；VM 158 已完成 macOS FreeRDP 实机验收 |
| M9 | OS 模板与 GPU 基线 | 进行中 | 三个 OS 模板均完成克隆/QGA/Agent/RDP 验收；控制面已接入完整直通与 GVT-g 两条路径，6 个 PVE 9.2.10 节点已建 mediated Resource Mapping。Debian 13 的 V5_4 克隆、冷启动、i915/render node、Agent 和 RDP 端口已实机验收；生产启用前还需完成 Windows Guest Intel 驱动与实际 RDP 图形帧验收 |
| M10 | 镜像 Bootstrap WebUI | 已实现，待独立 VMID 实建验收 | 管理员可在 Web 配置 Debian 13、Windows 10/11 的节点、未占用 VMID、ISO/校验和、内网镜像源、存储、网络和硬件，使用一次性构建凭据启动固定 Packer 定义并观察持久化进度；完成后只进入 `testing`，不自动宣称可用 |
| M11 | 会话策略基础 | 已排期 | 建立统一策略、作用域和解析顺序；控制面签发带版本与哈希的策略快照；macOS、Debian 13 与 Windows Guest Agent 完成禁止剪贴板、磁盘/文件重定向以及受管桌面背景的端到端执行和审计 |
| M12 | 水印与扩展防泄漏 | 已排期 | 会话水印包含用户、设备、会话和时间；补齐打印、USB、音频和拖放通道控制、策略漂移告警及 Windows/Linux 客户端一致性验证 |
| M13 | Web 产品工作区闭环 | 已完成 | 管理端以“桌面 / 近期任务 / 基础设施 / 桌面镜像 / GPU 加速 / 访问控制 / 审计”七个真实工作区组织现有能力，普通用户只显示自己的桌面；服务端按 PVE `vc-vdi` 标签签发显式 `managed` 能力，不按名称猜测；1280px/320px、明暗主题、中英文和键盘操作完成浏览器验收 |
| M14 | macOS 客户端真实闭环 | 已完成 | 系统可识别且注册 `vc-vdi://` 回调的 `.app` 已完成本地登录、服务器绑定 Keychain Session、可搜索桌面卡片、启动/取消/重试、Guest Agent 等待、FreeRDP 异常回传、会话聚焦/断开、Dock 窗口恢复、退出清理和自适应网络；本机客户端已覆盖真实 PVE 冷启动和直接连接 |
| M15 | 统一桌面分配与客户端授权 | 已完成 | 管理员维护本地/OIDC 用户、独立 Agent 凭证、受管桌面注册表和用户/Agent 两类显式分配；Native、Web 与 MCP 在服务端统一鉴权并记录审计；停用身份、轮换凭证和移除 Agent 分配立即阻断访问或吊销租约 |
| M16 | 开源项目 Landing Page | 已完成 | `/` 用中英文和明暗主题说明 PVE、原生客户端、镜像/GPU 与 MCP 的当前实现，唯一主操作进入 `/console`，源码操作指向固定 GitHub 仓库；无假指标、路线图占位或未发布安装命令 |
| M17 | Web 到 macOS App Link | 已完成 | Web 桌面名称可通过 `vc-vdi://connect?vmid=<VMID>` 唤起签名后的客户端；链接不含秘密，客户端使用 Keychain Session 重新读取可见桌面后才连接，冷启动与未登录状态可延后消费；非法/重复参数被拒绝 |
| M18 | macOS 原生会话与自包含运行时 | 已完成 | 签名、自包含的 `.app` 已对真实 Debian 13 与 Windows 11 完成图像、窗口与远程画面缩放后键鼠、双向剪贴板、自适应网络、全屏、主动断开和退出清理验收；FreeRDP Mac 剪贴板修复以幂等源码补丁随构建应用 |
| M19 | Guest 本地权限策略 | 已完成 | Web 可配置标准用户/本地管理员；关机桌面保留 pending，运行中立即收敛，连接前强制复核；Debian 13 sudo/NOPASSWD 和中文 Windows 10/11 Administrators 组均完成真实 PVE 加入、移除、独立验证与状态恢复 |
| M20 | 管理审计闭环 | 已完成 | 安全事件追加写入并在持久化前脱敏；登录成功/失败、退出、配置、Guest 权限、PVE 任务结果、原生连接与 AI Lease 可按结果和用户/事件/目标搜索；管理员游标查询和 Web 详情完成实际数据库与浏览器验收 |
| M21 | 统一应用品牌标志 | 已完成 | 从六个不同方向完成尺寸对比，最终确定第六稿 `Agent Fabric`；Web、favicon、README 与 macOS Bundle/登录页使用同一品牌源，签名应用完成实际启动验收 |
| M22 | Kubernetes 服务端部署 | 已实现，待集群部署验收 | Web、控制面、远程 MCP 与 Bootstrap PostgreSQL 具备非特权容器、Kustomize、探针、Secret 分界和默认拒绝网络策略；公网只暴露 `443/TCP`，PVE、OIDC、RDP、QGA 与镜像构建端口有明确边界；待确定镜像仓库、域名/TLS、StorageClass 和命名空间后完成真实集群 rollout |

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
12. M15 已把分配关系和权限判定落在控制面：普通用户的 Web/Native 清单与连接操作、Agent 的清单/Lease/电源操作都使用同一桌面注册表和显式分配。组、桌面池和临时分配属于扩展调度模型，不混入当前个人持久桌面 MVP。
13. M16 只陈述仓库内已实现并有验收记录的能力。仓库首次 push 后再从真实 GitHub 状态补充许可证、版本和安装入口；公开部署前将语言切换升级为可索引的稳定语言路径与 `hreflang`。
14. M17 使用自定义 URL Scheme 覆盖任意自托管地址的 MVP，不把 VMID 链接当成授权票据。Universal Links 需要项目稳定域名、站点关联文件和发布签名中的 Associated Domains，不能承诺为未知部署域名动态生效。
15. M18 不接受“把 `sdl-freerdp` 隐藏在 App Bundle 中”作为内嵌完成。数据面必须与 SwiftUI 共享同一应用生命周期和 AppKit 视图树；发布包不得解析到 Homebrew、MacPorts 或 `/usr/local` 的第三方动态库。FreeRDP 更新必须同时固定版本和 SHA-256，并复测自适应策略及受管的带宽诊断参数。
16. M19 的本地权限是 Guest 内系统权限，不等同于应用白名单。标准用户阻止 sudo/Administrators 级安装；若要禁止 AppImage、便携 EXE 或用户目录安装，后续必须增加应用允许列表和执行控制，不能用文案扩大当前安全边界。
17. M20 当前提供应用层 append-only 查询与统一脱敏，不等同于法规级不可抵赖审计。独立写入角色、保留期、SIEM/WORM 和密码学防篡改在生产合规阶段单独验收。
18. M21 的唯一主标志和应用图标源位于 `assets/brand/`。业务页面不得自行派生替代图形，也不得把品牌标志当作功能图标；macOS ICNS 由仓库脚本从 1024px 品牌源图生成。
19. M22 的基线只让 Web Ingress 对外；控制面、MCP 和 PostgreSQL 必须保留集群内 Service。远程 MCP 调用方使用逐 Agent 签发的凭证，不能复用控制面内部 Token。镜像构建因 Packer 安装器反向 HTTP 端口尚未固定，在 Kubernetes 基线中保持关闭，后续以专用 Worker/Job 和受控路由实现，不使用 `hostNetwork` 扩大边界。

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

2026-09-02 App Link 证明：Web 对受管桌面生成仅含 VMID 的 `vc-vdi://connect` 链接；浏览器在中英文、明暗主题和 1280px/320px 下验证名称入口与可访问名称，320px 根宽度保持 320px，App 图标无需横向滚动即可看到。Swift 测试覆盖合法链接及密码、Token、重复参数、路径、片段和伪造协议拒绝；签名后的 `.app` 保留 `vc-vdi` 注册，macOS Launch Services 成功分发不存在的测试 VMID，客户端进程保持运行且未触发真实 PVE 动作。锁屏状态下未采集窗口截图。

2026-09-02 macOS 应用内会话证明：客户端不再启动 `sdl-freerdp` 子进程，而是通过仓库内 C ABI Bridge 在 SwiftUI 会话页挂载 FreeRDP 原生 `MRDPView`。构建脚本从固定 SHA-256 的 FreeRDP 3.31.0 与 OpenSSL 3.5.8 LTS 源码面向 macOS 14 生成约 12 MB 的自包含 `.app`；8 个内嵌动态库及应用通过深度签名，所有 Mach-O 均无构建机依赖或绝对 RPATH。签名 `.app` 已分别使用“弱网”连接运行中的 Debian 13 VM 9101、使用“自动”从停止状态启动并连接 Windows 11 VM 9113，验证真实图像帧、窗口缩放后的鼠标命中、应用切换后的键盘焦点、双向文本剪贴板、全屏往返、主动断开与退出清理。实测修复了内嵌视图连接后未恢复剪贴板轮询、点击不回收键盘焦点、Smart Sizing 视口尺寸未同步，以及 FreeRDP Mac 文本格式回退和后台线程写 pasteboard 的问题；M18 退出条件已满足。

2026-09-02 原生会话阶段证明：FreeRDP 3.31.0 的原生 Mac Client、仓库内 C ABI Bridge 与 SwiftUI `NSViewRepresentable` 已形成单进程会话生命周期；签名 `.app` 包含 FreeRDP/WinPR/OpenSSL 动态库和证书 NIB，体积约 11 MB，`codesign --verify --deep --strict` 与运行时 `dlopen` 通过，所有非系统依赖均使用 `@rpath`。无真实凭据的失败回收冒烟与后续 Debian/Windows 真实 PVE 图像帧验收均已完成；历史阶段记录不再作为未完成门槛。

2026-09-02 Guest 本地权限证明：第 11 个数据库迁移已在 PostgreSQL 17 实际应用。控制面 API 在运行中的 Debian 13 VM 158 返回 `local_admin`/`standard` 的 applied revision，并独立确认本地管理员可直接运行 `sudo -n`、切回标准用户后该提权失败；最终恢复为标准用户。Windows 实测暴露并修复了中文系统不能按英文 `BUILTIN\\Administrators` 查组的问题，最终统一通过 SID `S-1-5-32-544` 获取本地化组；VM 9113 与 VM 9112 完成管理员加入/移除验证，VM 9112 最终用正常 shutdown 恢复关机。PVE `guest-exec-status` 的瞬时超时采用 60 秒有界重试，QGA 未运行时策略保持失败/待应用且连接被拒绝。Web 在真实 PVE 七台受管桌面清单上完成中文深色 1280px、英文浅色 360px、键盘语义和零控制台错误验收；隔离测试数据库和会话已删除。

2026-09-02 管理审计证明：第 12 个迁移在隔离 PostgreSQL 17 数据库实际应用并确认三个查询索引；真实 API 验证成功/失败筛选、事件/用户/目标搜索、游标分页、未知账号目标保留、初始化 Token 不落库，以及普通用户读取返回 403 并追加 `audit.read_denied`。浏览器使用真实事件完成中文深色、中文浅色和英文浅色列表、筛选、详情与 30→36 条继续加载验证；普通用户重新加载后不显示审计入口。Go 全量测试、Web TypeScript 检查、16 个 Vitest 测试和生产构建通过，隔离数据库及会话在验收后删除。

2026-09-02 应用标志证明：以 16px、32px、64px 和展示尺寸比较六个不同标志方向，按最终选择切换为第六稿 `Agent Fabric`，并生成 SVG、1024px PNG 和包含 16–1024px 表示的 ICNS。真实浏览器在 Landing 与登录页完成浅色/深色检查；最新签名 `VC Workspace.app` 在退出旧进程后从构建产物实际启动，SwiftUI 登录页读取系统应用图标并显示新标志。Web 类型检查、测试与生产构建、Swift 测试、ICNS 反向解包及应用深度签名检查均通过。

2026-09-02 Kubernetes 部署证明：三个生产容器镜像在本机完整构建；使用隔离 Docker 网络让 Web、控制面和远程 MCP 以容器形态实际运行，验证 Web 同源 `/api/v1/ready`、鉴权后的 `/mcp` 工具目录、未鉴权 `401` 与浏览器 Origin `403`。`make k8s-check` 完成 Kustomize 渲染和客户端 dry-run。该记录证明镜像和清单可解析，不替代目标集群的 Ingress、TLS、持久卷、NetworkPolicy 与 rollout 验收。

2026-09-02 macOS 桌面库证明：签名 `VC Workspace.app` 以隔离控制面和 PostgreSQL 17 会话加载 VM 9101/9113 的真实 PVE 状态，验证桌面卡片在最小、默认和宽窗口下自适应，单卡不拉伸，名称/VMID/节点/状态搜索与搜索无结果恢复动作通过，且界面不暴露带宽调参。同一 App 从停止状态启动 VM 9101 并在 Guest Agent/桌面服务就绪后连接，验证默认→宽→最小窗口、原生全屏往返、各尺寸画面填充及缩放后鼠标命中/键盘输入。过程中修复 PVE 冷启动状态瞬时回退导致连接过早失败，以及全屏按钮可访问名称不跟随窗口状态的问题。主动断开、本地退出、VM 9101 关机、隔离数据库/会话清理和客户端控制面偏好恢复均已完成。

真实 PVE 写入测试必须显式设置 `VC_VDI_PVE_MUTATIONS_ENABLED=true`，并使用名称前缀 `vc-vdi-mvp-`。VM 147 是停止状态的控制面基线资源；VM 158 是 Debian 13 XFCE/RDP 完整数据面验收机；VM 9101、9112、9113 分别是 Debian 13、Windows 10、Windows 11 模板的停止状态完整克隆验收机。实时状态记录在 PVE 运维文档。
