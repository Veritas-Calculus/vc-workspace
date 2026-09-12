# 项目状态与 TODO

更新日期：2026-09-12

本文是项目“现在做到哪里、接下来做什么”的唯一当前事实源。[MVP 开发计划](mvp.md)保留里程碑、实施顺序和实机证据；架构文档只描述已经采用的边界，不重复维护任务清单。

## 当前结论

VC Workspace 已完成面向人的核心纵向闭环：管理员可以初始化本地账号、接入真实 PVE、创建和分配受管桌面、管理 Guest 本地管理员权限并查询审计；用户可以通过签名、自包含的 macOS 客户端登录、搜索桌面、启动虚拟机并在应用内连接 Debian 13 或 Windows 桌面。

项目当前处于“MVP 回归修复与关键扩展补齐中”，还不是生产就绪版本。2026-09-05 全面 review 发现授权撤销、每用户 Guest 与 MCP Helper 衔接等会破坏既有闭环的回归；历史里程碑验收只说明当时版本，不再用旧的完成数量表示当前完成度。

已按部署方 2026-09-06 的最新要求建立总目标：完成下表全部尚未闭环的 P0–P2 TODO，并修复实施和实测中发现的回归。每个批次交付不等于总目标完成；仅在所有退出条件满足后关闭总目标。执行顺序是：①授权/会话、数据库、构建与 Web 恢复；②每用户 MCP、企业身份、策略和镜像闭环；③GPU、弱网/Gateway、桌面客户端；④IaC、审计、Kubernetes 和发布。每一项必须具备对应测试与所需实机证据；环境和发布身份不足时明确保留为未完成，不将清单或代码当作验收。

当前最重要的缺口是：会话策略已完成桌面级纵向切片和 Debian 13 实机收敛，但多作用域解析、签名/Guest 回报、Windows/macOS 行为实测及动态水印尚未完成；OIDC 与 SSSD LDAP 已有本机及 PVE amd64 VM 托管的 Docker 真实服务证据，Debian 12 独立 PVE Guest 也已完成 Samba AD 直接入域和在线撤权，但生产 IdP/LDAP、正式 Debian 13 的目录身份、FreeIPA/Windows 目录、Windows 镜像实建和 GVT-g 仍缺目标环境验收；macOS 尚未提供 AD/LDAP 密码、Kerberos 或 SSSD OIDC 设备码交互；infra Kubernetes 已完成首次部署、基础验收及持续 Mac 桌面连接下的内部叶证书轮换，仍需运维故障恢复和升级恢复。Guest 光标缓存补丁已进入实际 Web 构建的新 Debian 模板，并在独立克隆上通过 Mac 全屏往返和重连；新模板首次连接与输入异常仍待归因，暂不标记 ready。Guest 防绕过、Windows 网关、WAN 负载、Windows/Linux 原生客户端和生产发布链尚未完成。

## 能力状态

| 领域 | 状态 | 当前能力 | 未闭环部分 |
|---|---|---|---|
| Go 控制面与 PVE | 回归修复中 | 真实清单、受管桌面、按主体/路径隔离的幂等 Clone/启停、Job 收敛、QGA 状态与连接凭据签发 | 撤权完整边界、多副本 Job leader election 与跨网络 Session Gateway |
| Web 与 Landing | 已完成 | 七个管理工作区、普通用户桌面入口、中英文、明暗主题、响应式布局和 App Link | 正式下载入口需等待首个 Release |
| 本地身份与授权 | 回归修复中 | Setup Token、本地用户、自助改密/管理员重置、Web/Native Session、Personal 唯一 Owner、Shared 用户/组授权、Agent 独立分配，以及每用户 Guest 本地账号和 8 小时 Connection Session | REVIEW-02 的 Guest 会话撤权边界与实机验证；登录限速等生产加固 |
| OIDC | Docker/PVE VM 集成已通过 | Web/Native Authorization Code + PKCE、state、nonce、一次性 Native code，以及可配置 Group Claim 的事务性组同步和组授权；真实 Keycloak Web 首次/重复登录、组撤权收敛及 Native Code 兑换/重放拒绝/注销通过 | 生产 IdP、真实 macOS `.app` Open URL、拒绝、过期和 JWKS 轮换验收 |
| Guest OS 目录身份 | AD/LDAP 部分实测 | Debian 13 SSSD AD/FreeIPA/LDAP、Windows AD 在线加域；OpenLDAP StartTLS 实验通过；独立 Debian 12 PVE Guest 已通过 Samba AD 直接入域、SSSD/NSS/PAM/Kerberos、自动 Home、允许组拒绝与在线撤权/恢复 | 正式 Debian 13 模板、生产 AD/LDAP、FreeIPA 与 Windows 10/11 加域和登录；macOS 目录凭据/Kerberos/设备码交互；默认 FreeRDP 构建尚无 AAD |
| macOS 客户端 | 核心闭环已实测，原生外观改进中 | Keychain、桌面卡片与搜索、分阶段 Pre-session、取消/断开/失败恢复与原位重连、内嵌 FreeRDP、Retina 2× Guest 动态分辨率、Linux 建联 DPI 联动、全屏、剪贴板和 Dock 窗口恢复 | MAC-03 新界面/动效与键盘实机回归；Windows 端动态分辨率/DPI；Linux 活跃会话跨 1×/2× 屏幕 DPI 同步；发布签名、Notarization、安装包与自动更新 |
| OS 模板 | 进行中 | Debian 13 正式模板；Windows 10/11 技术模板及 Guest Agent/QGA/RDP 管线 | 从 Web 以独立 VMID 完整实建；Windows 使用授权且仍受支持的生产介质 |
| GPU | Linux 人工会话硬件渲染已实测 | PVE PCI/mdev 发现、Resource Mapping、Intel GVT-g 调度，以及 Debian 13 RDP 的 Intel HD 530/Glamor 硬件渲染和 5K 动态分辨率 | 编码与无 GPU 性能对照；Agent/目录用户访问策略；正式模板实建；Windows Intel 驱动与图形帧；独显实机透传 |
| 镜像 Bootstrap | Debian Web 实建与 Mac 连接已实测，未全部验收 | Web 参数、内网镜像源、Packer allowlist、一次性秘密和脱敏进度；独立 Debian 13 模板与完整克隆、首次启动、Agent/QGA、Mac RDP/全屏/重连 | 首次传输失败及输入问题归因、Web 验证克隆入口、Windows 实建与最终 ready；Kubernetes Builder Worker |
| Guest 本地权限 | 已完成 | Debian sudo 与 Windows Administrators 的标准用户/本地管理员切换，连接前强制复核 | 应用允许列表、便携程序与用户目录执行控制 |
| 应用层审计 | 已完成 | 追加写入、脱敏、结果筛选、搜索、游标分页和管理端详情 | 保留期、SIEM/WORM、哈希链、可信代理与合规级防篡改 |
| MCP Agent Lease | 已完成 | 每 Agent 凭证、显式分配、独占租约、TTL、`control_epoch`、租约内启停，以及管理端当前 VM/到期时间状态 | 无 |
| MCP Computer Use | Linux 完整 MCP 链路已实测，跨端未闭环 | 五类工具、跨副本锁/撤销队列；独立 Agent OS 账号及 V2 UID/会话/实例绑定；真实 SDK HTTP→控制面→PVE→无人值守 xrdp 的图片、坐标、AT-SPI、输入、凭证轮换、重新领取和撤权通过 | Windows V2、Native 显式观察/接管同一现场、异常网络和正式模板实建仍待完成，见 REVIEW-04；当前拒绝 Windows/未升级 Guest，不回退共享 `vdi` |
| 会话管控 | 进行中 | 每桌面文本剪贴板、驱动器重定向、受管背景、revision/哈希快照、Web 原子保存、macOS 参数执行和建联前 fail-closed 已实现；Debian 13 VM 158 的真实 Web→Guest 下发通过 | 多作用域解析、密码学签名与 Guest 摘要回报；Windows 和 macOS 实际行为；M12 水印与更多设备通道；新模板实建验收 |
| IaC | 已实现首批 | 同一控制面 API、最长 90 天且只显示一次的管理员 API 凭证、`vcworkspace_desktop_assignment` 资源、`vcworkspace_desktops` 数据源、Import 与漂移读取；Terraform 真实 apply/漂移/destroy 已通过 | 更多平台资源、Token Scope/紧急吊销、正式 Provider 签名发布，以及 OpenTofu CLI 验收 |
| Kubernetes | infra 已部署，基础验收通过 | 私有 amd64 Digest、cert-manager HTTPS、Web/API/MCP/Gateway、mTLS 控制接口、Calico 隔离、Ceph PVC、一次备份恢复与 Mac 经网关连接 Debian 桌面 | 证书/凭据轮换、持续加密备份/PITR、升级回滚、网关稳定性/防绕过及 Builder Worker |
| Windows/Linux 客户端 | 未开始 | 仅保留目录和跨平台 Session Core 边界 | WinUI 与 GTK4 客户端的登录、桌面库、数据面和发布链 |
| 开源发布 | 进行中 | 许可证定为 Apache-2.0，`LICENSE`、`CONTRIBUTING.md` 与 `SECURITY.md` 就位；Landing 已指向固定仓库地址；基线 8 个 commit 已 push 到 `Veritas-Calculus/vc-workspace` 的 `main` | 远端 GitHub Actions 首次运行的 `macos` 作业失败，也没有 Release；2026-09-03 起的工作树尚未提交 |

## TODO

优先级含义：P0 是下一阶段闭环或公开发布的阻塞项；P1 是生产部署和安全体验所需；P2 是 MVP 后扩展。任务只有在“完成条件”全部满足后才能改为已完成。

| ID | 优先级 | 状态 | 任务 | 完成条件 |
|---|---|---|---|---|
| REVIEW-01 | P0 | 已修复并自动验证 | 阻止幂等缓存越权与错配 | Native 当前授权先于重放；五类 Job 写入按主体/方法/路径隔离；相同 JSON 重放、不同内容 409、并发冲突和旧越权复现回归通过 |
| REVIEW-02 | P0 | Debian 默认 HTTP/macOS 撤权与注销已实测，跨端未闭环 | 完整撤销已分配的 Guest 桌面访问 | 独立撤销队列/有效期、在途凭据复验、revision 防旧任务和真库回归已有证据；默认控制面与 macOS 发布版已通过保留应用重连、在线撤分配、恢复授权及注销清空保留桌面，独立检查固定 UID/PAM/Xorg 节点。剩余 Windows WTS、多副本完整故障矩阵；网络分区断线依赖 NET-01/Guest 本地有效期。本轮撤分配约 18–20 秒、注销约 48 秒完成队列收敛，不承诺即时撤权 SLA |
| REVIEW-03 | P0 | 已修复并自动验证 | 防止旧撤销任务误伤新连接 | 持单桌面锁重读连接，已关闭旧快照不再调用 QGA；旧 Session 清理后新 Session 密码不被旋转的 PostgreSQL + 假 PVE 回归通过 |
| REVIEW-04 | P0 | 进行中 | MCP Helper 跟随每用户交互会话 | Linux 独立 Agent 账号与完整 SDK MCP 链路已实测。024–026 保留固定 UID/SID，不随撤权清空或重绑。Windows 每用户 Helper、本地到期回收与固定 SID 的结构化 SAM/WTS 观察已实现并有原生/交互证据。新增独立无界面 RDP Worker 与 Go 监管；Windows 10 已使用它通过双用户完整交互及到期注销，Linux ARM64 容器安全回归通过。默认 Windows MCP 仍关闭，剩余安装/服务恢复、无需人工处理首次登录的模板、Broker 账号/Lease/进程所有权、REVIEW-12 的 Guest 写入版本保护与默认执行端接线；另剩 Native 显式观察/接管同一现场、升级/新模板实建、多会话和异常网络矩阵。逐批结果见最近验证，不以实验 Guest 协议或单个连接组件代替整体 MCP 验收 |
| REVIEW-05 | P0 | 已修复并自动验证 | 策略升级强制重新收敛 | 新增 019 迁移，不修改已发布 018；旧 applied revision 递增为 pending，重复迁移不重复递增；升级回归通过 |
| REVIEW-06 | P0 | 已修复并本地构建 | 修复控制面容器缺少嵌入资源 | Dockerfile 包含品牌资源，本地实际 Docker build 成功；CI 新增镜像构建门禁，远端执行由 RELEASE-01 验收 |
| REVIEW-07 | P0 | 已修复并自动验证 | 并发迁移与测试数据库隔离 | 迁移在专用连接持数据库锁，释放失败关闭连接；两个 Store 同时首次迁移和重复迁移通过；集成测试使用独立 schema，CI 显式运行 PostgreSQL 与 race 测试 |
| REVIEW-08 | P1 | 已修复并浏览器验证 | Web 会话失效恢复 | 受保护 API 的 401 清空页面后返回登录，匿名/错误密码不跳转，403 不误判过期；中英文、明暗主题、窄屏及键盘重新登录通过 |
| REVIEW-09 | P1 | 已修复并浏览器验证 | Web 任务轮询恢复 | 单在途读取、15 秒超时、临时失败退避与重试入口、权限/不存在终止、清理后不更新；浏览器模拟 502 后恢复同一 Job，确认启停只提交一次 |
| REVIEW-10 | P0 | Linux 默认链路已修复并实测，Windows 基础已验证 | Guest 授权乱序、发布中断与撤权版本保护 | 025 增加 VM 持久化 epoch，撤销队列保留 closing epoch，关闭租约不能复活；Linux 改为 stdin 独立输入、Guest 排他锁与单调比较提交，新授权文件隔离旧发布者。真实 PostgreSQL 升级/重领/失败清理/旧队列版本、Linux 延迟旧 active/revoke、进程中断恢复/旧协议迁移/Helper 重启和最终 PVE MCP 回归通过。Windows 实验栅栏及多用户原子提交已在上批原生验证；默认 Windows 接线仍随 REVIEW-04 验收，不以此宣称跨端全部闭环 |
| REVIEW-11 | P0 | 已修复并跨 OS 实测 | 拒绝 QGA 异常退出及不完整回执 | 缺失 exitcode、signal/Windows 异常、截断或无效元数据不再变成零退出码成功；不重发有副作用的命令。15 个状态矩阵子用例、真实 Linux 正常/非零/自身信号退出、Windows 正常/异常式退出均通过；交互验收清理增加明确回执和独立精确身份残留检查 |
| REVIEW-12 | P0 | Linux 默认链路已实测，Windows 资料保护修复中 | Guest 账号生命周期的迟到写入隔离 | 027/028 固定身份、单调版本、原子 Connection 提交及双副本恢复已有自动或真库证据。隔离 Debian 的 PAM/root 孤儿、服务/到期/开机回收、冻结 Xorg 与节点清理，以及默认 HTTP→macOS 保留应用重连、撤权、恢复授权和注销已实测。Windows Native 独立 SAM/WTS 执行端及 10/11 真实账号密码、到期和中断恢复测试通过；仍须默认 Broker、真实桌面保留与登录/进程创建边界、DPAPI/凭据库跨轮换与新登录的数据完整性。已实测拒绝已有 Profile 的不安全重置；禁用/过期改密及密钥迁移候选仍失败，不能上线。其他剩余项包括旧账号归属与进程排空/升级、PID 1/systemd 异常与在途 logind 重启、其他登录入口和全链路故障矩阵。逐批边界见最近验证，不以局部通过关闭整体项 |
| RELEASE-01 | P0 | 远端 CI 修复中 | 建立开源仓库基线 | 许可证已定为 Apache-2.0，`LICENSE`、`CONTRIBUTING.md`、`SECURITY.md` 已完成；版权归属为 `Veritas-Calculus`，Go 模块路径已对齐仓库地址；基线已 push 到远端 `main`。剩余条件是让 GitHub Actions 在远端通过：首次运行 `core` 与两个 `guest-agent-artifacts` 作业通过，`macos` 作业在 `build-app.sh` 失败。因为 `build-native-rdp.sh` 与 `verify-app.sh` 要求 `rg`，而 GitHub macOS Runner 不预装 ripgrep，两个脚本已改用 `grep`；另修复 `internal/pve/principal_test.go` 复制 `sync.Mutex` 导致的 `go vet` 失败。两项修复推送后，远端 `core`、`macos`、`linux-computer-sessions`、`headless-session-worker` 四个作业全部通过；剩余 `guest-agent-artifacts (windows-latest)` 的 Windows 会话回归失败，见最近验证 |
| AI-01 | P0 | Linux 已复验，跨端修复中 | MCP Computer Use 最小纵向切片 | Debian 13 已按独立 Agent 身份完成 SDK MCP HTTP→PVE→Guest 实测；Windows 10/11 新的独立 SID/真实登录 Helper 已通过顺序双用户图形与输入断言，仍不是默认 SDK MCP/无人值守登录证据。REVIEW-04 的 Windows 默认链路、新模板与显式人工观察/接管未完成前，不把整体跨端能力视为闭环 |
| POLICY-01 | P0 | 进行中 | 完成 M11 会话策略基础 | 每桌面数据模型、Web、哈希快照、macOS/Guest 执行端、审计、Debian 13 实机与未应用拒绝建联已完成；仍需多作用域解析、密码学签名/Guest 回报，以及剪贴板、文件/磁盘重定向和受管背景在 Windows 与 macOS 用户路径实测 |
| IMAGE-01 | P0 | Debian 实建与 Mac 切片已通过，仍在验证 | 通过 Web 完成模板 Bootstrap 闭环 | Web 创建 Debian 13 VM9202、完整克隆 VM9203、首次启动/Agent/QGA 与 Mac 全屏/重连已实测；克隆使用受限 PVE API，Web 尚缺从 testing 状态创建验证克隆的入口。首次 TCP 失败及输入异常、Windows 11 实建与最终 ready 未完成；Windows 10 是否生产启用由介质支持与授权决定 |
| GPU-01 | P0 | 待实测 | 完成 Windows Intel GVT-g 验收 | Windows Guest 安装匹配驱动，确认设备与硬件加速状态，并从 macOS 客户端收到实际加速桌面图形帧；失败时模板不得默认启用该档位 |
| GPU-02 | P1 | Debian 新模板 Mac 硬件渲染已实测，编码未完成 | Linux GVT-g 渲染与编码链路验收 | VM160已有受管用户权限修复前后的 llvmpipe/Intel HD530 对照；Web实建模板9202→产品新克隆9205也已通过Mac登录、Glamor/iris渲染、5K全屏往返与重连，未手工补Guest GPU配置。当前为CPU RFX编解码，不是GPU/H.264编码；下一步验证新版xrdp/xorgxrdp与自包含H.264解码、许可证和RFX回退。仍需Agent/目录用户GPU策略、模板ready晋级及升级矩阵，以及同一负载无GPU/GVT-g的1080p/1920×1200/5K帧率、输入延迟、CPU和带宽，不修改运行中VM158或宿主驱动 |
| WATERMARK-01 | P1 | 待开发 | 完成 M12 水印与扩展防泄漏 | 水印覆盖用户/设备/会话/时间，并通过缩放、全屏、重连、多显示器和 DPI 验收；补齐打印、USB、音频、拖放策略及漂移告警 |
| DEPLOY-01 | P1 | infra 已部署，运维验收未闭环 | 在目标 Kubernetes 集群部署 | `ws.infra.plz.ac` / `vc-workspace` 已用私有 amd64 Digest、cert-manager 与 Ceph 实际 rollout；组件健康、网关 mTLS、部分正/负网络策略、数据库 Pod 替换保留及独立备份恢复通过。签发/活动材料分离、双指纹客户端推广、旧 Keep-Alive 拒绝和两端服务叶证书续期已在持续 Mac→Gateway→Debian 桌面连接下实测；同一连接/Guest 会话、Pod UID 和零重启保持不变。剩余强制中断/过期身份应急恢复、后台推广告警、CA 实机轮换、90 天 PVE/Harbor 凭据轮换告警、持续加密远端备份/PITR、跨版本升级回滚与故障矩阵；不能据局部运维验收宣称生产就绪 |
| OIDC-01 | P1 | 部分实测 | 接入真实 OIDC Provider | Docker Keycloak 已在本机和 PVE amd64 VM 通过 Web 首次绑定、重复登录、组增删、本地管理员共存，以及 Native Code 兑换、重放拒绝和注销；剩余生产 issuer/client/claims、真实 macOS `.app` Open URL、拒绝、过期和 JWKS 轮换测试 |
| IDENTITY-01 | P0 | 回归修复中 | 每用户 Guest 本地身份与短期连接 | 既有映射、分配与两次签发证据保留；必须同时满足 REVIEW-02/03 的完整撤权、Guest 注销、并发与失败重试退出条件，才恢复完成状态 |
| IDENTITY-02 | P1 | 部分实测 | 验收 Linux/Windows 企业目录 | Docker OpenLDAP 已在本机及 PVE amd64 VM 托管环境验证 Debian 13 SSSD StartTLS、PAM、准入拒绝和自动 Home；独立 Debian 12 PVE Guest 已通过 Samba AD 直接入域、Kerberos、允许组登录、在线撤权/恢复和幂等重应用；剩余正式 Debian 13 模板、生产 AD/LDAP 的离线缓存与撤权、FreeIPA，以及 Windows 10/11 加域、重启、Remote Desktop Users 和 macOS 安全目录登录交互 |
| IDENTITY-03 | P2 | 实验实现 | 验收直接 IdP Guest 登录 | 用受支持 Provider 验证 Debian `sssd-idp` 设备授权；重编并审计启用 AAD 的 FreeRDP 后验证 Entra Tenant/主机/用户；未通过时两个服务端能力开关保持关闭 |
| IAC-01 | P1 | 已实现首批 | 完成平台资源 IaC | 已实现有期限的 IaC API 凭证、同 API/权限/审计链路、桌面授权 CRUD/Import/漂移读取和受管桌面数据源，并完成 Terraform 真实生命周期验收；剩余桌面生命周期、策略、组/成员、Identity Profile、镜像/GPU 资源，细粒度 Token Scope、正式签名发布及 OpenTofu CLI 验收 |
| NET-01 | P1 | Debian 网关桌面已实测，稳定性/WAN 未闭环 | 弱网基准与跨网络 Session Gateway | 票据/短租约/WSS/mTLS、Linux Broker、原生正/误证书互操作与 infra 网络隔离已有分项证据。正式签名 `.app` 经真实 Broker/Gateway 的 5K 桌面、保留会话重连、在线撤权和内部叶证书轮换已实测；时钟领先误拒绝已修复。同连接 120 MiB 往返、背压及 60 秒空闲通过，但不替代桌面/WAN。缩放后的 Cached Pointer 旧引用已由 Guest 补丁修复，隔离机上的 8 次显示尺寸变化、实际输入、约 16 分钟持续连接及重连通过；尚未安装到默认模板。仍须模板推广/长期回归、不可绕过的 Guest 网络规则、Windows 适配，以及 WAN 撤权与持续负载首帧/交互延迟/资源分布；原本机部署不启用新路由，不公开 Guest 3389 |
| RELEASE-02 | P1 | 待开发 | macOS 可分发发布链 | 使用正式 Developer ID 签名并 Notarize，生成可校验安装包、SBOM/第三方许可证、版本和下载链接；Web 仅在真实 Release 存在后显示安装兜底 |
| MAC-01 | P1 | 待实测 | Windows Guest 动态显示回归 | 使用 Windows 11 验收机覆盖默认窗口、系统缩放、全屏、退出全屏和 Windows DPI；从 Guest 内确认实际分辨率，并在每次变化后验证鼠标、键盘和剪贴板 |
| MAC-02 | P1 | 待开发 | Linux 活跃会话跨屏 DPI 同步 | 已连接会话在 1× 与 2× 显示器之间移动时，无需重连即可同步 XFCE DPI；覆盖两个方向、窗口/全屏往返和键鼠坐标验收 |
| MAC-03 | P1 | 生命周期/冷登录/全屏切片已实测，完整体验矩阵未完成 | 完善 SwiftUI 外观与交互 | 已修复并回归任务/会话所有权、过早终止及旧回调、首帧前后取消、冷登录显示请求收敛；首次握手故障和已连接断网可恢复同一 OS 会话。全屏显式隐藏系统工具栏、退出显式恢复。infra 全屏后断线已实机归因为 Guest 光标缓存旧引用，错误分类和 Guest 补丁均有真实回归；隔离机通过三轮全屏往返、拖动缩小/系统最大化、原生菜单断开/保留桌面重连及撤权提示，正式模板推广与长期回归仍待完成。另需全屏坐标自动化异常归因、剪贴板、完整 Tab、悬停/按压、减少动态效果/后台停动与 Dock 点击恢复；历史首次失败缺少日志，不能断言全部同因 |
| AUDIT-01 | P1 | 待开发 | 生产审计与认证加固 | 配置可信代理、登录/初始化限速、保留期、导出/SIEM、独立写入角色与 WORM 或哈希链；完成备份恢复和权限测试 |
| BUILDER-01 | P1 | 待设计 | 拆分可恢复 Image Builder Worker | 固定安装器 HTTP 端口范围，使用专用 Job/Worker 与最小权限 PVE Token；控制面重启不丢失状态，Kubernetes 不使用 `hostNetwork` |
| CLIENT-01 | P2 | 未开始 | Windows 原生客户端 | WinUI 登录、凭证存储、桌面卡片、App Link、RDP 数据面、会话策略和安装签名形成闭环 |
| CLIENT-02 | P2 | 未开始 | Linux 原生客户端 | GTK4 登录、Secret Service、桌面卡片、RDP 数据面、会话策略及主流发行版打包形成闭环 |
| APPCTRL-01 | P2 | 未开始 | Guest 应用执行控制 | 在 sudo/Administrators 之外增加允许列表或执行控制，明确覆盖 AppImage、便携 EXE 和用户目录安装，不扩大现有权限模式的安全承诺 |
| PROTOCOL-01 | P2 | 待研究 | 高性能串流协议评估 | 用同一组画质、输入、音频、弱网、GPU 和运维指标比较 RDP、WebRTC/QUIC 等候选；有数据后再决定是否替换或并存 |

## 需要用户或环境提供的决定

- Kubernetes 首次部署环境已落实；仍需远端加密备份位置、保留期及 RPO/RTO。当前只有受保护的本地备份和独立恢复检查。
- 用于验收的 OIDC Provider、Client Registration 与回调域名。
- Windows 10/11 的授权介质和生产支持策略，尤其是已经结束普通支持的 Windows 10 22H2。
- macOS 发布所用 Apple Developer Team、Developer ID、Notarization 和更新渠道。
- Session Gateway 已在 infra 的域名/Ingress 上部署；后续互联网可达范围、Guest 防绕过与 WAN 故障/负载验收仍需明确，不将内网域名证据当作互联网验收。

## 最近验证

- 2026-09-12（远端 CI 首次完整执行与 Windows 会话门禁）：推送 `b637a52`–`a0abda1` 后远端运行 `34679990491`。`core`（14 分 1 秒）、`macos`（7 分 21 秒）、`linux-computer-sessions`（5 分 10 秒）、`headless-session-worker`（1 分 41 秒）通过，`rg` 与 `go vet` 两项修复在远端生效，`make macos-gateway-check` 也在 CI 通过。`guest-agent-artifacts (windows-latest)` 失败，其 ubuntu 矩阵项因 fail-fast 被取消，不是独立失败。

  Windows 失败的是本轮新增的 `cargo test --locked -p vc-workspace-windows-session`：36 通过、18 失败、40 忽略。18 条中 17 条报 `invalid pipe account SID`，1 条报 `authority updates require LocalSystem Session 0`。因为 `valid_account_sid` 只接受 `S-1-5-18` 或 `S-1-5-21-…` 且 RID ≥ 1000，而 GitHub `windows-latest` 以 `runneradmin` 运行，其 SID 不满足该规则，所以绑定 Helper 管道的用例无法执行。这是门禁对运行账号的前提假设，不是产品缺陷；该步骤在 2026-09-02 基线中不存在，从未在 Runner 上执行过。

  处理方式是让 CI 提供合格账号而不是缩小断言：先以 Runner 账号 `--no-run` 构建测试二进制并复制到工作区外的暂存目录，再创建一次性本地账号（`New-LocalUser` 分配 RID ≥ 1000，加入 Administrators 以保留原本通过的注册表和 worker 用例），通过计划任务以 `/RL HIGHEST` 运行，避免 `Start-Process -Credential` 给本地管理员的 UAC 过滤令牌。`require_system` 只校验 SID 是否为 `S-1-5-18`，因此该条单独以 SYSTEM 运行。测试结束后删除账号和暂存目录。该步骤只能由远端 Windows Runner 验证，本机无 Windows 环境。

- 2026-09-12（Gateway 隧道被客户端代理设置劫持）：`make macos-gateway-check` 的 `bundledNativeBridgePinsTheGuestInsideWSS` 在设有 `HTTP_PROXY`/`HTTPS_PROXY` 的机器上失败：原生传输停在 `state=0`，`transport.error()` 为空，fixture 回报 `bytes=0 guest_tls=false`，Guest 从未收到 X.224 协商请求。

  根因是 FreeRDP 的代理判定与自带描述符冲突。`transport_connect` 先调用 `proxy_prepare`，该函数在 `FreeRDP_ProxyType` 为 `PROXY_TYPE_NONE` 时读取 `https_proxy`/`HTTPS_PROXY`；测试目标 `192.0.2.1` 不在 `NO_PROXY` 内，于是判定为代理连接。随后 `transport_connect_layer` 经桥接的 `TCPConnect` 回调取回已经连到 Guest 的隧道描述符，`transport_connect` 却按代理路径在这条隧道上写出 HTTP CONNECT 前导。Guest 只等 X.224，双方各自阻塞到 10 秒超时。

  修复：`pinned` 分支显式设置 `FreeRDP_ProxyType = PROXY_TYPE_IGNORE`，`proxy_prepare` 随即直接返回 FALSE。这与该分支既有的 `GatewayEnabled=FALSE`、`AutoReconnectionEnabled=FALSE` 一致——描述符已经连通，不允许任何再拨号或前导。直连 RDP 路径不改，客户端处于代理后需要拨号的场景保持原行为。

  保留 `HTTP_PROXY`/`HTTPS_PROXY` 重建并复跑：`make macos-gateway-check` 67 个测试/3 个 suite 通过，`make macos-rdp-reactivation-check` 与 `verify-app.sh` 通过。此前把该失败判断为 2026-09-07 之后的回归并不成立，当时的 59 个测试/2 个 suite 记录与本轮无冲突；GitHub Runner 默认不设代理变量，远端 `macos` 作业此前也从未执行到这一步。

- 2026-09-12（远端 CI 首次失败归因与本地门禁复核）：基线 8 个 commit 已在远端 `main`，GitHub Actions 首次运行的 `core`、`guest-agent-artifacts`（Linux/Windows）通过，`macos` 作业在 `./scripts/build-app.sh release` 退出 1，日志为 `native RDP build requires rg`。因为 GitHub macOS Runner 不预装 ripgrep，`build-native-rdp.sh` 与 `verify-app.sh` 已去掉 `rg` 依赖改用 `grep`：三处字面匹配用 `grep -F`，`minos` 检查用 `grep -E '^[[:space:]]+minos 14\.0$'`；管道改为 herestring，避免 `pipefail` 下 `grep -q` 提前退出使上游收到 SIGPIPE 而误判未匹配。本机以真实 FreeRDP 3.31.0/OpenSSL 3.5.8 完整重建 `.app` 并通过 `verify-app.sh`。另修复 `go vet ./...` 失败：`internal/pve/principal_test.go` 的表驱动用例按值复制含 `sync.Mutex` 的 `Client`，改为 `*Client`。复核本地可执行的 CI 门禁：`go build`、`go vet`、`go test ./...`、`cargo test --workspace`、`pnpm check/test/build`、`make iac-check`、`swift test`（67 项）全部通过。尚未验证的远端门禁是两个 `docker build`、Playwright 浏览器回归、`make macos-gateway-check`/`macos-rdp-reactivation-check` 与两个 Linux 容器作业。2026-09-03 起的工作已在本轮纳入版本控制（`b637a52`），此前远端 CI 结论只覆盖 2026-09-02 的基线；推送后需要重新以完整代码验证一次。补充验证本地可跑的其余门禁：`control-plane` 与 `session-gateway` 两个镜像构建、`make macos-rdp-reactivation-check` 与 `make macos-gateway-check` 均通过。

- 2026-09-08（IME事件宿主组件）：新增NSTextInputContext事务宿主，生产默认工厂创建真实AppKit上下文，测试注入仅替代上下文事件处理。区分本地IME与物理按键，消费IME处理的按下/松开对，Command/Control与无组合文本的命令仍返回物理路径；取消先失效客户端再discard/deactivate，抵御丢弃时同步unmark提交。组件测试覆盖组合不发送、下一事务、重复松键、快捷键、命令回退、旧客户端迟到提交及discard回调，ASan/UBSan和现有原生门禁通过。该宿主尚未挂到实际MRDPView，也未提供preedit可视层/真实候选位置与输入源检测，测试没有调用真实输入法；本地IME仍未启用，后续需完成视图挂载和中文实际验收。

- 2026-09-08（IME宿主接线前的同步撤销边界）：新增可重入焦点检查测试，修复前检查回调同步invalidate后selectedRange仍返回有效零选区，断言复现失败。现在保留检查block到调用返回，并复核原代次和回调仍有效；提交前第二次检查也使用同一保护，防止确认过程中失焦后继续发送。查询时撤销、提交前撤销两项以及ASan/UBSan通过，完整原生输入/剪贴板/编辑门禁通过。此轮修复协议适配层，不包含实际MRDPView宿主接线；本地IME仍未对用户启用，候选窗/事件路由与真实中文输入继续待完成。

- 2026-09-08（AppKit文本输入协议适配层）：依据本机SDK的NSTextInputClient契约实现独立组合事务客户端，接受NSString/NSAttributedString、相对插入串选区与本地preedit局部替换；只查询本地组合文本，不暴露/替换未知远端文档。确认及unmark只提交一次，取消/失焦永久使旧客户端失效，候选框坐标由当前宿主回调提供；未知命令不动态执行任意selector。测试通过真实协议一致性、preedit不提交、局部替换、选区、候选矩形、确认/重复确认、失焦后迟到回调、取消、非法远端替换范围及代理对边界；ASan/UBSan和完整现有原生输入/剪贴板/编辑门禁通过。仍是组件层：尚未连接MRDPView的NSTextInputContext事件路由、候选位置及preedit绘制、焦点/断线生命周期与提交核心，不是生产IME接入或中文实机证据；下一步必须完成宿主连接后验收。

- 2026-09-08（本地IME组合状态）：新增主线程组合状态，保存不可变preedit快照并按焦点代次校验更新/提交；取消、非法输入和成功提交使旧代次失效，迟到回调不能清空或覆盖新组合。限制4096 UTF-16单元，拒绝控制字符、非法代理对、越界/溢出选区和拆开代理对的选区。Foundation测试覆盖组合更新、外部可变字符串、一次提交、重复确认、旧焦点、取消后确认和边界；首版ASan/UBSan通过，补充代理对选区后随完整原生输入/剪贴板/编辑门禁回归。该状态层仍未接入生产NSTextInputClient，未创建候选窗或做Guest中文实测，不标记IME可用；后续必须连接实际事件、候选位置、失焦/断线回收与本地/远端输入法切换。

- 2026-09-08（Mac 本地文本提交核心）：新增有界UTF-16提交函数，整串预检后才发事件，拒绝空串、超过4096单元、控制字符与不完整代理对；每次按下/释放前检查原会话有效性，发送失败或失焦立即停止，不自动重放部分文本。中文、emoji代理对、组合字符、4096/4097边界、各发送位置取消/失败及非法尾部零写入测试通过，ASan/UBSan通过；内存活跃FreeRDP上下文实际Unicode回调也验证完整单元序列、失焦拒绝与失败截断，既有剪贴板/编辑门禁通过。当前仅提交核心与组件测试，尚未接入生产MRDPView的NSTextInputClient、marked text/候选窗、快捷键及生命周期清理，未替换运行.app或测试真实Guest中文输入；本地IME不能标记可用，MAC-03继续进行中。

- 2026-09-08（Mac 特殊符号物理键回归）：在实际MRDPView与FreeRDP输入编码器的内存测试中增加12个ANSI符号（反斜线、竖线、下划线、加号、括号、引号等），逐项覆盖先松修饰键/先松字符键共24种路径，核对扫描码、Shift按下/释放顺序与无残留修饰键；全部通过，现有剪贴板/编辑动作测试也通过。测试不创建窗口、不注入操作系统输入、不连接Guest。源码显示原生路径以keyCode/修饰键转发，不以characters文本内容替代物理键；当前未实现NSTextInputClient本地组合文本接口。此证据缩小问题范围，但不能证明自动typeText、非ANSI布局、macOS本地IME或实际远端文本完整性，MAC-03输入矩阵继续未闭环；不以物理按键单测替代中文/组合输入验收。

- 2026-09-08（新版 Mac 实测回收）：注销后首次关机预检发现Guest撤权队列尚待维护循环，正确拒绝提前关机；等待当前实例完成撤权后正常shutdown，PVE stopped/OK。停止独立控制面，删除本轮Token和两条专属ACL并回读验证，私有日志closed且清除测试Token/登录/初始化秘密；独立数据库保留审计。最终158仍运行，9202–9205均停止，原配置标记/DHCP/GVT-g未变；新版客户端保留、旧包备份可恢复。

- 2026-09-08（新版 Mac 签名包实测）：退出旧测试客户端后保留旧.app备份，构建并签名Release包，codesign deep/strict通过；新进程运行工作区产物，未操作另一个Xcode实例。新建独立数据库与两小时、仅VM9205写权限的Token，原开发库不变。Mac实际登录、卡片启动、首帧、终端echo输入、全屏及主动断开通过；此次审计启动1次/连接签发1次，原生日志0.751秒由connecting到connected，无自动恢复或TCP失败诊断。本轮未复现间歇性首连失败，不据单次成功关闭根因项。QGA当前用户Xorg日志确认5120×2678→5120×2804，已退回窗口模式；不是弱网/Gateway或完整特殊字符验收。登录时AX setValue一次未同步按钮启用，实际键盘重新输入后正常，工具编辑路径与用户键盘路径需区分。注销成功、恢复原HTTPS地址，资源清理随后核对。二进制SHA-256为9d6344ee69514b091df2071f1ca21d23953b5cf00a42646f35880933be80c1d9，Bridge为ac3ad8105ea377258d32ebf389a7c9a0a8a27dcc8b83c9bbe462298823aae0ac。

- 2026-09-08（Mac TCP 脱敏诊断）：为固定FreeRDP源码增加立即connect失败、异步SO_ERROR及等待超时三个诊断点，不改变连接控制流、重试预算或证书验证。Bridge仅接受固定调用点和完整格式，输出阶段与规范数值错误码；拒绝附加文本、未知格式/调用点、负数、前导零、溢出和空指针，保留Winsock与平台SO_ERROR命名空间区别。C白名单测试在ASan/UBSan下通过，补丁fuzz=0适配成功；完整原生FreeRDP3.31.0/OpenSSL3.5.8构建及现有剪贴板/显示/编辑动作门禁通过（上游弃用与可选依赖告警仍在）。产物放入隔离缓存，未替换当前运行的Mac应用，未启停PVE资源；下一轮仍需新版应用实机采集，不能把诊断能力记成首连故障已修复。

- 2026-09-08（首连失败的真实服务时序）：在隔离VM9205再次正常启动后，仅通过QGA读取上次启动的持久xrdp/journal日志，未创建用户或修改服务。PVE启动stopped/OK；早期QGA两次500后同一VM正常响应，未因观察失败重复启动。日志显示10:05:52 UTC策略重启完成且xrdp已监听3389，而Mac首轮TCP失败约为10:05:57，Guest没有该轮外部连接接入记录；10:06:06才记录第二轮外部接入并成功建立会话。此证据不支持“失败恰逢xrdp未恢复监听”的推测，暂不据此更改服务重启或新增固定等待。下一步在新版客户端实测时采集脱敏TCP错误类别与连接路径，进一步区分地址选择、路由/本机网络权限及远端拒绝；首次连接根因继续未闭环。QGA读取exitcode0且输出未截断，私有日志保存；诊断后正常shutdown stopped/OK，最终158运行及9202–9205停止的基线复核通过，无Token/数据库/宿主驱动变更。

- 2026-09-08（Mac 冷启动恢复状态）：复现并修复启动后的连接流程仍持有 stopped 卡片快照的问题。传输首次失败触发自动恢复时，此旧快照导致第二次启动请求；新增测试修复前实际观察 starts=2、journey仍stopped。现在启动轮询成功后经取消/attempt归属校验更新流程快照，准备失败也保留最新状态，自动恢复与手动重试均只启动一次。新增两项回归，Swift全量67项通过（12.449秒，GatewayInterop与展示目录仍按原条件跳过）；本轮未重新打包或安装Mac应用，也未重启真实VM，实机复验仍待完成。上一轮原生日志确认首个连接0.251秒进入failed、firstFrame=0、错误0x00020006，调用链位于TCP连接/协商；不能由此断言根因是xrdp重启或网络。服务策略可能重启xrdp而ready仅检查文件标记，是下一轮需验证的时序线索，不将重复启动修复当作首轮传输失败根因已修复。

- 2026-09-08（本轮 Mac 验收清理）：VM9205 正常 shutdown 的 PVE 回执 stopped/OK；注销撤权完成后停止独立控制面，删除本轮临时 Token 与两条专属 ACL，并回读确认不存在。私有日志 closed、已清除该 Token secret/初始化及登录密码；独立数据库保留审计记录。最终核对158仍running，9202/9203/9204/9205均stopped，9205的DHCP/GVT-g配置和原Job标记保留，未修改宿主驱动。首轮服务日志还显示首张连接票据退休后再次请求启动与连接，最终进入桌面；这一自动恢复过程的原因尚未定位，不能据最终首帧宣称首次连接路径无异常。

- 2026-09-08（新模板 Mac 原生连接与 GVT-g）：独立控制面、独立 PostgreSQL 数据库和仅授权 VM9205 的两小时 PVE Token 完成真实 Mac UI 登录、卡片启动并连接、首帧、全屏往返、主动断开与重连；原终端及内容在重连后保留。Token 对158/9202/9203/9204配置访问均403，原开发库未改。新克隆未再手工补 Guest GPU 配置，实际用户 Xorg 日志显示 Intel HD530 Glamor、iris DRI2/DRI3；Mac 接收真实图形帧。Guest 日志与交互 xrandr 确认窗口5120×2678，全屏5120×2804，退出后恢复。此项补齐新模板→新克隆→Mac直连的硬件渲染证据，不代表硬件编码、弱网或K8s网关验收。原生“缩放窗口”动作本轮未观察到尺寸变化，不能算通过；自动 typeText 的管道/反斜线丢失，而逐键 Shift+反斜线及反斜线可显示，需进一步区分自动输入路径与客户端映射，输入矩阵继续开放。注销后的 Guest 撤权最终 revision7/revoke/applied，回收队列清空后发起正常关机；客户端恢复原 HTTPS 地址，测试基础设施清理结果另行核对。

- 2026-09-08（新克隆首次初始化）：仅启动上一轮由产品创建并自动配置的 VM9205，PVE 启动任务 stopped/OK；未再手工补网络或 GPU 参数。早期 QGA 网络查询返回500，等待后同一VM响应，未重启。Guest cloud-init 从 modules-final/running 收敛为 done，各阶段 errors/recoverable_errors 为空，记录完成时间49.11秒；首次配置含 package_upgrade:true，本次最终正常完成。QGA/xrdp active、DHCP地址10.31.0.176、HD530由i915驱动、renderD128存在，systemctl failed列表为空。结果由真实QGA进程回执（exitcode0、输出未截断）保存到私有启动日志。此项验证新克隆首次初始化，不代表RDP会话已用Glamor/硬件编码，也不包含Mac登录连接；接下来仍需独立会话与客户端验收。

- 2026-09-08（真实新克隆完整收尾）：预检 VMID9205 未占用、模板9202正确且无锁、infra-node6约6.5GiB空闲内存/Ceph356GiB可用/GVT-g V5_4可用1。新增显式 opt-in TestLiveCompleteClone，以一次性 PostgreSQL 配置验证镜像/GPU档位，通过实际 createDesktopInstance 路径创建9202→9205完整克隆，真实 PVE 完成后后台执行 DHCP、GVT-g摘要写入、目标复核和桌面登记。实机47.88秒（race包49.496秒）通过：Job succeeded、登记为Linux且启用、同键重试不重建，clone/DHCP/GPU各写1次，VM9205始终stopped。传输保护拒绝其他VM写入/启停/映射变更，克隆前独占持久化私有日志，最终configured；默认测试跳过。保留停止的新VM9205和磁盘、已生成的DHCP/GVT-g配置供首次启动验收，一次性数据库按测试机制回收，未修改原开发库或正式部署；这是产品Handler+真实PVE/真库，不是浏览器/正式服务登录链路。首次启动、cloud-init完成、RDP硬件渲染与Mac体验继续待验收。

- 2026-09-08（真实 DHCP 收尾函数）：新增显式 opt-in TestLiveCloneNetworkDefaults，使用产品 ensureCloneNetwork 与真实 PVE Client 对停止的既有隔离 VM9204 连续收尾两次，观察 ipconfig0=ip=dhcp 且 PUT 总数1。传输层仅允许该目标 config/status 读取、认证及一次严格字段/摘要校验的网络 PUT；提交前独占创建私有日志，默认测试跳过。实机测试0.21秒（含 race 包1.901秒）通过，无一次性数据库写入；外层实验脚本预存完整配置，结束后带新摘要删除本次 ipconfig0，确认其他配置恢复，VM始终停止，日志restored。此轮直接验证产品函数而不是重写其逻辑的独立 API 请求，但目标是已存在克隆，不是部署后的新建→完整 Job→首次启动/Guest/Mac 验收，不能关闭整个模板网络项。

- 2026-09-08（克隆→DHCP→登记接口链路）：真实一次性 PostgreSQL 的克隆边界测试加入 cloud-init 磁盘/net0/摘要，所有完成路径必须经过 DHCP 条件写入，断言写入时 Job 仍 running，正常、克隆响应丢失、句柄持久化失败及恢复响应丢失后最终成功且 clone/网络写入各1次。另增加 PVE 模拟服务实际接收网络 PUT 后应用值并 Hijack 关闭 TLS 测试连接的故障，后台重新读取已应用 ipconfig0 后完成，不重发网络 PUT。原五场景两轮6.122秒、新增第六场景后两轮20.646秒通过。没有真实 PVE 写入；这证明模拟上游故障与真库 Job/登记链路，不等同部署、真实克隆首次启动或 Mac 连接验收。

- 2026-09-08（Linux 克隆 cloud-init 默认网络）：基于 VM9204 实测缺少网卡配置的证据，在克隆收尾增加主网卡 DHCP 补齐：仅保存 Linux 快照且有 cloud-init 磁盘、ipconfig0 为空时生效；核对目标/无锁/net0/摘要后条件 PUT，必须回读 ip=dhcp 才继续。已有配置和 ipconfig1 等其他网卡不覆盖，非 cloud-init/Windows 不改动。增加模拟 PVE 的默认写入、已有静态配置、无 cloud-init、Windows、无网卡/摘要、目标不符、冲突、响应丢失后读回不重写、写入未生效十类检查。此轮未修改真实模板/VM；现有模板本身仍没有该默认值，需要部署后的真实克隆与首次启动验收。静态地址继承、多网卡配置和 Windows 网络策略仍未闭环。

- 2026-09-08（隔离 Debian Guest 首次 GVT-g 启动）：VM9204 默认 cloud-init dump 仅 DNS、无网卡配置，预检阻止直接启动；以摘要保护临时添加 ipconfig0=ip=dhcp 与 GVT-g V5_4，核对生成的 DHCP4 配置后启动。PVE 启动任务 stopped/OK，实际 Guest Debian13、内核6.12.94+deb13-amd64 的 lspci 显示 HD530 8086:1912 使用 i915，/dev/dri/renderD128 存在，QGA/xrdp active，eth0 获得 DHCP 地址。初次只读 QGA 诊断脚本错误地传 JSON command 字符串，改为 API 要求的重复 command 字段后取得 pid、exitcode0 和未截断输出；这不是产品 ExecGuest 的缺陷。观察时 cloud-init 仍 running，不能宣称初始化完成。已正常 shutdown（stopped/OK），以新摘要删除临时 hostpci0/ipconfig0、核对其他配置恢复及158/9202/9203基线状态未变，日志 restored；首次启动对隔离 Guest 磁盘的正常写入保留。此轮证明 mdev 进入 Guest 并被驱动识别，不证明 xrdp/Glamor、编码性能或 Mac 会话加速。后续需修复/明确克隆的默认网络生成，并验证初始化完成及实际 RDP 硬件渲染。

- 2026-09-08（隔离 VM 真实 GVT-g 配置往返）：只读确认 infra-node6 的 Intel HD530、现有共享映射 vc-vdi-intel-gvtg 及 i915-GVTg_V5_4 可用实例1；VM9204 stopped、原名称/Job 标记、无锁/非模板且无 hostpci。使用刚读取的摘要实际 PUT hostpci0=mapping=vc-vdi-intel-gvtg,mdev=i915-GVTg_V5_4，回读参数符合、除摘要及 hostpci0 外完整配置相同，VM 保持 stopped。随后以新摘要删除本次添加的 hostpci0，确认删除成功、其他配置恢复，158/9202/9203/9204 的节点和状态与本次基线相同。私有独占执行日志 phase=restored。未改共享映射、宿主驱动或原开发库；实际调用为独立 API 验证脚本，不是完整控制面克隆链路，VM 未启动，不能视作 Guest 驱动、硬件渲染/编码、性能或 Mac 连接验收。

- 2026-09-08（GPU 配置比较顺序无关）：将收尾跳过条件从完整字符串相等改为完整参数集合相等，允许参数顺序变化，但拒绝重复键、缺失项、额外项、不同值和超限输入，不猜测隐式默认值。新增 PCI 六种顺序、mdev 两种顺序及异常参数单测；登记故障恢复接口夹具将已应用参数逆序返回，继续断言两种 GPU 路径恢复后写入总数1次、授权仅在 Job 成功后放行。当前架构文档已替换旧的字符串相等描述；本轮为自动测试，未做真实 GPU 挂载或部署变更。

- 2026-09-08（GPU 成功后的登记恢复）：修复 GPU 配置已成功、登记 SQL 失败后再次收尾会重复设备写入的问题。核对目标/摘要后，若 hostpci0 与原请求的完整 PCI 或 mdev 字符串一致则跳过 PUT，仍执行最终目标复核与登记。新增一次性 PostgreSQL/模拟 PVE 测试：清单先登记并分配目标、真实触发器拒绝登记 INSERT、验证原 Job running 且授权拒绝，解除故障后新 Server 收尾 succeeded、授权恢复且 GPU 总写入仍1次；两路径两轮 3.174 秒通过。初轮夹具遗漏预登记而在分配准备阶段被正确拒绝，补齐后到达预期故障点。此轮未部署或写真实 VM；严格字符串相等不覆盖 PVE 参数重排序，真实 Guest 加速与浏览器恢复闭环仍需后续验收。

- 2026-09-08（真实 PVE 摘要拒绝）：只读预检确认隔离 VM9204 位于 infra-node6、stopped、名称与原克隆 Job 标记一致、无锁/非模板且有合法摘要，既有基线状态一致。使用错误但格式合法的摘要，对该 VM 同步 config PUT 仅提交其现有名称，不添加 GPU、不改变目标配置、不启动。PVE 返回 HTTP500 并明确 checksum mismatch，前后完整 config 深比较相同，VM 仍 stopped；私有执行日志记录 verified，脚本有独占提交记录，禁止意外重跑。实测没有创建 Token/ACL，也没有触碰 VM158 或宿主驱动。将实际500形状加入 PCI/mdev 两路径“不重试、不移除摘要”单测；这只证明实际集群配置 API 的拒绝行为，不等于真实 GPU 参数挂载、真实并发改写或跨系统登记的完整验收。

- 2026-09-08（GPU 配置摘要前置条件）：核对 PVE 上游同步 config API 的 digest 检查后，VMConfiguration 保留内部摘要；PCI 与 GVT-g/mdev 写入签名强制要求有效的 40 位摘要并原样发送。克隆收尾读取摘要时再次验证名称/标记/非模板/无锁，缺失或无效即停止，不退回无条件写入。新增摘要解码、两路径无效值零请求、摘要冲突一次请求且不重试测试；目标变化接口矩阵扩展到缺失/非法摘要并断言写入携带核对值，两轮 9.916 秒通过。完整 PVE race 2.150 秒、HTTP API race 27.976 秒通过（完整运行启动后补充的两项摘要接口用例另由定向两轮覆盖）。仅真实一次性数据库和模拟 PVE，未部署/写入真实 GPU；需要隔离 PVE 实测其实际版本冲突行为，跨系统登记竞态与同配置替换仍未闭环。

- 2026-09-08（克隆收尾目标复核）：发现后台收尾仅依据历史任务 stopped/OK，恢复核对后或进程重启期间发生的目标变化未再次阻断。所有克隆收尾现于 GPU 写入前、最终注册前重新核对 inventory/config 的节点、类型、模板、名称、Job 标记和锁；不符或读取失败保留 running/原 UPID，不登记或放行桌面，后续重试重新核对。新增真实一次性 PostgreSQL/模拟 PVE 八类变化回归：缺失、换节点、模板、改名、换标记、锁定、503、GPU 写入后换标记，两轮 6.460 秒通过；最后一类仅首次 GPU 写入一次，第二次收尾不再写，所有异常均无注册记录。更新既有 GPU 隔离与双副本/子进程退出夹具提供真实形状的目标观察，既有完整 HTTP API race 23.669 秒通过。未修改真实 VM 或原开发库；多次观察仍不是 PVE 原子快照，外部管理员在观察与写入之间的变更风险尚未完全解决，不能以此关闭外部资源并发安全或整个恢复验收项。

- 2026-09-08（Web 恢复硬截止修复）：发现此前 35 秒仅调用 AbortController，传输忽略取消时流程仍会悬挂。改为取消 Promise 与请求竞争，读取超时进入错误，提交/查询超时保持结果不确定；关闭立即结束等待并释放定时器，不等待底层传输结束，迟到成功或失败不能覆盖新状态，也不自动重发 POST。新增模拟忽略 Abort 的截止前后、迟到 resolve/reject、提交及查询超时、关闭清理测试。此轮仅修改异步控制器与单测，不涉及可见布局或真实 PVE 写入；正式登录/真实 API 浏览器链路仍未验收。

- 2026-09-08（Web 恢复焦点与列表夹具）：恢复表单打开时聚焦标题，刷新、提交及查询前移到稳定标题，避免控件卸载丢失焦点；关闭返回原按钮，任务更新移除按钮后回到任务列表标题，入口提供展开状态。浏览器夹具改用真实 ActivityView 与任务更新回调，实际验证键盘打开/关闭、未知结果查询、更新后关闭回退；中英文 × 明暗 × 320/1280 共 8 格截图及根宽检查通过，空候选和 503 错误均有明确状态及刷新入口，没有提交按钮。构建和 45 项 Web 单测通过。依据 web-ui 技能补齐焦点行为；本次网络仍为夹具，不代表正式管理员登录、真实 API 与 PVE 浏览器端到端验收，后者继续待办。

- 2026-09-08（Web恢复可见入口初版）：近期任务对accepted/无UPID克隆显示“恢复克隆”，在列表下展开原位表单，复用FormField、按钮和主题Token；中英文完整文案，管理员明确选候选/填原因，提交后区分已登记、冲突与未知结果，未知时只查询原Job，关闭不暗示服务器取消。生产构建与45项Web单测通过。浏览器通过独立5191端口的组件夹具实看英文320/1280×浅/深色、中文窄深/宽浅，英文四格根宽等于scrollWidth；发现并修正窄屏Close断词，修正后复看窄英文。真实浏览器操作选择候选、填原因、Tab到提交再Enter，模拟响应丢失只剩“查询原任务”，查询后显示已登记。夹具无PVE副作用，视口已复原；这不是正式登录任务列表端到端验证。尚需正式列表入口/焦点转移与返回、完整中文主题尺寸矩阵、错误/空候选状态和真实API浏览器闭环，不以初版关闭Web恢复项。设计文档已同步交互边界。

- 2026-09-08（Web恢复异步状态）：新增恢复交互控制器，统一idle/loading/ready/error/submitting/uncertain/checking/settled/conflict状态；请求带取消、35秒Abort截止和版本守卫，旧响应或已关闭流程不能写回，重复提交被同步状态挡住。提交失败或响应不符进入uncertain，只读原Job，不自动重发POST或刷新成可提交状态；其他句柄已占用时明确conflict，不能当成本次恢复成功。模拟忽略Abort的乱序读取、关闭后的迟到响应、重复提交、未知结果查询、查询失败及不同句柄冲突，全部Web45项单测和TypeScript检查通过。依据web-ui技能的单一请求守卫与双终态要求实现；本轮仍仅交互逻辑，无可见表单/样式改动，也没有浏览器渲染证据，近期任务入口与主题/尺寸/键盘验收仍未完成。

- 2026-09-08（Web恢复数据层）：依据现有UI设计语言，确定恢复操作沿用“近期任务”而不新增导航；新增前端候选证据类型、no-store/可取消读取、带CSRF的恢复提交（不自动重试），以及仅对accepted/无UPID克隆生效的候选判定。判定拒绝任务ID不符、目标缺失/不匹配/锁定、重复候选、非法时间、未成功或日志目标未匹配，不自动选择句柄；服务端仍为提交授权与新鲜证据的最终判定方。定向13项、全部Web41项单测通过，TypeScript检查通过。此轮只增加API与判定逻辑，没有可见恢复入口、表单或样式变更，尚未执行主题/尺寸/语言/键盘渲染验收，不能称Web恢复功能已完成；下一步接入近期任务交互与浏览器实测。

- 2026-09-08（提交后进程直接退出接续）：后台接续测试新增实际子进程，在一次性schema执行恢复事务后立即os.Exit(23)，不运行defer、优雅关闭或watcher；父进程确认精确退出码及数据库running/原UPID，再由两个新维护实例正常收尾，终态观察仅一次，恢复审计仍1条。子进程入口需显式环境开关且DSN的search_path必须匹配一次性vcw_test_随机schema，不执行迁移。定向三轮初版3.531秒，追加schema保护及审计断言后三轮3.603秒通过，完整HTTP API race23.359秒通过。本轮使用真实PostgreSQL与真实子进程、模拟PVE，没有启动正式control-plane二进制或修改真实VM；证明的是恢复事务返回后进程直接退出，不涵盖SIGKILL/COMMIT在途/主机断电或真实外部资源并发变更，Web入口仍待继续。

- 2026-09-08（持久化克隆任务后台接续）：确认原收尾只依赖提交时 watcher 或管理员GET，新增控制面启动的独立 `RunPVEJobMaintenance`，按ID游标分页扫描 running/已知UPID克隆，每项10秒上下文；和凭据撤权维护分开，避免PVE等待阻塞注销。无句柄accepted任务不自动认领。克隆refresh在数据库桌面控制锁内重新读取状态，串行化原watcher、扫描实例与页面查询，防止已完成任务重复GPU/注册收尾。独立PostgreSQL/模拟PVE覆盖没有原watcher或GET时两个新实例恢复同一任务、仅一次终态观察、正常注册、未知任务不变、取消退出、分页不漏项/排除终态；三轮Store2.310秒/API2.490秒通过，完整HTTP API race23.469秒通过，control-plane编译通过（无测试文件）。本轮未部署、未修改真实PVE或原开发库；重建实例不等于真实杀进程/断电验收，外部资源变更边界和Web入口仍待继续。

- 2026-09-08（恢复提交响应丢失）：在克隆上游响应丢失与句柄持久化失败两条接口测试链路上，分别增加恢复 API 已提交后 ResponseWriter 返回 ErrClosedPipe 的故障，断言确实发生一次响应体写入且客户端未收到任何响应体。等待原收尾完成后重建 Handler，通过管理员 Job GET 读回 succeeded/原句柄，重复恢复409、恢复审计仍仅1条、clone计数仍为1。Job GET 增加 no-store 并同步 OpenAPI，避免不确定提交后使用缓存状态。独立 PostgreSQL / 模拟 PVE 定向 race 三轮7.321秒，完整 HTTP API race23.961秒通过。本轮未写真实PVE；响应写失败测试不等于进程终止、连接断电或提交与watcher调度之间崩溃的验收，这些场景及Web入口继续待办。

- 2026-09-08（真实 PVE 克隆响应丢失→恢复验收）：新增显式 opt-in `TestLiveCloneRecoveryResponseLoss`。使用独立 Token、一次性 PostgreSQL schema 和固定目标9204，PVE nextid 查询附带真实 vmid=9204 校验；传输保护只允许一次9202→9204 full clone，先独占创建/fsync 私有提交日志，取得真实 UPID 后给控制面注入 EOF，不伪造 PVE 响应。真实 PVE 克隆完成后，重建 Handler 的同键重试保留 accepted/无句柄，通过恢复 API 重新核对任务/日志/目标并正常 watcher 收尾 succeeded，克隆次数仍为1，后续重试不再次克隆。实机用例47.29秒（含race48.972秒）通过；完整 HTTP API race25.183秒通过（默认不执行 opt-in 实机用例）。这是实际PVE加客户端响应丢失注入，不是杀死服务进程的重启/断电验收，也不证明外部管理员并发改VM的安全性。结束后真实检查9204 stopped、无锁、名称/Job标记匹配；158/9202/9203状态与基线一致。已删除专用Token及5条ACL并验证不存在，清除私有日志中的Token Secret；保留停止的测试VM9204及磁盘用于复核，没有修改原开发库。Web入口、真实进程崩溃/提交响应丢失、外部资源变更边界等仍待继续，整个恢复项未关闭。

- 2026-09-08（真实克隆恢复隔离预检）：PVE 只读核对 VMID9204 未占用、模板9202 为预期 Debian13模板且无锁，ceph-pve 可用约 359 GiB、infra-node6 空闲内存约 6.55 GiB。原测试 Token 对9204仅有审计权限，未尝试用它克隆或扩大其 ACL。使用用户提供的管理凭证创建新的两小时、privsep Token，复用现有实验角色，仅在 VM 路径9202/9204授予操作权限，沿用基础设施只读、指定存储和 vmbr0 权限；实测158和既有9203的配置访问均403，9202具备Clone、9204具备Allocate。私有独立日志 phase=prepared，记录凭据/ACL/基线以支持恢复与撤销；没有创建、启动或修改任何 VM，业务158仍running。下一步须在该独立目标与一次性数据库执行真实响应丢失→恢复API验收，结束后回收专用Token/ACL；不能将本次权限准备计作克隆恢复实测通过。

- 2026-09-08（克隆故障到恢复的接口链路）：扩展既有真实 TLS 测试连接故障场景：模拟 PVE 接收 clone 后 Hijack 断开响应，以及任务句柄数据库 UPDATE 被触发器拒绝，两种情况下重建控制面 Handler，确认同键重试保持 accepted/无句柄且克隆计数仍为 1；解除持久化注入后，经恢复 API 新鲜证据核对与事务补录，正常 watcher 收尾为 succeeded，原目标 Linux 注册信息保留、恢复审计恰好一条，后续同键重试仍不再次克隆。定向首轮 2.923 秒，新增审计/注册断言后三轮 4.937 秒，完整 HTTP API race 23.001 秒通过。另只读复核真实隔离 VM9203：既有克隆 stopped/OK，VM 保持 stopped，磁盘/NIC 与之前证据一致。本轮没有创建或改动真实 VM；连接故障发生在模拟 PVE 服务，不是实机 PVE 故障验收，真实新克隆的隔离目标/权限预检及响应丢失→API 恢复仍待执行。

- 2026-09-08（管理员克隆恢复 API）：接入 `POST /jobs/{id}/clone-recovery`，要求管理员会话、非空正确 CSRF、任务句柄与原因。30 秒上下文内获取目标桌面控制锁，再核对原任务身份/时间、stopped/OK、明确的日志源/目标声明，以及未锁定 QEMU 的原名称和 Job 标记；缺原始快照、旧日志未知格式、任何不匹配均不猜测。事务补录与审计后返回 202，启动既有克隆 watcher 完成注册/原配置收尾，不重发 clone；失败或响应不确定要求读取原 Job，重复提交 409。独立 PostgreSQL / 模拟 PVE 接口测试覆盖角色/CSRF/匿名路由、运行中/失败/主体/源/时间窗、日志未知或不符、目标锁定/缺失/标记不符、上游失败，以及成功补录→正常 watcher succeeded→重复拒绝；定向首轮 2.218 秒，新增时间窗/匿名覆盖后三轮 3.134 秒，完整 HTTP API race 22.213 秒通过。OpenAPI 已同步。本轮未部署或写真实 PVE；仍需真实克隆响应丢失故障注入、补录 API 实机验收、并发外部资源变更边界与 Web 入口，不能将模拟接口闭环视作整个恢复项完成。

- 2026-09-08（克隆恢复事务）：新增 Store `CommitCloneRecovery`，在权限变更锁内重新核对启用的管理员，并对 accepted/无句柄、源/目标/节点及原请求 JSON 做 CAS；仅补录为 running，和成功审计在同一数据库事务提交。审计记录获胜句柄 SHA-256，不复制含主体的原始 UPID；重复提交返回冲突，由读取 Job 确认已提交结果，不重开任务。独立 PostgreSQL 测试覆盖普通/缺失/禁用管理员、请求快照不符、确实抵达审计 INSERT 的故障注入及完整回滚、8 路不同句柄并发唯一获胜、仅一条审计且摘要匹配、重试冲突及访问门禁仍关闭。初版测试夹具误用 CreateLocalUser 的 Role 参数，已修正为显式测试管理员并加强故障到达断言；定向三轮通过 5.456 秒，新增禁用与摘要断言后三轮通过 2.668 秒，完整 Store race 86.996 秒通过。本轮没有部署或改动真实 VM/原开发库；事务尚未接入 HTTP 恢复提交，仍需新鲜 PVE 联合证据、桌面控制锁、CSRF/请求契约和实机故障闭环，不能视为恢复功能已完成。

- 2026-09-08（克隆日志目标关联）：根据 [PVE 克隆实现](https://github.com/proxmox/qemu-server/blob/master/src/PVE/API2/Qemu.pm) 与 [任务日志 API](https://github.com/proxmox/pve-manager/blob/master/PVE/API2/Tasks.pm)，加入只读前 32 行日志检查，严格识别源/目标声明，拒绝行号缺口、重复声明、整数溢出、同源目标及超限；旧格式不从磁盘名猜测。管理员候选响应增加 log_target_status（matches/mismatch/unrecognized），不返回原始日志，不认领或放行；日志读取失败返回 503。完整 PVE/HTTP API race 分别 2.114/25.923 秒通过。真实 PVE 使用既有隔离克隆任务只读查询，唯一候选、任务身份/成功状态及日志源 9202→目标 9203 均核对通过（原生 0.07 秒、含 race 1.355 秒）；没有新建或修改 VM。日志声明证明该任务的目标意图，不单独证明创建成功或不可变归属；后续仍需把联合证据纳入受审计、并发安全的恢复提交与实机故障闭环，Web 入口尚待接入。

- 2026-09-08（克隆候选联合观察）：管理员候选 API 对原主体匹配的每个句柄独立读取任务状态，复核类型、源 VMID、主体/Token、开始时间，再复用目标检查读取 inventory/config，返回任务 running/stopped/exit_status 与 target_status；整个上游观察限时 20 秒。身份或时间证据矛盾、任务/目标读取失败均返回 503，不泄露部分候选；失败任务、缺失/不匹配/锁定目标明确展示，不推进 Job 或访问权限。独立 PostgreSQL / 模拟 PVE 新增运行中、失败、源/Token/时间变化、上游失败及目标状态矩阵，定向 race 2.054 秒，完整 HTTP API race 21.318 秒通过；既有目标检查回归同时覆盖公共 helper 的重构。OpenAPI 已同步。本轮没有真实 VM 写入或部署变更；联合观察并非原子快照，任务成功与可变标记匹配仍不足以证明任务创建了目标，任务日志/目标关联证据、受审计恢复提交、Web 入口与 API 实机闭环继续待办。

- 2026-09-08（克隆原始提交身份快照）：服务端在克隆准入时记录 PVE 用户或完整 Token ID，覆盖客户端伪造值，不保存密码、Token Secret 或 Ticket。候选 API 按这份原始主体精确过滤，包括同一用户下不同 Token 的隔离；更换当前配置不改变历史任务的匹配依据。旧 Job 缺少快照返回 409，要求独立核对，不猜测当前身份。独立 PostgreSQL / 模拟 PVE 定向 race 连续 3 轮通过（PVE 1.481 秒、HTTP API 6.152 秒），完整 PVE/HTTP API race 命令退出 0（HTTP API 21.960 秒）。OpenAPI 已同步。该轮未变更真实 VM 或部署数据库；身份匹配仍不等于目标归属证明，联合证据、受审计补录、Web 入口和恢复 API 实机闭环仍待完成。

- 2026-09-08（管理员候选发现 API）：新增 `GET /jobs/{id}/clone-candidates`，仅管理员可对 accepted/无 UPID 的原克隆 Job 查询，源节点/源 VMID 与创建前 30 秒至后 15 分钟窗口来自数据库，调用方参数不能扩大范围。完整有界结果返回句柄与开始时间，no-store 并审计数量；读取失败/超限返回 503，已知句柄任务返回 409，不自动认领或放行。独立 PostgreSQL / 模拟 PVE 验证匿名/普通用户拒绝、参数不能改写 Job 范围、返回唯一候选仍不改任务、上游失败和已知句柄不再查询，定向 race 3 轮通过 2.581 秒，完整 HTTP API race 通过 26.834 秒。OpenAPI 明确 UPID 自带 PVE 主体标识、不得公开分享。Web 入口、进一步归属核对、受审计补录及 API 实机验收仍待继续。

- 2026-09-08（丢失克隆句柄的有界候选发现）：新增只读 `CloneTaskCandidates`，固定 qmclone/源节点/源 VMID/source=all，时间窗最多一小时，请求 51 条以检测超过 50 个候选；超限、重复、源/节点/类型/时间不符或缺主体明确失败，不返回可被误认为完整的部分结果。保留独立 tokenid，不把列表状态用作完成证明。10 类响应与非法窗口回归通过，完整 PVE race 2.138 秒。真实 PVE 以既有克隆开始时间前后各 30 秒查询，得到 1 个候选且精确包含已知 UPID，并再次核对其身份和 stopped/OK（原生 0.06 秒、含 race 1.528 秒）。全程只读、没有新建或修改 VM；候选发现仍未接入管理员恢复提交，须联合目标标记、主体/时间/日志证据和审计后才可补录，不能仅以唯一候选自动认领。

- 2026-09-08（真实 PVE 任务证据只读验收）：新增显式 opt-in `TestLiveTaskEvidenceReadOnly`，使用 mutations=false 的 Token 客户端查询指定已完成任务，不列举或修改任务，不打印主体/Token。先对隔离 VM9203 原关机任务核对节点、句柄、主体、Token ID、开始时间及 stopped/OK（原生测试 0.04 秒、含 race 1.601 秒）；再对创建 VM9203 的既有 qmclone 任务执行同样核对（0.03/1.489 秒），实际任务 ID 为源模板 9202，与此前源码语义一致。没有新建、启动、关机、删除或修改任何 VM。该证据验证 `InspectTask` 与真实 PVE 响应兼容，不证明丢失任务发现、目标归属联合校验、自动补录或恢复完整闭环。

- 2026-09-08（PVE 任务证据读取基础）：依据 [PVE Tasks API 实现](https://github.com/proxmox/pve-manager/blob/master/PVE/API2/Tasks.pm) 新增独立只读 `InspectTask`，保留 UPID、节点、类型、源 ID、用户、独立 tokenid、开始时间及状态；不改变现有轻量 TaskStatus。目标参数异常在发送前拒绝，返回句柄/节点不匹配、缺主体/时间和不完整生命周期明确失败。9 类响应及非法目标测试通过，完整 PVE race 通过 2.037 秒。此方法尚未接入恢复授权入口或实机调用；任务类型/时间/主体与平台 Job 的匹配、目标标记、日志和补录审计仍须联合核对，不以取得任务元数据代替归属证明。

- 2026-09-08（克隆检查歧义与模板变化保护）：只读检查发现重复目标 VMID 时返回 inspection_unavailable/503，不选择第一条；目标配置新增内部 template 读取，清单后已转为模板时也判 mismatch。扩充节点不符、LXC、清单模板、配置模板、名称不符及重复清单六类回归，继续断言不输出原始描述、PVE 无写入和 Job 不变。定向 API race 3 轮通过 2.899 秒，完整 PVE/API race 通过 2.013/22.470 秒。此检查仍为非原子观察，不证明外部资源所有权或克隆完成；真实环境、日志归属及恢复写入入口仍待继续。

- 2026-09-08（管理员克隆目标只读检查）：新增 `GET /jobs/{id}/clone-inspection`，先验证管理员与克隆 Job，再读取集群清单和目标配置，区分 absent/mismatch/locked/marker_matches；上游读取失败明确 503，不伪装成不存在。检查记录审计、响应 no-store，不返回原始描述，不写 PVE 或改变 Job。独立 PostgreSQL / 模拟 PVE 验证匿名 401、普通用户 403、四类证据、检查失败、描述不泄漏，以及 marker_matches 后任务仍 accepted/无句柄、PVE 写入为 0。定向 race 3 轮通过 6.333 秒，完整 PVE/API race 通过 2.039/30.762 秒，OpenAPI 已同步。仅提供当前目标证据，不能据标记自动认领任务、判定克隆完成或解除访问栅栏；任务日志/时间/来源归属核对、补录入口、Web 使用与实机验收仍待完成。

- 2026-09-08（克隆外部关联标记）：核对 [PVE 克隆实现](https://github.com/proxmox/qemu-server/blob/master/src/PVE/API2/Qemu.pm)，`qmclone` worker 标识关联源 VM，不能单凭任务 VMID 认领目标；克隆 description 可写入目标配置。正式创建请求现携带服务端生成的 `VC Workspace clone job: <Job ID>` 标记，发送前 Job 已持久预留；不接受客户端自定义该字段。API 测试端独立按标记查询数据库，确认对应 accepted Job 与正确目标；PVE 客户端单测验证表单标记及无标记的旧调用仍不覆盖 description。完整 PVE/HTTP API race 分别通过 2.031/37.306 秒。标记仅是后续归属核对的一项证据，不是防篡改或授权凭据；尚未实现据此认领任务、恢复丢失 UPID，也未在真实集群新建 VM 验证描述。现有任务无标记不自动补写或认领。

- 2026-09-08（克隆句柄绑定与迟到写入保护）：新增原子 `RecordCloneTaskHandle` 并接入正式克隆入口，核对任务操作、源/目标 VMID 及源/目标节点；只允许 accepted/空句柄变成 running，或相同 running 句柄重复确认，不允许换句柄或重开终态。独立 PostgreSQL 验证五类目标错配拒绝、两个不同句柄并发仅一个成功、相同句柄幂等和成功终态保持。定向 store/API race 3 轮通过 2.565/4.795 秒，完整 HTTP API race 通过 22.045 秒。此为持久化基础，不是管理员任意补录句柄的授权入口；独立 PVE 任务归属核对、丢失句柄的发现/恢复与真实进程中断验收仍待实现，未修改实机环境。

- 2026-09-08（克隆任务句柄落盘失败不再误报运行）：修复已收到 PVE UPID 后忽略 UpdateJobTask 错误的问题；持久化未确认时返回 503/`clone_task_persistence_uncertain`，不发出正常 running 响应、不启动未落盘任务的 watcher，保留原预留。独立 PostgreSQL 触发器拒绝 running UPDATE，模拟 PVE 正常返回句柄；新 Handler 重放两次仍得到原 accepted/无 UPID/原编号，PVE clone 调用仅一次，变更请求拒绝。三类验证克隆边界 race 3 轮通过 5.733 秒，完整 HTTP API race 通过 22.332 秒。只证明不误报与不重复创建，尚未实现句柄补录/独立核对或进程崩溃后的自动恢复；未修改真实 PVE 或实机验收库。

- 2026-09-08（克隆上游回执丢失保护）：原代码将任意 PVE 调用错误记录为 failed 并声称已被拒绝，无法区分已接收但响应丢失。现在保守保留 accepted/无 UPID 的待核对任务与原编号，保存脱敏的结果未知说明，返回 `pve_clone_uncertain`，不自动重发。独立 PostgreSQL / 本地 TLS PVE 测试端读取实际 clone 请求后直接关闭连接；新 Handler 两次同请求重放仍返回原任务/目标，PVE clone 计数为 1，变更请求仍冲突。定向 race 3 轮通过 3.997 秒，完整 HTTP API race 通过 23.177 秒。此为真实本地连接故障、模拟 PVE，不是真实集群故障验收；没有 UPID 的结果核对/人工恢复，以及收到 UPID 后持久化失败的恢复仍待补齐，不能自动另建 VM 或宣称克隆确实失败。

- 2026-09-08（克隆空闲编号候选搜索）：创建入口从 PVE 建议编号开始，在数据库事务内扫描最多 1024 个候选，排除当前集群清单、受管记录、历史克隆与镜像构建目标，选定后持久预留；保留失败记录，不通过删除预留复用编号。PVE 实际克隆使用返回的预留编号，幂等重放保持原目标。独立 PostgreSQL 4 路并发分别获得不同候选，覆盖已占编号、空清单、搜索耗尽无残留与稳定重放；模拟 PVE 接口验证旧失败预留 9203 后选用 9204 且只克隆一次。定向 store/API race 3 轮通过 4.014/2.314 秒，补空清单断言后候选测试 3 轮通过 7.964 秒，完整 HTTP API race 通过 25.161 秒。未修改真实 PVE；集群清单仍是观察快照，外部管理器并发占用由 PVE 拒绝，不能据此证明完整资源所有权/外部竞争验收。失败目标人工恢复与真实建联仍未闭环。

- 2026-09-08（克隆 VMID 请求预留）：正式创建入口改为先持数据库授权变更锁原子保存目标预留，再调用 PVE；已有受管 VMID 或历史克隆目标拒绝被其他请求再次占用，失败/不确定结果仍保留原任务。同一请求通过原指纹返回既有任务，变更指纹拒绝。独立 PostgreSQL 8 路竞争只产生一个所有者，失败重放不新建、他人/已有目标冲突、无多余 Job；3 轮定向 store/API race 分别通过 2.844/3.460 秒，追加 API 冲突不触发 PVE clone 的断言后完整 HTTP API race 通过 22.568 秒。未改真实 PVE。仍需空闲编号自动候选搜索（PVE 持续建议已预留但尚未存在的编号时当前会冲突）、与外部管理器/清单并发的所有权确认，以及失败目标人工恢复；不能将本次请求间预留扩大为完整资源所有权闭环。

- 2026-09-08（克隆访问保护完整回归）：修正后的 `go test -race -p=1 ./internal/store ./internal/httpapi -count=1 -timeout=3m` 全部通过（store 82.603 秒、HTTP API 23.486 秒），包含旧版本升级测试。新增独立 Native 状态矩阵，先绑定固定身份再引入 accepted/running/failed 克隆任务，确认重复绑定与新凭据预留均拒绝且账号保持 revision 0/none/idle；成功任务允许正常绑定与预留。该测试不清除撤权队列、不改 Guest，补充上一轮混合夹具未能单独证明的 Native 授权边界。仍为独立数据库证据，未升级真实验收控制面或运行 Mac；全链路创建/并发 VMID 所有权及迁移后旧会话清理仍待验证。

- 2026-09-08（克隆创建阶段访问保护）：新增迁移 031，以目标 VMID 上未成功的克隆 Job 阻止用户有效授权；Agent 清单、访问、租约及 Guest 签发/完成检查加入同等条件。accepted/running/failed 均拒绝，succeeded 才解除克隆栅栏（其他授权与撤权队列仍须满足）；清单同步与停用写库失败不能绕过。独立 PostgreSQL 的用户/Agent/租约状态矩阵，以及 HTTP GPU 停用写入失败回归通过。首次完整回归发现三个旧版本升级夹具依赖新函数而失败，已将 Agent 查询改为等价直接条件；最终定向克隆与三类升级 race 回归通过（store 2.859 秒、HTTP 2.465 秒），修正后尚未重跑全量。Native 绑定的成功断言遇到夹具清单同步留下的待撤权队列，未清除队列来伪造放行，此轮不宣称新 Native 建联实测。迁移只在随机回归 schema 应用，未升级实机验收库/原开发库。仍需全量回归、迁移后既有会话收敛、VMID 所有权/并发预留和故障任务人工恢复，整体镜像项未关闭。

- 2026-09-08（GPU 克隆停用写库失败回归）：在独立 PostgreSQL schema 对停用 UPDATE 注入异常，确认 GPU 失败后的 Job 保持 running/原 UPID，未因登记失败提前终结。移除测试故障后由新建 Handler 实例从持久 Job 继续收敛，最终实例 disabled、原用户失去有效访问，清单同步不重新启用，终态轮询不再调用 PVE 配置。GPU/验证克隆 race 回归 3 轮通过（3.991 秒），之前单项 3 轮通过（2.920 秒）。此测试也明确证明写库失败回滚期间原实例仍可能 enabled，创建阶段访问隔离尚未完成，不能把可重试收敛当作全程 fail-closed；新 Handler 不等同于真实进程崩溃或多副本故障验收。未修改真实 PVE、验收库或原开发库。

- 2026-09-08（GPU 克隆失败停用修复）：修复克隆完成后 PCI/mdev 配置报错仍登记为启用的问题。故障实例保留供管理员排查，但通过授权变更事务写入 disabled 并使用既有撤权队列；清单重同步不会重新启用。如果登记/停用写入失败，Job 保留 running 以继续收敛，不提前记为终态。独立 PostgreSQL / 模拟 PVE 的 GPU 配置 500 回归确认 Job failed、实例 present/disabled，原分配用户失去有效访问且清单重同步不解除停用。store/HTTP API 全量 race 分别通过 79.275/21.297 秒；最后调整登记失败重试后，定向克隆回归 3 轮通过 4.258 秒。未修改真实 VM；尚需创建中抢先访问、跨副本重复配置、登记故障注入与正式 GPU 桌面验收，不能据此关闭 GPU/镜像整体项。

- 2026-09-08（IMAGE-01 克隆 OS 来源一致性修复）：发现 Job 完成时重读源镜像配置，期间修改 OS 类型可能把已受理的克隆登记到错误平台。新任务保存服务端准入时的 OS 快照，覆盖客户端传入值；完成时使用该快照，旧任务或未注册来源则检查目标 VM 配置，不再依赖可变源配置。独立 PostgreSQL / 模拟 PVE 在真实克隆 API 调用期间修改源配置为 Windows，验证 Debian 克隆仍登记为 Linux、伪造快照被覆盖、镜像不自动提升 ready；克隆相关 race 测试通过（2.125 秒），完整 HTTP API race 回归通过（21.512 秒）。本轮未启动或修改真实 VM。Web 验证克隆入口、构建/配置并发锁、Windows 实建及最终 ready 验收仍未完成，IMAGE-01 保持进行中。

- 2026-09-08（Native 恢复响应写失败回归）：真实隔离 PostgreSQL / 模拟 Guest 的正式恢复 Handler 在提交成功后注入 `io.ErrClosedPipe`，确认响应体未送达；重新 GET 得到预期版本的 `aligned_closed`，旧 POST 再提交返回 409、恢复记录仍只有一条，Guest 未被写入，随后正常 Native 连接使用更高版本。正常响应路径同步保留。NativeRecovery HTTP API race 测试单轮通过（3.083 秒）、重复 5 轮通过（8.871 秒）；身份架构文档补充不确定响应的重新核对与停止自动重试规则。此为 Handler 响应写失败，不是真实 TCP/Ingress 故障或控制面进程崩溃验收；完整跨组件恢复和其他未闭环项继续保留。

- 2026-09-08（Native 恢复事务中断回归）：新增真实 PostgreSQL 的请求取消和专用后端连接终止两种故障测试。隔离 schema 内 AFTER UPDATE 触发器以非事务 sequence 发布该请求 PID，确认已经过版本/防重放写入后才注入中断，不以提交前取消冒充事务中断；终止仅针对该 PID 且再次核对数据库与当前 UPDATE。独立读取证明账号版本未变、恢复记录与新防重放标记均为零，随后同一记录/连接 ID 重试成功。单轮中断测试通过（2.364 秒），全部 NativeRecovery race 测试重复 5 轮通过（11.339 秒）。实机验收库只读仍为 revision 26/revoke/applied；未重启数据库服务器、修改 Guest 或原开发库。此项不覆盖控制面进程崩溃、COMMIT 已成功但 HTTP 响应丢失，以及跨组件整体灾难恢复，相关退出条件仍待验证。

- 2026-09-08（Native 恢复并发与事务失败回归）：在独立 PostgreSQL 回归库新增 8 路并发 CAS 测试，确认只有一个请求成功、账号仅前进一次且恢复记录仅一条；新增 AFTER UPDATE 故障注入，覆盖版本触发器写入凭据防重放标记之后的失败，确认账号和恢复记录回滚，并用同一记录 ID/连接 ID 重试成功，证明没有遗留防重放标记。`go test -race -p=1 ./internal/store -run TestNativeRecovery -count=5 -timeout=2m` 通过（7.664 秒），此前单轮通过（3.232 秒）。只在随机隔离 schema 注入测试触发器，没有修改生产迁移或实际 Guest；实机验收库只读复核仍为 revision 26/revoke/applied。此证据覆盖数据库竞争和事务写失败，不等同于 HTTP 断连、进程崩溃、提交响应丢失或整个灾难恢复矩阵完成。

- 2026-09-08（恢复后正式 Mac 新连接与剪贴板实测）：签名、自包含 Mac 客户端连接隔离恢复控制面，在 Debian 13 VM9203 成功登录，独立 SQL/QGA 确认新凭据为 revision 24、固定 UID 不变。原生复制/粘贴快捷键在 Guest 文本编辑器生成两行测试文本，保存后独立核对 31 字节和 SHA-256；主动断开、返回桌面库后，Guest 复制内容可用原生粘贴进入 Mac 搜索框。退出登录后库收敛为 revision 26/revoke/applied，Native 会话为 0；前两次清理检查尚未收敛，随后独立检查确认账号锁定、无用户/Xorg/会话进程、四项服务正常，正常关机任务 OK、VM stopped，并保存新的关闭状态检查点。Mac 已清空测试登录信息，恢复 infra HTTPS 地址且原生连接检查正常。此轮桌面连接走隔离控制面，不算 HTTPS Gateway/WAN 新验收；新剪贴板版本的正常双向行为通过，不把 GUI 结果扩大为协议自动回声不存在的证明。冷登录黑屏等待、策略恢复版本来源（当前仅核对剪贴板/磁盘映射语义与背景）、并发/中断恢复矩阵仍未闭环，MAC-03/POLICY-01 与整体目标保持进行中。

- 2026-09-08（Native 7→23 隔离恢复实测）：保留恢复前检查点，确认无活动连接/租约/待处理队列或构建后，以新版控制面在 18182 迁移持久化隔离恢复库。真实 GET 核对 VM9203 为库 7/Guest 23，再通过正式 POST 成功返回 `aligned_closed`；独立 SQL 确认库为 revision 23/revoke/applied 且恢复记录精确为 7→23。独立 QGA 将恢复前后完整账号观察作深比较，UID、连接标识、版本、禁用和无进程/登录写入者均未变化；旧请求实际重放返回 409，记录仍为一条。清理确认退出 0、服务正常，随后正常关机；旧备份带回的 4 条隔离测试 Web 会话已清除，Native 会话为 0，保存新的恢复后检查点。测试控制面已恢复服务，Image Builder 暂保持关闭。未改原开发库、VM158、宿主驱动或 Kubernetes；这不是完整灾难恢复验收，尚需恢复后的正式 Mac 登录/新连接、策略版本复核和并发/中断矩阵。

- 2026-09-08（Native 前向版本恢复实现，实机未验收）：迁移 030 与管理员 POST 恢复入口已实现，仅接受同一 Linux 固定身份、库中已撤销/完成、重新观察的 Guest 已撤销且版本领先；需要预期版本、CSRF 和原因，提交时重新检查操作者及桌面未完成工作。账号版本与恢复记录原子提交，普通跳版本/回退仍拒绝，恢复不向 Guest 写密码。独立 PostgreSQL race 验证无记录跳版本拒绝、普通用户拒绝、失败 CAS 回滚记录、重复提交拒绝、记录 UPDATE/DELETE 拒绝、后续版本递增；模拟 Guest 的接口回归覆盖重新观察变化/未撤销拒绝、恢复成功及随后正常连接使用更高版本。尚未部署或向隔离验收库导入，完整并发/中断/实机恢复仍需验证，不能据此恢复生产连接；此前只读诊断是本流程的前置观察而非授权。

  - 包级 `go test -race -p=1 ./internal/store ./internal/httpapi -count=1 -timeout=5m` 退出 0：store 119.516 秒、HTTP API 69.916 秒；使用独立回归数据库，未迁移验收恢复库或原开发库。未配置的 live 测试不在此通过范围内。

- 2026-09-08（Native 恢复前只读核对 API，导入未实现）：新增管理员 `GET /managed-desktops/{vmid}/native-recovery`，在桌面锁内核对已绑定账号和运行中 Guest，只返回用户 ID、库/Guest 版本和封闭状态分类；不输出连接 ID、Guest 用户名、密码或 Token，不修改账号，结果 `no-store` 并记录核对审计。身份不同、无版本、状态不一致及检查失败不会被标记为可恢复；`guest_ahead_closed` 仅是诊断，不是恢复授权。11 个比较用例、匿名路由拒绝、非管理员拒绝、真实独立 PostgreSQL / 模拟 Guest 的无写入与检查失败测试，以及旧库拒绝连接回归均在 race 下通过。尚未部署或实测该 HTTP 接口，恢复意图/导入/提交审计、并发与中断恢复仍待实现，验收控制面继续停用。

- 2026-09-08（恢复库与真实 Guest 版本核对）：控制面保持停用，仅启动已核对名称/NIC/磁盘的 VM9203，QGA 调用正式 Native account inspect 确认 UID 1003、账号禁用、无进程和登录写入者，Guest 为 revision 23/revoked；恢复库为 revision 7/revoke/applied，已证实不一致，不能直接恢复该用户连接。未改密码、UID、Guest 栅栏或数据库版本。新增真实隔离 PostgreSQL / 模拟 Guest 的 HTTP 回归，证明 Guest 领先时新连接返回 409，数据库意图和 Guest 生命周期均不改变（race 通过）；不是版本导入或恢复能力已完成的证明。清理检查明确退出 0、服务正常，VM9203 正常关机任务 OK/status stopped。后续仍需受审计的版本恢复流程，或使用全新隔离实例继续验收，不能靠回退 Guest 栅栏绕过。

- 2026-09-08（数据库回归隔离与重跑）：在持久化恢复容器内另建专用回归数据库，不再使用实机验收库；`go test -race -p=1 ./internal/store ./internal/httpapi -count=1 -timeout=5m` 退出 0，store 87.683 秒、HTTP API 25.095 秒，随机测试 schema 清理后剩余 0。该结果覆盖配置了数据库的包回归，不包括未配置的 live 验收。恢复库保持可连接，18182 控制面继续停用；PVE 只读确认 VM9202/9203 均 stopped，检查点中三条连接为 revoked、撤销队列 revision 1 已完成，Native 账号为 revision 7/revoke/applied。尚不能据旧库证明 Guest 当前版本一致，因此不启动控制面、不重置 Guest 栅栏；后续先做外部状态核对或新隔离实例验收。贡献指南增加测试容量隔离和旧备份恢复约束。

- 2026-09-08（隔离测试库容量故障与检查点恢复，未算全量回归通过）：扩大执行 `go test -race ./internal/store ./internal/httpapi` 时，HTTP API 包通过，store 包因隔离 PostgreSQL 的 256 MiB tmpfs 耗尽而失败，数据库 WAL 恢复再次遇到无空间后退出，非 OOM。已暂停 18182 测试控制面，保留原容器，并用独立持久化 Docker Volume / 新容器在同一隔离端口从 11:40 检查点事务恢复成功（1 个用户、3 个镜像配置、1 个构建任务）。检查点之后的测试会话/策略/审计记录未恢复，必须先核对 Guest/控制面状态再恢复服务；原开发库、真实 PVE VM 和 Kubernetes 未被本轮修改。全量 store 回归需在另建的容量充足测试库重跑，不能将这次环境失败记为通过。

- 2026-09-08（IMAGE-01 克隆源歧义保护）：模板查询在同一个数据库语句快照中计数，多个镜像引用同一 VMID 时返回冲突，不任意选取其状态、OS 或默认 GPU；未配置的零 VMID 不参与解析。普通/验证克隆均返回 `image_template_ambiguous` 409，管理员需先消除重复引用。真实隔离 PostgreSQL + 模拟 PVE 的 race 回归验证 ready/testing 重复引用拒绝、零 VMID 拒绝、解除冲突后的正常完整克隆及幂等；候选镜像 Native/Agent/Web 隔离回归通过。未修改现有配置、真实 VM 或生产数据库。该检查不构成跨请求生命周期锁，Web 入口与并发状态变更仍待继续。

- 2026-09-08（IMAGE-01 验证克隆 API 基础，Web 入口待接线）：`POST /desktop-instances` 新增显式 `validation_image_profile_id`，要求匹配启用中的 testing 镜像及模板 VMID、完整克隆；普通创建拒绝已登记但未 ready/已禁用的镜像。验证任务沿用管理员/CSRF/主体与内容绑定的幂等、Job 和审计，克隆成功不改变镜像 testing 状态。请求自带 PCI/mdev 映射被清除，只能由已启用 GPU Profile 解析。状态矩阵和真实隔离 PostgreSQL + 模拟 PVE 的接口测试通过；同时复验镜像候选不进入 Native/Agent/Web 数据面。本轮未部署新控制面、未创建真实 VM；Web 入口、生命周期并发与实际 PVE 验证仍未闭环。

- 2026-09-08（Mac 剪贴板远端来源防回传，组件验收）：实际 FreeRDP 定时器的新回归先复现远端写入被再次广播的问题，再验证修复后不读取该版本正文、不自动回传；新的远端通知不误清来源标记，本机重新复制仍正常广播。实际原生编辑方法验证主动粘贴远端来源文本仍等待格式确认并发送一次 Ctrl+V。四组剪贴板回归通过，补丁从基线正向应用与当前源码一致、反向探针通过。本条是新版本的组件证据，不替代下条旧版本的 Guest 实机切片；MAC-03 / POLICY-01 仍未闭环。

  - `make macos-check` 退出 0：65 项 Swift/Gateway 测试（27.564 秒）、四种 RDP 重协商、原生剪贴板/编辑与自包含签名校验通过。最新正式包的 Mac FreeRDP 库 SHA-256 为 `0b797a5880a49cfed2b12f25abd2df9b8ef322e33a1a3b956c4abfc5ca4511b9`。长时 soak、GUI 渲染目录测试仍跳过，本次未操作 PVE、Kubernetes 或数据库；新包 Guest 双向复制和主动粘贴需继续实测。

- 2026-09-08（防回传修复前的 Mac 剪贴板允许/禁止实机切片）：

  - 正式签名 `.app` 连接隔离控制面 18182 / Debian 13 VM9203。允许状态下，本机搜索框复制的 `MacPolicySeed1355` 经 Cmd+V 出现在 Mousepad；Guest 连续复制 `GuestPolicyOne1358`、`GuestPolicyTwo1358`，断开后在原生搜索框 Cmd+V 得到第二版。该结果证明最新版本双向链路仍正常，不等于已覆盖所有并发时序。
  - 通过管理 API 保存完整原策略后，仅对 VM9203 禁用 clipboard，revision 2 applied；独立 QGA 确认 `cliprdr=false`、入站/出站限制均为 `all`。重新建联后原生剪切/复制/粘贴菜单禁用；本机 `MacDenied1359` 未进入 Guest，Guest 内部 Ctrl+C/V 成功复制 `GuestDenied1402`，断开后的 Mac 搜索框仍粘贴出原来的 `MacDenied1359`，证明这一链路双向隔离，不能扩展为 Windows/其他重定向通道验收。
  - 实机发现禁止状态下单独 Cmd+V 会在 Guest 输入普通 `v`，与 Ctrl+V 的 Guest 本地粘贴行为不同。随后用实际 MRDPView 和 FreeRDP 输入编码器复现失败；已在原始按键入口拦截被禁用的 Cmd+C/X/V，并按物理键记录和消费对应松开事件，包含先释放 Command 的顺序。普通 `v`、Guest Ctrl+V 及已转发 Command 的释放回归通过。修复后的正式 `.app` 已在相同 VM9203 / 禁止策略 revision 4 下逐个复验 Cmd+V、C、X，空白编辑区无多余字母；普通 `cvx` 和 Guest Ctrl+C/V 正常。保存的两行 `cvx GuestLocal1417` 为 37 字节，独立 QGA SHA-256 `839d127c1db8505c3a0977061a8817195dbff66c927950026c31f20c0a25c2cb` 与预期一致；断开后 Mac 仍粘贴出本机哨兵 `MacBlocked1414`。该缺陷已修复并实测；MAC-03/POLICY-01 的其他条件仍未满足。
  - 修复后 `make macos-check` 全流程退出 0，Swift/Gateway、四组剪贴板/编辑、四种 RDP 重协商和自包含签名验证通过；补丁基线正向应用与实际源码一致，反向探针通过。当前 Mac FreeRDP 库 SHA-256 为 `c43c77fb315fbaa063aa8c421cb2b145acb5d542821af1aa57d7b9d5ef55b1c0`。修复批次未操作 PVE/Kubernetes/数据库；完整 GUI、其他组合键和 Windows 行为仍待验收。
  - 快捷键复验结束后已按快照恢复允许策略，revision 5 applied；原生注销后 QGA 确认 UID 1003 锁定、用户/Xorg/xrdp-sesexec 无残留且服务正常。VM9203 正常关机任务 OK、状态 stopped；客户端已清空测试用户名并恢复 infra 网关，连接检查正常。保留其他 GUI/跨端与策略组合的未完成状态，不重复扩大本次通过范围。

- 2026-09-08（Mac 原生编辑响应与确认后粘贴；Debian 双向复制、菜单与快捷键切片已实测）：

  - `freerdp-mac-edit-actions.patch` 在实际 MRDPView 实现 `copy:/cut:/selectAll:` 和原生菜单验证，发送 Ctrl+C/X/A。先释放已转发的 Shift/Control/Option/Command，不切换 Caps Lock/Num Lock；修饰键释放失败不继续操作，发送失败仍尝试释放 Ctrl。复制/剪切受剪贴板策略和通道就绪约束，断线全部拒绝；全选不要求跨设备剪贴板权限。终端 Ctrl+Shift+C 等应用语义仍待实测。
  - `paste:` 绑定触发时的本机剪贴板版本，等待对应出站快照确认后才发送一次 Ctrl+V。确认回调通过持有通道取消对象的主队列块推进，不捕获原始上下文；定时器仅按实际确认状态推进/超时，不猜测成功。内容变化、焦点离开、断线、空/不支持内容和策略禁止均不发送旧粘贴；8 秒单调时钟预算取消动作，但保留协议在途状态，防止迟到 ACK 被误认。拒绝/超时当前为系统提示音，完整可访问反馈和双向所有权仍需补齐。
  - 新测试使用真实 MRDPView/FreeRDP 编码器与私有活动上下文，记录输入回调，不创建窗口、读取系统剪贴板或连接网络。测试依赖修正后先在旧库明确失败 `native edit responder is missing`；新库通过三个动作、菜单允许/拒绝、精确按键顺序、残留修饰键、四个发送失败位置、释放失败、策略禁止与断线断言。RDP 内部头与 AppKit 的 HTTP 枚举冲突用单独 C 测试单元隔离。初版补丁缺少前导上下文，修正后新归档顺序应用及全部反向幂等探针通过。
  - 新增粘贴回归先在旧库明确失败 `confirmed native paste responder is missing`。新库通过确认前无按键、确认后一次 V、准确保留 `a_b|c 中文` 出站数据、重复请求、版本/焦点变化、空内容及禁止时不读剪贴板；纯状态测试覆盖过期、拒绝及迟到确认，实际 `Clipboard.m` 测试确认主线程交接与交接前断线。私有焦点/剪贴板夹具不代表真实 AppKit 焦点或 Guest 粘贴。空快照的 WinPR 数据读取返回无数据并产生格式诊断，断言确认没有发送 V。
  - 最新 `make macos-check` 全流程退出 0：65 项 Swift/Gateway、3 suites、27.435 秒，四种 RDP 重协商、剪贴板/粘贴、显示/诊断与自包含签名验证通过；新归档及反向幂等探针通过。当前 Mac FreeRDP 库 SHA-256 `c6e25852d7198480b68cfe4bbcd23858422a2d34cdfde74976c4aaa29e12602a`，Bridge `3fea98fca8f5802d58489f1bd69433a99b3c6d3f5f56aa6f544cfaf13cc37224`。构建前正常退出已登出的 App；粘贴版本尚未进行 GUI/Guest 验收，未操作 PVE/Kubernetes，独立长时 soak 仍跳过。隔离控制面 18182 与一次性 PostgreSQL 已重新只读确认存活，不因旧进程号改变而重启服务。
  - 随后使用同一正式签名 `.app`、隔离控制面 18182 与 Debian 13 克隆 VM9203 实测：Mac→Guest 的 `VC Workspace clipboard: a_b|c 中文 20260908` 保存为 45 字节文件，Guest QGA 读取 SHA-256 `d2cc1e75488495cd89a4df5dc55f859f00ca525bab191616aeef55714bb88bc4` 与预期 UTF-8 完全一致。自动化粘贴报告剪贴板读取超时，但实际文件证明内容已到达；不能把工具超时当作传输失败。中文在当前小尺寸编辑器中存在字形可视性疑点，字节正确不代表字体体验通过。
  - 在 Guest 新输入独立文本 `GuestReturn1316` 后用 Mac 复制快捷键复制，断开后通过 Cmd+V 在原生桌面搜索框得到相同文本，证明 Guest→Mac 路径；再次连接保留两个 Mousepad 标签。原生“剪切”菜单让选区变为空，但随后“粘贴”菜单尚未观察到文本恢复，保留为未通过，不能将双向复制证据扩展为完整菜单验收。工具直接键入文件名还出现句点丢失，需与输入注入路径分开归因。
  - 已通过原生客户端注销；独立 QGA 检查确认固定 UID 1003 的受管账号锁定，UID 进程、Xorg、xrdp-sesexec 均无残留，四个 Guest 服务/定时器仍 active，命令退出 0 且无截断。随后仅对 VM9203 正常关机，PVE 任务返回 OK 且 VM 状态 stopped；不修改 VM158、宿主驱动或原开发库。客户端清除测试用户名并恢复 `https://ws.infra.plz.ac`，原生连接检查显示服务器连接正常。
  - 下一步验证 Linux/Windows 的快速连续操作、内容版本/焦点取消、策略拒绝与双向所有权，补齐超时恢复反馈。MAC-03/POLICY-01 与总目标继续开放。
  - 带诊断的正式签名 `.app` 再次连接隔离 VM9203，原生菜单“剪切→粘贴”和 Cmd+X→Cmd+V 均恢复 `MenuCycle1333`；通过编辑器保存后，独立 QGA 检查为 13 字节、SHA-256 `5b4760e0f496b89f08c32483e32beadc134faa822efe8f4ffb34f18463477d46`、完整文本一致，非截图推断。当前运行无粘贴拒绝日志。上一轮动作后立即截图为空不足以证明失败，本轮未复现；仍需快速连续操作、版本/焦点竞争及 Windows 矩阵，不能宣称间歇问题全部排除。
  - 正式运行日志只接受字母数字函数名，原先 Objective-C block 的静态提示会被过滤；新增精确调用点、格式和完整文本白名单，仅输出 1–5 的粘贴原因编号，不透传任意日志内容。拒绝未知函数、格式、附加文本及空参数的测试通过；重新打包、自包含签名验证与四组剪贴板/编辑测试通过。没有因此放宽现有日志脱敏规则。
  - 排查增加真实 `Clipboard.m` 的非文本格式回归，证明仅图片的 offer 已由底层拒绝请求格式 0，且后续文本仍正常请求；没有按这个被排除的猜测改动协议。增加初始失焦时不读剪贴板、不发布、不发送按键的真实 MRDPView 回归。原因编号分别对应策略/通道不可用、初始焦点拒绝、等待中焦点/就绪变化、版本/确认/超时取消这一组原因和空快照；四组剪贴板/编辑测试及补丁反向探针通过。
  - 连续远端通知的回归先在旧实现明确失败：文本响应已排队、随后收到新的空格式列表，旧任务仍写入本机剪贴板。取消对象新增单调远端 generation，格式列表在替换前推进版本，排队写入只在同版本且通道仍有效时执行；版本检查和写入共用锁，溢出关闭取消对象而非复用旧版本。真实 `Clipboard.m` 回归、新版本可写/旧版本和关闭后不可写的 Foundation 断言、四组剪贴板测试及补丁反向探针通过。该版本检查覆盖“响应已经排队后又收到新通知”的边界；在途响应处理和构建结果见下项，实机竞争验收仍未完成。
  - 随后的在途回归在旧实现明确失败于 `incomingRequests == 1`：连续通知会发出多个无请求编号的数据请求。已改为单个在途并固定请求格式/远端版本，新通知仅保留最新格式；旧响应排空且丢弃后请求最新版，中间版本不再请求。解码不读取已被后续通知替换的格式映射；无在途响应忽略，规范空失败响应结束当前请求但不返回通道错误，畸形响应仍拒绝。通道销毁释放格式列表并重置在途状态，分配失败后的空列表安全释放。真实回调覆盖三次通知合并、旧/新内容、无在途重复响应、失败响应/旧失败推进、空列表取代及发送失败恢复，全部通过。
  - 两批竞争修复已重新打包进签名 `.app`。`make macos-check` 退出 0，65 项 Swift/Gateway、3 suites、27.152 秒及四种 RDP 重协商通过；最终空指针释放防护后另行重新构建、四组剪贴板测试与严格签名检查通过。补丁可从基线正向应用并与实际源文件一致，反向探针通过。仍未进行该版本的 Guest GUI 竞争实测；本机内容抢占、远端内容往返反射、无响应恢复及 Windows 矩阵保留未完成，不以串行请求回归替代完整剪贴板所有权验收。本轮不启停 PVE VM、不操作 Kubernetes 或数据库。
  - 本机抢占回归先在旧实现失败：远端响应排队后，用户复制的新本机内容被旧任务覆盖。请求现在绑定本机复制代次，主线程写回前复核真实 change count；外部变更推进代次并拒绝旧请求，客户端自己的远端写回只更新已知计数，不误判为本机新复制。初始未知、外部变更、自己写回、旧请求拒绝和后续新远端复制恢复的 Foundation/真实回调测试通过，四组剪贴板测试退出 0；三份补丁从基线正向应用与实际源码一致，反向探针通过。已重新构建签名 `.app`，但未进行该版本的 Guest GUI/跨应用竞争实测；检查后写入并非 macOS 跨进程原子 CAS，不能声称消除全部竞争。远端内容往返反射、无响应恢复、Windows 和完整策略矩阵仍未完成。本批未操作 PVE/Kubernetes/数据库。

- 2026-09-08（Mac 控制面连接检查）：

  - 登录页和原生设置共用 `idle/checking/succeeded/failed` 状态；仅主动检查显示结果，启动探测不添加成功标签。地址变化取消请求并清空旧结果/能力，回调同时校验请求身份和地址；成功只保存对应地址。检查失败不再把已登录用户切为 signedOut，也不覆盖已有会话错误。失败文案按超时、网络、地址、证书分类，不直接展示底层或服务端原始消息。设计规则同步至 `docs/design/ui.md`。
  - 最新源码完整执行 `make macos-check`，退出 0：65 项 Swift/Gateway 测试、3 suites、27.094 秒（含新增 6 项连接检查回归）、自包含 Release 签名验证、剪贴板/显示/诊断及四种 RDP 重协商用例通过；独立长时 soak 仍跳过。新主程序 SHA-256 `dd9dbd0dad1424df6f065fc15d21d9bb2389f8e813a15896300f74ba0bb22e71`。原生渲染目录另单独执行通过（9.129 秒），新增登录/设置成功与失败 × 浅深色 × 680×460/1000×700 共 16 个渲染单元，人工抽查可见反馈和换行；680×460 登录展开时结果位于滚动区下方，未完成该尺寸真实滚动验收，不把首屏截图当作全部可达性证明。
  - 正式 `.app` 正常退出后重建并实际启动：登录页与设置页均验证 `https://ws.infra.plz.ac` 成功、改为本机不可达端点时旧结果立即消失、回车检查失败、恢复正式地址后重试成功。实际截图显示设置窗口内结果完整可见；登录用户名→密码的 Tab 顺序及地址回车通过。当前系统 Tab 未遍历按钮，完整键盘/VoiceOver/系统外观切换矩阵仍未验收，未更改用户系统偏好。结束时关闭设置、收起登录配置、清空凭证字段，保留正式服务器地址，未登录或连接 Guest。
  - infra 仅只读复核：四个 Deployment 全部就绪、五个 Certificate Ready，`/api/v1/system` HTTPS 200/TLS verify 0；未新增本机 CA 信任、修改集群或启动 VM。本次客户端健康检查不是新增真实桌面或 WAN 验收，总目标继续进行。

- 2026-09-08（Mac 剪贴板回归汇总；原生编辑与实机验收未完成）：

  - 输入对照：同一个正式 `.app` 的本地用户名框中，`typeText('a_b|c')` 实际为 `abc`，显式按键为 `a_b｜c`（全角管道），本地原生粘贴为准确的 `a_b|c`。不能把先前远程符号缺失直接归因于 RDP，输入法及真实远程键盘仍待验收。已清空输入并恢复 `https://ws.infra.plz.ac`。实际包内 `MRDPView` 缺少 copy/cut/paste/selectAll 响应，嵌入子类也未补齐；仍不能以固定等待或逐字输入代替原生粘贴。
  - 策略和生命周期：先在旧库复现禁止重定向仍读取本机剪贴板；现于读取 change count/内容前检查策略及就绪条件。每通道独立取消对象串行保护读取/发送、写回与销毁；排队块和定时器持有该对象，不在关闭后访问原始上下文。主队列安装/恢复/清理定时器，重复恢复不重复安装，旧清理按对象身份匹配；暂停后仍可恢复当前通道。
  - 内容与方向隔离：先复现清空后不发布新格式，以及远端转换覆盖本机待发送缓冲区。现每个本机版本替换旧格式，空/不支持/无效 UTF-8 发布零格式列表，过滤 WinPR 内部格式 0；发送失败不消费版本号。远端解码改用独立临时 Clipboard，成功、拒绝及断线组合下均保持原本机文本不变；缺失/矛盾标志和有长度却无载荷的响应拒绝。它保护的是转换缓冲区，不代表双向所有权和所有迟到响应已闭环。
  - 协议确认：每通道只允许一个在途格式列表，等待期间本机轮询不替换已发布内容；拒绝、畸形或未确认时，数据请求返回零长度失败，无对应请求的确认忽略。[MS-RDPECLIP §3.1.5.4.3](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpeclip/dc95d66a-9b5f-4ec2-8878-357c623fcf4c) 要求拒绝列表后的请求失败，未确认也拒绝是本实现的保守边界。不用计时器猜测成功或重置同一通道的确认身份。
  - 自动证据：`check-clipboard-read-policy.sh` 的真实 MRDPView 测试覆盖九种策略/就绪状态、关闭后无效上下文、重复恢复/新旧通道、精确中英文和符号、空/不支持/无效内容、发送重试及在途内容冻结；实际编译的 `Clipboard.m` 覆盖四种允许/关闭组合、转换不覆盖出站内容、异常载荷、确认矩阵及 12 次数据响应。新增回归先复现响应发送失败仍返回成功，现向上传递；空格式列表也必须确认，确认发送失败不继续请求数据。取消对象另通过在途关闭与 AddressSanitizer。私有视图/剪贴板夹具不创建窗口或操作系统剪贴板，无真实 Guest 验收；曾误用 MIME 作为 UTI，修为 `public.data` 后重测，未放宽断言。
  - 完整构建：修复光标补丁上下文重叠，未改运行逻辑；新归档/重复补丁探针通过。随后正常退出已注销的客户端，实际完成 `make macos-check`：Release 重建、自包含/macOS 14/深度签名、剪贴板/显示/诊断、59 项 Swift/Gateway 测试（2 suites，27.824 秒）及四种真实 RDP 重协商用例全部通过；离屏目录与独立长时 soak 仍显式跳过，未冒充通过。新包主程序 SHA-256 `7a5163b8afa628386a87071e4791cd59b9209808a9fdc644932040db9a8b47de`，Mac FreeRDP 库 `c82e4f566fab5e7465be9ebb84327686a644313d07382bfc38859f5130f6823c`，Bridge `578df19da94fb8f690875b13051483cb34b9443f7339303ab11aab20fd5522cb`。
  - 实际启动与范围：重建的签名 `.app` 已通过正常 GUI 启动，本地用户名框原生粘贴准确为 `a_b|c`；随后清空并收起设置，服务器仍为 `https://ws.infra.plz.ac`，未提交登录或连接 Guest。当时发现登录页检查缺少成功反馈，后续修复与实测见上方连接检查记录。这组回归未启动 VM、改变集群/系统信任或原开发库；期间 infra 只读就绪证据不等于已部署本地新源码。
  - 仍需完成：原生编辑响应与粘贴动作关联、无确认的超时反馈/恢复、双向所有权切换和迟到数据处理、Linux/Windows 真实双向复制粘贴与菜单/键盘矩阵；不能据以上组件测试宣称 Guest 剪贴板已清空或完整复制体验已通过。MAC-03/POLICY-01 与总目标保持进行中。

- 2026-09-08（Web → Debian 模板 → 独立克隆 → Mac 实测）：

  - 在一次性 PostgreSQL 029 库、本机 Builder 与仅允许 VM9202/9203 的 PVE Token 上，从真实 Web 表单提交 Debian 13；软件源为 `http://10.31.0.2/debian` 和 `/debian-security`，安装 HTTP 明确绑定本机 `10.31.0.128:18840`。任务 `job_sVBmTO0ypx5BgV0nSbils_Rj` 于 `10:56:42–11:18:42 CST` 完成，无人工处理安装器；infra-node6 的 VM9202 正常转成停止的 PVE 模板，Web 自动进入 `testing`。未改 VM158、VM160、原开发库、宿主驱动、Kubernetes 或 Mac 信任。
  - 用同一受限 Token 完整克隆至空闲 VM9203（2 核 / 3072 MiB / 32 GiB，无 GPU），clone/start 均 `OK`；固定 MAC 核对到 `.171`。首次启动的 cloud-init 无错误，QGA/xrdp/sesman/Agent/账号回收定时器 active，构建账号锁定且一次性 sudoers 不存在。xrdp `+vcw1` 的实际 daemon 摘要和包回执与下方构建记录一致，Native/Agent 登录栅栏能力齐全；PAM conffile 因正式登录栅栏安装而改变，未把它当作全包零差异。首次只读审计夹具误用了回执路径，修正为 `/usr/share/vc-workspace/rdp-runtime.json` 后重测通过，没有修补 Guest。
  - 正式签名 Mac `.app` 通过隔离的回环控制面直连这个克隆，由默认 Broker 创建 UID1003；不是本轮新增 Gateway/HTTPS 验收。首次 `11:28:45` 在首帧前以 `0x00020006` 失败，自动复验权限后恢复，首帧 1.000 秒。随后约 4 分 44 秒连接内，实际终端读回 DPI192、`5120×2678 → 5120×2804 → 5120×2678`，鼠标和普通文本输入通过；主动断开后重连首帧 0.750 秒，原终端内容保留且再次输入成功。新模板内置补丁通过这一小段全屏往返，不等于长期稳定性。首轮背景在重绘后才完整出现；自动化 `typeText` 的下划线/管道未按预期送达，`paste` 超时且终端出现单个 `v`。尚未区分注入工具、客户端键盘映射与 Guest 配置，不宣称这些问题已修复，IMAGE-01/MAC-03 保留开放。
  - 实测修复三类产品回归：PVE 空清单返回 `null` 导致 Web 白屏，现固定返回数组；已受理构建立即更新对应镜像状态/VMID 并禁用编辑；窄屏长日志的 flex 子项增加宽度约束。另统一 Web/Native/MCP/启停/清单同步的镜像资源排除，当前模板 VMID 及未完成/失败构建目标不能进入桌面数据面，即使存在旧分配；正常继承 `template` 标签的克隆不误删。失败构建的 VMID 继续保守保留，清理/复用需后续显式流程。
  - 真库回归覆盖旧分配、三种未闭环构建状态、普通用户/Web/Native/MCP 清单和拒绝启停；Go 全量 test/vet、相关包 race、Web 36 项及正式构建通过。正式 Web 产物在真实构建/完成状态下，中英 × 明暗逐宽度读取 320–1280px，根节点无溢出；端点尺寸截图、键盘打开/取消对话框及焦点返回通过。浏览器只读接口不能等待两次 animation frame，因此逐宽度结果仅为同步几何读回，不算动效稳定性证明；首帧闪烁、对比度自动审计及完整键盘矩阵未测。
  - 收尾由 Mac 正常注销触发产品回收：三张 Connection 全部 revoked，Native revision7 revoke/applied，独立 QGA 确认 UID1003 账号锁定、用户进程/Xorg/sesexec 均为空，服务仍 active。VM9203 于 `11:39:02 CST` 正常关机 `OK`，保留新模板与克隆供后续复现，均未标记 ready；一次性数据库已保存私有逻辑归档并验证可列出目录，尚未做这份归档的恢复演练。Mac 地址恢复 `https://ws.infra.plz.ac`，用户名/密码清空；infra 所有 Deployment 和 5 张 Certificate 就绪，默认 kube context 未切换。本轮新源码修复只运行于隔离控制面，尚未替换 infra 镜像 Digest。

- 2026-09-08（Debian 正式配方的 xrdp 补丁包接线与 Packer 校验）：

  - 原先通过 Mac 实机的光标重激活修复已接入新 Debian 13 配方：Builder 从运维指定的本地目录读取固定 `xrdp.deb` 和规范 SHA-256 sidecar，启动时固定摘要、构建前复核；Web 不接受任意产物路径。Guest 在 root 私有目录重新校验上传内容、准确包名/版本/架构与原系统版本，再经 APT 安装并严格执行 `dpkg --verify`，写入运行时回执；不降级未知/更新版本，也不用于热更新已占用桌面。
  - 经 `10.31.0.2` 的已签名 Debian APT 源重新构建实际 amd64 包 `0.10.1-3.1+deb13u2+vcw1`，xrdp 19 项测试通过。包摘要 `19965782ee83325e5e6e940f0ba067ceb72ed283e0bb9fa89a76fc37ee654088`；daemon 摘要 `2fb80fbe9a752e32a848a1ef1dbf95822fb96e7b01da1c8d2d8a8e4dc528f99f` 与前次 Mac/PVE 验证一致。隔离 Debian 13 amd64 容器实际安装、错误摘要拒绝、重复安装拒绝和全包完整性检查通过。首次容器验收因 slim 排除文档文件失败，仅修正容器夹具以匹配桌面安装语义，没有弱化生产完整性验证；容器未运行图形桌面，不能据此关闭 IMAGE-01。
  - 真实 `packer validate` 发现指定网卡与非空绑定地址互斥，已修正配置默认值并加入回归。安装 HTTP 服务默认限制为 `8840–8847/TCP`，可明确选择 IPv4 地址或网卡及至多 32 个端口。新增 `make images-packer-check` 对实际配方及插件验证，Debian 默认/IP/网卡和 Windows 10/11 五组均通过；与 HCL syntax-only 区分，该检查没有 PVE 凭据或 VM 创建。Go Builder/Config race、安装器 5 项边界测试及 `make images-check` 通过。
  - 此处是产物与配置验证；后续 Web/PVE/Mac 实建结果见上方同日批次。infra Kubernetes Builder 仍关闭；专用 Worker、安装网络路由、Windows 新模板及剩余 P0–P2 条件继续开放。

- 2026-09-07（Guest 光标补丁、Mac 实机回归与持续桌面流证书轮换）：

  - 基于精确 Debian 安全更新源包 `xrdp 0.10.1-3.1+deb13u2` 构建 `+vcw1` 候选，保留 Debian 安全补丁和签名仓库校验，另固定两个源码归档的 SHA-256。Guest 在重置前保存当前光标，重置期间保存新图像并抑制旧缓存引用，静态光标重新发送后再发送当前动态图像；不关闭 FreeRDP 缓存重建或无效更新拒绝。构建/安装边界见 [xrdp 候选补丁](../../deploy/images/debian-13-xfce/xrdp/README.md)，当前不自动进入默认模板。
  - 首次 amd64 Debian 包完整构建、`make check` 通过（xrdp 子套件 18 项）；新增“不发生后端更新时仍重发原动态图像”后，同一已构建阶段实际重跑为 19 项、零失败/跳过。四个新增用例调用正式 WM/cache 源码，覆盖 32 次重置、重置中鼠标移动/后端变化、静态/动态图像、容量变化和无效索引；新旧测试构建中的 daemon SHA-256 均为 `2fb80fbe9a752e32a848a1ef1dbf95822fb96e7b01da1c8d2d8a8e4dc528f99f`。两次真实 FreeRDP 协议门禁、59 项 Swift/Gateway 测试、自包含/签名/macOS 14 检查、镜像语法及 10 项 Kubernetes 离线测试通过；opt-in 渲染目录/独立长时 soak 未运行，不算通过。
  - 在空闲 VM160 上通过哈希/版本守卫暂装候选 daemon；正式 `.app` 从 `https://ws.infra.plz.ac` 经默认 Broker/Gateway 新建独立 Guest UID1006。首条连接从 `16:18:09.934` 至菜单主动断开 `16:34:32.434 CST`，约 16 分 22 秒，首帧 0.750 秒；期间两轮全屏往返及拖动缩小/系统最大化共 6 次尺寸变化，实际读回 `5120×2804`、`5120×2678`、`3978×2156` 和 DPI192，重复终端输入成功。诊断记录确认重置后补发动态图像 2，不再引用未发送的旧槽 3。该结果不是 WAN 性能分布或长期稳定性证明。
  - 在同一条桌面连接中，将客户端 Certificate revision 2→3，显式推广于 `08:22:39.379Z` 开始、`08:24:49.963Z` 完成（130.584 秒）；旧 Keep-Alive 拒绝，新活动指纹为 `af6ec4f4d3826e905c1551bc5268f0018c706266395027d20b3af82d1617222c`。随后逐一将控制面/Gateway 服务 Certificate revision 2→3，全新可信 TLS 1.3 握手分别核对叶指纹 `7b200eafbad758dc2b907576d9bde50572cd7e9009a78332d43b2275e8963816`、`b34ada4e6634bef1481eb9b8470845cc55c12265127903e83f54b355eb8f2170`。控制面 UID `0613394d-bfb9-4905-9a30-1fab6bb83377`、Gateway UID `d29cff28-701b-473b-9510-836755a0f127` 和零重启均保持不变；轮换后实际键鼠和终端读回成功，仍只有一张已消费且活动的票据。根 CA/公开证书未轮换，Mac 系统信任未修改，完成记录已去除私钥。
  - 从 macOS 原生“连接”菜单断开、点击“重新连接”，保留同一 UID1006、Xorg PID4820、XFCE PID4886/ticks338870 和原终端内容；新连接首帧 0.751 秒，追加一轮全屏往返及实际输入通过，共 8 次显示尺寸变化。部分全屏坐标操作被自动化服务以 `noWindowsAvailable` 拒绝，未计为鼠标验收；原生 AX、键盘及返回窗口后的实际点击仍可用，没有证据认定 Mac 锁屏或传输故障。设置页检查 HTTPS 成功，但完整键盘/主题/动效矩阵未完成。Guest 确认 Intel HD Graphics 530、Accelerated yes，不等于 H.264 硬件编码。
  - 正常 API 撤权后，客户端显示“桌面不可用”；Guest revision4 revoked/disabled，进程与登录写入者为空，两张已消费票据均关闭、活动为 0。一次只读观察遇到并发锁 EAGAIN，确认无待决 QGA 后再观察成功，未重复撤权。清理移除自有账号/Home/临时服务，恢复 Agent/PAM、七项配置及原 xrdp daemon；独立检查原 SHA-256 `c47ea4813ba8d5da33f589760766d594ba800fab17162ea7ee2b1bdcad7a00e5`、两个 xrdp 服务 active、自有备份/UID 进程/Xorg/sesexec 不存在。平台测试身份禁用并保留审计，临时密码清空，Mac 回到登录页。VM160 正常关机任务 `OK`、stopped/4096 MiB，GVT-g 与网卡未改；所有 Kubernetes Pod 就绪，默认 AWS context 未切换。未操作 VM158、宿主驱动或原开发库。MAC-03/NET-01/DEPLOY-01 与总目标继续开放。

- 2026-09-07（Mac 全屏断线实机归因）：

  - 解锁后实际重新启动签名 `.app`，通过系统信任 HTTPS、正式 Broker/Gateway 登录隔离 VM160 的新受管用户 UID1005；当前 DHCP `.168` 经固定 MAC 核验后以精确 `/32` 维护，无活动隧道时两项 rollout 完成。最大化 `5120×2678`、全屏 `5120×2804`、XFCE DPI192 由该用户实际会话独立读回；终端键鼠输入成功。Guest OpenGL 显示 Mesa Intel HD Graphics 530 / Accelerated yes，这不是 H.264 硬件编码验收。
  - 两次实际缩放后的点击分别在首帧后约 120.5 秒、148.5 秒失败。新增数值白名单记录表明，第二次退出全屏后缓存重建为 26 项，仅重发光标 1、0；`15:40:13.750 CST` 随后收到旧编号 3 的 Cached Pointer，类型 10 回调失败。xrdp `0.10.1-3.1+deb13u2` 在重置缓存后保留 `wm->screen->pointer`，鼠标移动会引用旧槽；不是 Gateway 首先断开或 Guest Xorg 崩溃。两次归因连接保留同一 Xorg PID1438、XFCE PID1504/ticks49857。
  - Native 已将该未取消接收失败报告为 `0x00020002`，真实 UI 显示连接失败，验证了此前错误分类修复；不代表协议根因已经修复。新增缓存创建/写入/缺失数值诊断，不记录图像、地址或凭据；正式 Release 重建和四种真实 FreeRDP 重协商协议用例通过。最后一次 Return 操作触发的新连接不计为自动恢复证据。
  - 正常 API 撤权后，三张已消费票据均关闭、活动为 0；Guest revision7 revoked、账号锁定且进程/登录写入者为空。一次只读检查遇到并发锁 EAGAIN，复查后才继续清理；已删除自有账号/Home，恢复 Agent/PAM、服务和七项配置。该批次仅完成归因；后续补丁与真实复测见上方批次，不能用诊断日志本身替代修复证据。

- 2026-09-07（infra 分阶段证书推广与真实续期）：

  - 新增环境专用 `infra-certificates.mjs`，支持只读 status/verify、首次活动 Secret 初始化及显式 promote。cert-manager 签发 Secret 与活动客户端 Secret 分离；双指纹先经运行中控制面实际 mTLS 验证，随后切活动材料、确认投影与 Gateway readiness，再撤销旧指纹。固定资源身份、UID/resourceVersion 条件写入、本地单操作者锁及私有进度记录限制误覆盖；完成记录移除旧/新私钥。未知状态拒绝，不自动回退旧凭据。流程与恢复边界只在 [Kubernetes 证书轮换](../operations/kubernetes.md#证书轮换) 维护。
  - 确认活动隧道为 0 后，构建并推送私有 Harbor amd64 镜像，仅条件更新控制面和 Gateway 两个 Deployment。控制面 Digest 为 `sha256:0a3d449b8244a474aa891dad1b85dc5b8054d53a6e3a1e36b265e9f726f1c258`，Gateway 为 `sha256:4e9df8b7b043a53bdd573801d0056a00fe90580251934db237ab46fc3ddb1824`；两个 rollout 成功，原配置保留私有恢复记录。没有重新执行整个 Bootstrap、升级原开发库或改动其他工作负载。
  - 使用项目缓存中的官方 cmctl v2.5.0，明确 infra context/namespace，单独触发客户端 Certificate revision 1→2。候选指纹已更新而活动身份仍保持旧值时，公开 HTTPS/WSS smoke 继续通过。实际 promote 于 `07:08:26.874Z` 开始、`07:10:48.125Z` 完成（141.251 秒）；新身份和 Gateway readiness 成功，旧客户端原 Keep-Alive 返回 403。控制面 Pod UID `b4b060a8-f053-4172-a469-1e9d7e7eb428` 与 Gateway UID `7e338246-71f4-4ddf-a779-bd70fe93d8ab` 均未变，重启计数都是 0；重复 promote 确认为无操作，完成记录不再含密钥材料。
  - 随后只触发两端内部服务 Certificate revision 1→2。全新、验证链和服务主机名的 TLS 1.3 握手分别观察到控制面叶 `9ccd54c60e1567e8c271f0f937d0596bcf5c11f62d523f2b82b104bd82bb3e28`、Gateway 叶 `c312ac9abc4f6d715cadae5c0c7ed15b7e830ea7809862ab0c4318151a54d99d`，与当前 Secret 一致；两 Pod 仍不重启。再次公开边缘 smoke 通过，5 个 Certificate Ready，Web/MCP/PG 保持原 Pod。根 CA 与公开 Let's Encrypt Certificate 未轮换，Mac 系统信任未修改。
  - Kubernetes 离线 10 项（含真实临时 OpenSSL 证书签名/用途/密钥拒绝）、Gateway/Config race（15.797 / 1.583 秒）、语法与 diff 检查通过。实机测试只做 readiness/证书握手与无效票据，不签发 OS 凭据或运行 PVE/Guest 操作，不能算持续桌面流下的无中断轮换。剩余 CA、后台推广告警、强制中断与过期应急等仍留在 DEPLOY-01；总目标未完成，默认 context 仍为原 AWS 集群。

- 2026-09-07（Gateway 证书/CA 热重载与保留隧道回归）：

  - 两个实际 TLS 监听器改为从固定文件路径加载新证书/密钥/CA；控制面每次请求重新验证当前 CA、用途、期限和登记叶指纹，旧 Keep-Alive 不保留已撤销身份。Gateway 客户端在材料变更时切换 HTTP 池，控制响应仍检查实际服务证书期限；不跳过链/主机名校验、不重放业务请求。文件损坏或不匹配拒绝当前操作，恢复后无需进程重启。
  - 临时私有目录通过真正的 `..data` 符号链接替换模拟 Secret 投影，真实 mTLS 双指纹与双 CA 过渡、撤销旧指纹/CA 后的原连接拒绝、新监听证书、坏文件与过期材料、128 次并发请求通过。单条 WSS→回环 TCP 跨轮换继续续租、双向数据与正常关闭；授权只消费一次。前三次专项连续回归通过（5.056 秒），追加实际环境加载器/服务接线和到期 Keep-Alive 服务证书拒绝后，Gateway/Config race 两轮通过（27.728 / 2.090 秒）。控制后端是模型，没有把这组测试算作真库、cert-manager 或 OS 桌面验收。
  - Go 全量 test/vet 通过，未提供真库或 Guest opt-in，本轮不增加这些集成项的实测证据。infra 只读检查确认控制面/Gateway 各 1 副本、Web/MCP 各 2 副本可用，5 个 Certificate Ready；未构建或替换集群 Digest，默认 context 仍为原 AWS 集群。本轮未启动 PVE VM、修改系统信任、证书资源或原开发库。
  - 目标集群当前仍是启动加载版本；直接挂载 cert-manager 客户端 Secret 会在新指纹登记前启用新身份，必须先补签发/活动材料分离、受控推广与失败恢复，再做真实轮换。源码热重载不等于无中断自动续期。运行边界统一维护于 [Kubernetes 证书轮换](../operations/kubernetes.md#证书轮换)，DEPLOY-01 与总目标继续进行。

- 2026-09-07（无需 GUI 的真实 RDP 重协商与错误分类修复）：

  - 新增 `make macos-rdp-reactivation-check`，加载当前 `.app` 内的 FreeRDP client/peer，以匿名 socket、内存临时 TLS 证书及精确证书比较完成真实握手；不使用 AppKit、Gateway、OS 登录或测试 VM。正常序列通过连续 32 次分辨率重协商，66 次光标创建、99 次设置和 66 次释放；缓存没有越过本连接生命周期保留。
  - 故意在重协商后引用尚未重发的缓存光标，实际触发 `update_pointer_cached`→FastPath→接收栈失败，诊断白名单正确识别类型 10。该序列与实机接收栈相符，但没有实机更新类型证据，仍不能确定二者同因。对照的 xrdp [resize 流程](https://github.com/neutrinolabs/xrdp/blob/v0.10.1/xrdp/xrdp_mm.c) 也会重置/重新发送光标，所以没有据猜测取消缓存重建、关闭解码检查或忽略无效更新。
  - 自动测试先证明回调失败后 LastError 仍为 0，新增失败分类断言在原运行库中确实失败。现仅对未取消且没有具体错误码的接收失败补 `ERRCONNECT_CONNECT_UNDEFINED`，让 Bridge 返回失败而非正常关闭；原鉴权/网络等特定错误不覆盖，主动取消保持 CANCELLED。四种协议测试另连续三轮通过，Swift 网络重连白名单显式拒绝这个未知错误，不靠自动重试掩盖故障。更新后的完整 Swift/Gateway 互操作汇总 59 个测试/2 个 suite、27.273 秒通过（离屏目录及独立长传输仍显式跳过）。这是断线状态分类修复，不是断线根因修复。
  - 该入口已接入本机 `macos-check` 与 macOS CI 配置，远端 Actions 未运行。完整 `make macos-check` 已实际通过，包括自包含 macOS 14/深度签名、59 个 Swift/Gateway 测试（27.455 秒，前述两个 opt-in 仍跳过）和四种真实 RDP 协议用例。最终重建主程序 SHA-256 `35c481a094076bc203c2ac740a626e6e851a6384273bd9e6bff96a05bc9160a0`，FreeRDP 库 `b3ea8f9fd1b27b9aafaf7a9979b89b0ca72d6ee94fba75d512d62196b4e3db8e`。本轮不启动 PVE VM、不改变 Kubernetes、系统信任或开发数据库；Mac 锁屏后的真实全屏/输入复测仍待进行。总目标及 NET-01/MAC-03 保持未完成。

- 2026-09-07（Gateway 长传输、DHCP 漂移与 Mac 断线归因）：

  - 新增 `make macos-gateway-soak-check`：正式 Swift 隧道与 Go WSS/Relay 在同一授权连接中往返 120 MiB，SHA-256 校验、慢消费者背压以及 60 秒空闲后的继续传输通过，实际 164.888 秒；只授权/关闭一次。它没有 FreeRDP 图形解码或真实 WAN 注入，不据此关闭弱网/全屏 TODO。
  - VM160 本次启动后 DHCP 由 `.166` 变为 `.167`，旧精确路由使首次尝试在 Guest 账号创建前失败。新增受控目标维护入口，验证固定 VM/4096 MiB/MAC 与唯一 IPv4、无活动隧道及自有资源后，更新两个精确 `/32` 并重启控制面/Gateway，实际 rollout 和系统受信任 HTTPS/WSS smoke 通过。没有开放整个内网或改变 Guest 网卡配置；后续发放授权前增加地址一致性检查。边界和恢复方法见 [Kubernetes 维护](../operations/kubernetes.md#infra-环境的部署与维护)。
  - 重复实测夹具新增 `prepare-next`，保留已清理轮次和 UID 墓碑，临时设置隔离 Guest 的 `UID_MIN=1004`；正式 Broker 实际创建 UID1004，而不是复用历史 UID1003 或由夹具预建 OS 用户。初始 SQL 字段错误在创建身份前即拒绝，已修正。三张新票据均实际消费；前两次连接保留同一 Xorg PID1467，撤权后第三次建立新 Xorg PID3455、UID 保持不变。
  - 首次桌面连接在全屏后再次失败：`13:58:34.251 CST` 的首个原生日志为 `fastpath_recv_update:508`，随后沿接收栈退出，Native `error=0 / firstFrame=1`，Gateway 随后按 transport 收尾。这将本次故障收窄到客户端更新回调，尚不能确定具体更新类型。重连后 5120×2678→5120×2804→5120×2678→5120×2804、Terminal `xrandr` 和鼠标输入均成功，约 218 秒后由正常授权 API 主动撤权结束；不能用此轮成功覆盖首次失败。
  - 增加缓存光标缺失的固定日志点，以及只匹配完整已知 FastPath 格式、仅输出数值类型的诊断白名单；拒绝额外正文、错误格式和不一致类型。C 原生格式/WinPR 格式各 12 条断言通过，不记录票据、密码、端点或画面。缓存重建只是待验证线索，未改变解码失败行为或以自动重连掩盖问题。上游 [缓存重建变更](https://github.com/FreeRDP/FreeRDP/pull/13196) 是定位参考，不是本项目根因已确认的证据。
  - 最终 Release、自包含 macOS 14/深度签名检查、24 条显示断言与 24 条诊断断言通过；使用最终 Bridge 的 `make macos-gateway-check` 汇总 59 个测试/2 个 suite、27.323 秒通过，离屏视觉目录和独立长传输用例在此命令中仍显式跳过，长传输另有上述实际通过证据。Kubernetes 离线 6 项、Gateway race（11.956 秒）通过。最终主程序 SHA-256 `b63b0abb8498151fed3b0f5698ed7d8839389925edf0bbc3fa1c049bf66bd706`，Bridge `3db5c335e675db8627e87029c8bd0fb9abf1e32138b0bc1768a3e9e25c78d476`；最后的数值诊断版本只做了本机互操作，因 Mac 锁屏未重新走 OS 图形验收。
  - 两次真实撤权均由产品完成 Guest 进程回收；最后 revision6 已撤销/账号禁用、登录写入者与进程为空，三张已消费隧道全部关闭、活动数为零。一次只读检查因并发锁返回 `EAGAIN`，没有重发状态变更；完成后新检查通过。已删除本轮自有账号/Home/临时服务和 Guest 私有备份，恢复 Agent/PAM 及七项配置（含 `/etc/login.defs`），独立复核无 Xorg、sesexec 或该账号残留；平台身份禁用并保留审计，临时密码清空。VM160 正常关机任务 `OK`、状态 stopped/4096 MiB，GVT-g 和网卡配置不变；Kubernetes 服务保持就绪，默认 context 未切换。未操作 VM158、宿主驱动或原开发库。MAC-03/NET-01 与全部 P0–P2 总目标保持未完成，继续 GUI 复测需手动解锁 Mac。

- 2026-09-07（正式 Mac → infra Gateway → Debian 桌面）：

  - 真实签名、自包含 `.app` 使用系统受信任的 `https://ws.infra.plz.ac`，经默认 Broker 创建独立 Guest 账号并签发一次性票据；没有手工制造票据、启动外部 RDP 应用或安装本机 CA。隔离 VM160 的 xrdp 记录确认实际连接来源为 Gateway 所在节点 `10.31.0.115`，不是 Mac；内层 Guest TLS 保持指纹校验，不提供直连回退。
  - 实测定位并修复客户端时间校验错误：服务器仅领先 33 毫秒也会拒绝新描述符。签发时间合理性检查现容忍最多 5 秒正向偏差，但不延长票据/会话过期时间、10 秒握手上限或服务端租约；新增 33/168 毫秒、5 秒边界、越界及过期拒绝回归。
  - 本轮 7 张票据中 5 张实际消费，5 次 RDP 均回到同一 Guest UID 1003、Xorg PID 2126 和 XFCE PID 2197/启动 ticks 136434。通过原生窗口连接、Terminal 输入、全屏及退出全屏；Guest `xrandr` 独立确认窗口 `5120×2678`、全屏 `5120×2804`。通过应用内“断开连接”及“重新连接”保留原 Terminal 和 OS 会话，不以新建空桌面代替恢复。
  - **稳定性未通过完整验收**：前三次成功建联分别在全屏后意外结束，Native 为 `error=0x00000000 / firstFrame=1`，后两次网关分类为 `transport`。xrdp 已完成显示尺寸更新，现有证据不能确定客户端、代理或 Guest 中哪一层先断开。随后一次多分钟全屏往返成功也不能覆盖这些失败。新增双向传输错误分类、WSS 关闭码及 FreeRDP 编译期函数/行号日志，禁止记录 peer 错误正文、票据、密码或桌面内容；下一批按 NET-01 / MAC-03 继续归因。
  - 正常 API 于 `05:20:10.407299 UTC` 撤销分配，网关在 `05:20:11.724441 UTC` 记录关闭（本次约 1.3 秒，不是 SLA），客户端显示“桌面不可用”。Guest 随后确认 revision 14 已撤销、账号锁定且登录写入者/桌面进程清空；数据库 7 条 Connection 均 revoked 且已关闭，5 条已消费隧道均关闭、活跃数 0，Guest 撤权队列完成 1/1。
  - 最终回归发现新增底层诊断改变了主动关闭的错误类型，已将 DispatchIO `ECANCELED` 保持为正常关闭；也移除诊断时间转整数的极值陷阱，补上极端日期拒绝回归。修复后 `make macos-gateway-check` 汇总 58 个测试/2 个 suite 通过（27.941 秒，离屏 UI 目录仍显式跳过，不计视觉验收）；Gateway race 通过（11.868 秒），Release 重建、24 条显示断言、自包含依赖/签名检查、Kubernetes 离线 5 项与真实 HTTPS/WSS smoke 均通过。最终 Release 主程序 SHA-256 为 `0b83aeb9fb691cca2b316f799f91489080f070ea6031c025a10a694c0dc04c88`；实际 OS 图形验收对应收尾修正前的 `5aa6a78b8138f2b089e8458dadd870696420733b6aa305189b37cd47bff7d6f8`，收尾版本仅重新做本机互操作，没有再次启动 Guest。
  - 测试仅临时安装本轮 Agent/PAM、服务和受管策略；通过固定 UID、文件摘要/权限和进程观察核对后，已删除自有 OS 账号/Home、临时服务及 Guest 私有备份，恢复原 Agent、PAM 与六个配置/背景文件。平台测试身份禁用并保留审计，私有状态中的临时密码已清空。VM160 正常关机任务 `OK`，回到 stopped/4096 MiB，GVT-g 配置未变；未操作业务 VM158、宿主驱动或原 migration 013 开发库。Kubernetes 部署保留，默认 context 未切换。

- 2026-09-07（infra Kubernetes 真实部署与受信任 HTTPS）：

  - 按部署方明确授权，仅使用 `kubernetes-admin@infra.homelab`，新建独立 `vc-workspace` 命名空间与 Ceph `csi-rbd-sc` 10 GiB PVC。Web/MCP 各 2、控制面/Gateway/数据库各 1 个 Pod 健康；数据库从空库迁移至 029，没有连接或升级原本机开发库。PVE、Harbor 和应用秘密仅通过私有状态与 Secret 注入，未推送 Git 或公开镜像。
  - cert-manager 既有 `cloudflare` ClusterIssuer 完成 `ws.infra.plz.ac` 的 Let's Encrypt 签发；系统信任 HTTPS 200、HTTP 308、API ready 200、匿名 MCP 401。Ingress 的实际 Nginx 配置独立确认上游内部 CA/主机名校验、TLS 1.3 与精确 WSS 路由；真实 WSS 已协商 `vc-workspace-rdp.v1`，无效票据断流。`/internal/gateway/v1/ready` 及无效网关路径为 404，不再误回 SPA 200。
  - 构建实测发现 Apple Silicon 直接执行 amd64 构建阶段的 `exec format error`，四个 Dockerfile 改为 BUILDPLATFORM 原生构建、Go TARGETOS/TARGETARCH 交叉编译。控制面、MCP、Gateway、Web 和 PostgreSQL 均验证 amd64，并以私有 Harbor 不可变 Digest 固定在 Overlay；修复初次部署的网关 ID 校验失败。控制面首次等待 headless PostgreSQL 就绪的两次启动失败随后自动恢复。
  - Gateway 的 `/ready` 通过真实 mTLS 与数据库；Calico 实测 Gateway→控制面 8444 / VM160 3389 可达、Gateway→数据库拒绝，Web→控制接口/数据库/Guest RDP 均拒绝。控制面外部出口收窄至 PVE Ingress HTTPS，Gateway 只放行 VM160 `/32`。专用权限分离 PVE Token 允许 VM160 配置读取、拒绝 VM158（403），不授予 VM 启停/克隆/配置权限；Harbor Robot 仅可 pull 本项目，二者有效期 90 天。
  - 正式 `.build/app/VC Workspace.app` 已在真实 UI 将服务器切换到新 HTTPS 域名，完成本地账号登录及退出。未增加系统 CA、未关闭证书校验。该账号未分配 OS 桌面，Gateway 票据数为 0；这证明 HTTPS 登录链路，不是 `.app`→Broker→Gateway→Guest 桌面的验收。
  - custom-format 数据库备份 116,948 字节成功恢复到无 PVC/Service、仅 Unix socket 的独立临时 PostgreSQL：29 个迁移、1 用户、1 受管桌面、0 Gateway 票据。源 PostgreSQL Pod 替换后使用同一 Ceph PVC，重新登录 201、桌面清单 200，账号与注册表保留。恢复检查 Pod 已删除，本地备份保留于 0700/0600 的 `.cache/infra-bootstrap/`，未覆盖在线库；这不是 PITR 或跨版本回滚验收。
  - 离线门禁新增 5 个配置/权限测试，并修复原 `kubectl create --dry-run=client` 仍进行当前集群 API discovery 的问题，改为真正离线渲染与结构检查。操作说明统一维护于 [Kubernetes](../operations/kubernetes.md)。完整 OS 建联、Guest 防绕过、Windows 路径、内部证书重载/轮换、持续备份和升级回滚继续开放；P0–P2 总目标未完成。
  - 最终 Go 全量 test/vet 和 diff 检查通过（未启用新的 PostgreSQL 集成测试环境，不把 skipped 的真库测试算作本轮通过）。VM160 正常关机任务 `OK`，恢复 stopped/4096 MiB；9112/9113 仍 stopped，158 仍 running/3072 MiB。本批未安装 Guest 服务、创建 OS 账号或修改宿主驱动；Kubernetes 部署保留运行，默认 kubectl context 未切换。

- 2026-09-07（macOS 进程内 Gateway 与内层 Guest TLS）：

  - Mac 新增 `rdp-gateway` 能力声明和异步 WSS 建联。先校验连接策略、凭据、固定路径/子协议、规范票据与期限；系统信任的 TLS 1.3、一次性兑换和 `ready` 成功后才启动原生 RDP。禁用 Cookie、共享凭据、缓存及重定向。匿名 socket pair 接入官方 FreeRDP 传输，无本机 TCP 监听器、外部应用或直连回退；双向有界背压、EOF、取消及异常帧统一关闭。
  - Bridge 升至 ABI v5，应用和 dylib 必须成套升级。内层 TLS 严格校验实际叶证书 DER SHA-256 和有效期，禁用 TOFU/历史指纹接受、明文降级、服务器跳转与上游认证弹窗；安全失败使用固定文案且不自动恢复。真实内嵌 FreeRDP 经正式 WSS Transport/Relay 对本机 TLS peer 实测：正确指纹进入首个 RDP 应用数据，错误指纹拒绝且应用数据字节数为零，两者均确认 Guest socket 关闭。peer 不提供 OS 桌面，该证据不能扩大为 PVE 图形/登录验收。
  - 新增 `make macos-gateway-check` 并串入 macOS 完整检查/CI。启用 Go race 的本机互操作通过 256 KiB 双向数据、空闲续租跨越票据/握手期限、撤权、票据重放、异常子协议/ready/数据帧、拒绝重定向/未受信 CA、无效策略前置拒绝和取消。测试 CA 只走 DEBUG 限回环的链/主机名验证入口，不安装系统信任；Release 不包含该入口。普通 Swift 单测跳过 opt-in 套件不能算验收。
  - 全量 Swift 含网关互操作最终连续两轮通过（runner 每轮汇总 57 个测试，离屏渲染目录明确未启用）；新增异步会话在取消后迟到时只销毁旧 transport/释放旧 Connection、不替换新会话。回归曾发现测试模拟接口串线，以及测试子进程跨线程 `waitUntilExit` 偶发不返回，已分别改为每用例独立路由和主 RunLoop 上有界观察后复验。Go 全量 test/vet、Gateway 两轮 race、Release 自包含构建、24 条显示断言及深度签名/依赖验证通过；CI YAML 与 diff 检查通过，远端 CI 尚未运行。本批未启动一次性 PostgreSQL，因此不把默认跳过的数据库集成计为新一轮数据库验收。
  - 原开发库只读复核仍为 migration 013；本批不访问/更改 PVE VM，不安装 Guest、不改宿主驱动、不重启原服务、不启用原部署 Gateway。Mac 真正 `.app`→Broker→Gateway→PVE 桌面、Guest 防绕过、Windows 稳定凭据、Kubernetes 和其余 P0–P2 项继续开放，总目标未完成。

- 2026-09-07（Native Broker 原子网关签票与 Guest 证书实测）：

  - Native Connection 默认 HTTP 入口支持显式、强制的 Gateway 路由，使用已登记 ID、固定 HTTPS 来源及私有 CIDR；未配置时保留原路径。旧客户端缺少协议声明在 Guest 操作前返回 426；越界目标、错误证书及 Windows 路径拒绝，不回退直连。新描述符为 `rdp-gateway`，绑定同一可信 Guest 指纹、已应用策略与一次性票据；客户端请求中的端点/证书不构成授权来源。
  - Linux 通过固定 QGA 命令对 Guest 自己的 RDP 监听器执行无凭据 TLS 握手，校验返回的 DER/有效期/用途，不用磁盘证书或网络 TOFU 建立信任。隔离 Debian 13 VM 160 的产品探测于真实 PVE/QGA 路径通过，指纹 `523fcb50de643c11c1bbada7539884b35912a347df3f7083f276729e49688936` 与独立配置读取一致；前后账号/配置基线不变，没有 Xorg 登录或 Guest 安装。入口为 `make pve-live-gateway-certificate-check`，该证据不是完整 macOS/RDP 数据面验收。
  - 修正接线中的事务缺口：OS 凭据回执、Connection 和唯一票据/审计同事务提交。票据插入或审计失败、在途策略变更/Native 注销均不留下部分连接或票据；更高 revision 的持久化恢复意图及后台撤销回归通过。新增未兑换票据真实 30 秒到期回收、已关闭/租约失效连接的后台凭据退役，普通关闭保留桌面；回收与兑换/续租共用授权锁。分钟级后台回收不冒充立即 OS 撤权。
  - Store / HTTP / Gateway / Guest 相关两轮 race、扩展 `make gateway-check` 两轮，以及带一次性 PostgreSQL 的根 Go 模块全量 race 通过，包含嵌入 Python 的真实 TLS、CredSSP 协商、降级/损坏/停滞拒绝；vet、OpenAPI 解析/引用、根 Go 可达漏洞扫描与控制面容器构建通过。最终控制面镜像在无网络、只读、非特权环境中确认不完整 Native 路由配置会退出，不启动直连回退。Broker 测试的 Guest 回执仍是模拟，实机测试只验证证书观察；不能将两类证据拼成已经完成的客户端闭环。
  - VM 160 已正常关机，PVE 任务 `OK`，恢复 stopped/4096 MiB；9112/9113 仍 stopped/8192 MiB，业务 158 的 XFCE PID 52975 与启动时刻不变。测试后非系统 schema 数量为零，无持久卷的一次性数据库 `vcw-gateway-broker-db-20260907` 已销毁，原开发库仍为 013；未更换 Guest、调整宿主驱动或重启原开发服务。macOS 隧道/严格证书验证、Guest 网络隔离、Windows 稳定凭据与 Kubernetes 等剩余项继续开放；本批不启用原部署的 Gateway 路由，NET-01 与 P0–P2 总目标保持未完成。

- 2026-09-07（独立 Gateway TLS 服务与真实加密链路）：

  - 新增 `apps/session-gateway`、只接收已验证且已登记客户端叶证书的专用控制监听器，以及独立 Gateway OpenAPI。控制监听器需显式完整配置及数据库才启用，不挂入公开 `/api/v1`，不沿用 MCP 内部 Token。控制请求只允许兑换/续租/关闭既有票据，Gateway ID 取自实际 mTLS 身份，不能提交任意目标。登记支持同 ID 两个证书用于轮换；拒绝重复映射和额外/重复请求字段，Keep-Alive 上每次请求仍检查证书期限。
  - 对客户端提供 TLS 1.3 / HTTP/1.1 WebSocket，拒绝 URL 查询、Origin/Cookie/Authorization 和不匹配 Host。3 秒内的首个 47 字节文本帧提交票据；兑换已提交且 Guest TCP 已建立后才返回 `ready`，然后只转发至多 64 KiB 的二进制消息。握手前限制 socket 数量，关闭采用即时关闭并收回已升级连接；控制面故障、迟到结果和不回应关闭帧的客户端不能延长租约。代理必须 TLS 透传或重新加密，不能给该进程发送明文上游。
  - 隔离 CA 的真实 TLS/mTLS/WSS 验证通过：无/未登记/错误 CA/错误用途证书、伪造认证头、证书到期后旧连接继续请求、HTTP 重定向、非法响应/消息、超时、双向字节和显式服务退出。一次性 PostgreSQL→mTLS→WSS→本机 TCP 联合测试通过有效票据、重放前置拒绝、Native 注销后的实际加密连接断开，以及独立数据库核对单次关闭/审计。运行函数也实测加载证书/登记文件、真实监听、就绪检查和关闭；没有拨号 PVE Guest，也不是 RDP/WAN/macOS `.app` 验收。
  - Store / HTTP / Gateway / Config 两轮 race、扩展 `make gateway-check` 两轮通过；最终依赖升级后，带一次性 PostgreSQL 的根 Go 模块全量 `-race -count=1`、vet、两个 OpenAPI 的解析/引用和 diff 检查通过。本机 arm64 的 Gateway 与控制面容器均重新构建；最终 Gateway 镜像以 65532 运行，保留新增 WebSocket 依赖许可证，无网络、只读、无额外 capability 的启动检查确认缺失配置会退出，不降级到明文。
  - 根 Go 模块将 `golang.org/x/crypto` 从 0.55.0 升至 0.56.0，处理扫描发现的两条 SSH 模块通告；使用当前 Go 工具链源码运行固定版本 `govulncheck@v1.7.0`，最终未发现可达符号或已导入包级漏洞，CI 同步加入此门禁。仍有未被当前根模块导入的 OpenPGP 模块级通告 [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932)，不能宣称项目零漏洞；该扫描不覆盖嵌套 Terraform 模块、Rust、Swift、前端或容器系统包。
  - 回归后测试 schema 数量为零，无持久卷的一次性数据库容器 `vcw-gateway-tls-db-20260907` 已销毁；原开发库仍为 `013_desktop_authorization.sql`。只读核对 9112/9113 为 stopped/8192 MiB、160 为 stopped/4096 MiB，业务 158 的 XFCE PID 52975 与启动时刻不变。本批未操作 PVE 虚拟机、安装 Guest、重启原开发服务或修改宿主驱动。
  - 默认 Broker 签票、可信 Guest 证书发现、macOS 内嵌隧道、Guest 网络不可绕过和 Kubernetes 路由尚未接线。Windows DPAPI 的已知失败不变，版本化 Windows Native/MCP 新路径继续关闭。NET-01 和全部 P0–P2 总目标保持未完成，配置及协议边界见 [Session Gateway](../architecture/session-gateway.md)。

- 2026-09-07（Session Gateway 授权与实际 TCP 转发内核）：

  - 新增 029 迁移和 Store 接口，连接在创建时保留不可变的发起 Native Session 摘要；未知旧来源拒绝补绑。一次性随机票据只存摘要，最长 30 秒且受原会话期限限制，绑定 Gateway、受管私有 IPv4/TCP 3389、证书和已应用策略 revision。两个 Store 的 24 个并发兑换只有一次成功，消耗与审计同事务；同用户其他设备、重放、旧策略、待撤权、失效授权及租约复活均拒绝。
  - 新增 `internal/gateway` 实际双向 TCP relay：显式 CIDR 白名单与容量上限、固定端点、以本地单调时钟扣除 RPC 耗时、最长 5 秒租约、双 socket deadline 和独立到期计时器。撤权、续租失败/卡住、迟到响应、目标/证书/策略改变、取消和 EOF 均关闭连接；网络关闭先于数据库收尾，后者失败不能假报成功。没有新增公开监听器、HTTP 服务身份、macOS 数据面或 Kubernetes 网关部署，默认 RDP 直连未改变。
  - 独立 PostgreSQL 17 + 真实本机 TCP 验证 Native 注销、撤分配、策略变更和 Authority 连接池不可用实际断开，不只是数据库状态变更；Store / HTTP / Gateway 的 `-race -count=2` 联合回归通过。补充票据过期独立断言，使用数据库自身时钟验证 30 秒 TTL，不将 Docker 与宿主绝对时钟比较当作授权依据。CI 增加 Gateway race；`make gateway-check` 缺少测试数据库会明确失败。该证据不是 TLS/RDP/WAN/macOS 实机验收，不能据此宣称生产 5 秒撤权 SLA。
  - 最终 `make gateway-check` 的 Store / relay 两轮 race 通过，包含停滞拨号和堵塞写端的期限收尾；后两类另连续五轮通过。真实断线断言按 5 秒租约观察 socket，数据库关闭审计另有 1 秒收尾期限，不把半租期续租时间误当作 3 秒 SLA。Go 全量 test/vet、Rust workspace 和 diff 空白检查通过。迁移只应用于一次性测试库的随机 schema，完成后的 schema 数量为零；无持久卷的测试数据库容器已销毁，原开发库仍为 013。架构、接线和验证边界统一见 [Session Gateway](../architecture/session-gateway.md)。NET-01 与全部 P0–P2 总目标保持未完成。

- 2026-09-07（Windows LSA 登录拒绝权改密候选）：

  - Windows 10 VM 9112 的随机新 SID 在保持账号启用的情况下，先添加交互、远程交互、网络、批处理和服务五类直接 LSA 拒绝权，再持旧密码改密。旧/新密码的五种 `LogonUserW` 类型均返回登录类型未授权；移除自身权利后新密码可登录，但原 DPAPI 密文仍为 `NTE_BAD_KEY_STATE`，故该候选失败且未启用。远程交互拒绝权已设置，但本轮没有实际 RDP/WTS 登录断言，不能把五种 `LogonUserW` 验证扩大为所有入口。
  - 测试拒绝接管已有 LSA 账号，操作前后独立枚举全局 LSA 账号与拒绝权成员并确认基线恢复；整轮 SAM/所有权/ProfileList 也恢复一致。原生 54、Agent 17、Native SAM 6 项和 Profile 防重置保护通过，三个资料保持候选仍失败；完整结果为 170.48 秒 FAIL。EXE SHA-256 为 `4d33a2a74c5fa739df0ce8cf02147f734d5660a01b323829e1bcefe1fab629dd`，没有更换已安装 Guest。
  - 默认版本化 Windows Native Broker/MCP 继续关闭。Gateway 提前作为短期授权隔离的依赖推进，但不能替代稳定 OS 凭据账本、重启恢复和真实资料/桌面验收，也不能通过临时开放已撤销旧密码来通过测试。Windows 11 未重复执行这个新失败候选；旧的 10/11 实测边界不变。
  - 收尾只读复核：9112/9113 均 stopped/8192 MiB，160 stopped/4096 MiB；业务 158 保持 running，XFCE PID 52975、`Fri Sep 4 09:41:39 2026` 启动时间不变。未改宿主驱动、原开发服务或业务资料。

- 2026-09-07（Windows Profile/DPAPI 数据完整性缺口与保护性修复）：

  - 在 Windows 10 VM 9112、Windows 11 VM 9113 为随机固定 SID 创建真正的 Profile，通过 `LogonUserW`、`LoadUserProfileW` 和用户 DPAPI 验证资料，不再只验证 SAM 密码。已有 Profile 的实验 Native `issue/retire` 现于修改前明确拒绝不安全的重置；真实退役拒绝、撤销、自然到期、再次签发拒绝及原密文保留在两个 OS 均通过。为证明数据没有损坏，夹具在撤销后仅对自己的随机身份临时恢复原密码登录；这不是产品重连成功的证据。
  - 改密方案尚未通过：启用账号持旧密码改密的对照可读；先禁用或设置过去的账号到期时间后改密，虽能用新密码登录，原数据均返回 DPAPI `NTE_BAD_KEY_STATE`。SYSTEM 线程模拟以及真实用户主进程的 `CryptUpdateProtectedState` 都报告迁移计数，但之后的新登录仍不能读取原密文。没有降低数据断言、删除密钥或将这些候选接入生产；完整 Profile 套件继续判失败。Windows 10 最后一轮为 183.13 秒，Windows 11 首轮为 179.86 秒；其中常规原生 54 项、Agent 17 项和 Native SAM 6 项均通过，不代表整体通过。
  - 新增 SYSTEM 用户范围 DPAPI 密文封装原语，绑定固定用户名/SID、所有权 nonce 与 revision，拒绝串用、损坏及线程模拟调用，不序列化明文。其真实 Windows 测试已通过，但尚未接入持久化凭据账本、崩溃恢复或正式签发。默认版本化 Windows Native Broker/MCP 保持关闭，旧未绑定路径没有在本批改造；REVIEW-12 及全部 P0–P2 总目标仍未完成。后续必须同时满足短期授权隔离、保留资料、凭据库/重启和真实客户端条件，不能通过短暂重新开放已泄露的旧凭据来绕过本问题。
  - Profile 验收新增单独 `make pve-live-windows-profile-check` 入口及完整 SAM/所有权/ProfileList 清理基线。Windows 11 首轮捕获 `DeleteProfileW` 返回成功后留下一个测试用户的 WinX 快捷方式，保留 SAM/账本并使基线检查失败；独立恢复核对固定 SID/所有权、无登录进程和 Profile 路径未改绑，只删除该文件及空父目录，再删除自身账号/账本，原基线已恢复一致。夹具失败收尾另补禁用保护，不能保留仍可登录的测试账号。操作边界与复跑方法见 [PVE 验收说明](../operations/pve-development.md#windows-原生账号与-profile-验收)。
  - 修正后的 Windows 11 复跑为 114.17 秒：54+17+6 项及 Profile 保护用例通过，已实测保留账号禁用不改变密文；两个 DPAPI 候选仍失败，因此整轮仍为失败。收尾独立 SAM/所有权/ProfileList 基线一致，没有把人工清理计为此次自动收尾成功。Windows 10/11 对照 EXE 为 `985e4f2460372f4cd82443c8b23d03c0d21a88794622d06c2d856d3da9600668`，最后 Windows 11 的夹具修正版为 `d1c176c3058a580661b4b80c0d9ffcf6375a85238b445686fc737df586504dcc`；Guest release `7e8ed71ef98c1c34c88910ea688267f1a152143f8d9fded7456145284d2166b6` 已交叉构建但未替换已安装 Guest。本机 Rust workspace、Windows-target Clippy、Go 全量 test/vet、Computer race 与严格基线/回执矩阵通过；没有新数据库或跨重启凭据验收。
  - 结束后再次独立核对最后一轮 34 个账号/所有权根、5 个 Profile 和两个上传目录均无残留，Windows 11 基线恢复为 `8b44b5febae15264018e8d849db4faa748aa6ff264e4475aa5645e6ae72abaa9`。恢复检查启动的 VM 9113 已正常关机，任务 `OK`；9112/9113 均 stopped/8192 MiB，VM 160 stopped/4096 MiB。业务 VM 158 的 XFCE PID 52975/启动时刻不变，原开发库仍为 migration 013；未改宿主驱动、原服务或业务资料。

- 2026-09-07（Windows Native 固定 SID 凭据执行端与真实 SAM 回归）：

  - 新增与 Agent 分离的 Native 所有权根、`NativeLifecycle` 单调账本和三个 Guest 命令。签发、退役、撤销先持久化意图后修改真实 SAM；退役不主动注销 WTS，也不刷新原期限。未知旧 `vcw`、同名换 SID、混入 Agent 账本、迟到写入和未提交重放均拒绝。服务主循环与显式巡检共用双命名空间执行入口，一侧失败不跳过另一侧。
  - 修复复查发现的 Agent 损坏生命周期回收缺口：固定 SID 可证明时，本地巡检仍禁用/注销该账号；保留损坏历史并报告失败，不重置版本或触碰同名替代用户。Native 使用相同失败处理原则。
  - 隔离 Windows 10 VM 9112 与 Windows 11 VM 9113 均通过 53 项常规原生测试、17 项 Agent 账号测试和新增 6 项 Native 测试。Native 覆盖真实 `LogonUserW` 新旧密码验证、退役后保持启用/期限、过期回收、删除后 SID 墓碑、命名空间与并发门，以及签发/退役在实际写入前后直接退出子进程的四个检查点；恢复不依赖 Rust 析构。夹具仅网络登录，不创建交互 Profile，因此不是 macOS/WTS 保留桌面或 DPAPI 验收。
  - 最终原生测试 EXE 为 `1f92fe752bb778b1f144a42e1262e317d0c2b3db1a54bbde7d5b199005f3b53e`。Windows 10 完整复跑通过（123.76 秒）；Windows 11 一次复核的 76 项测试均通过，但收尾基线读取遇到 QGA `PID lld does not exist`，整轮判失败（144.56 秒），没有将其计为完整通过。新增仅针对只读基线的三次有界重试和严格 JSON 验证，明确状态变化/非零退出不重试，也不重发账号写入；相关错误、超时、取消和无效基线 race 两轮通过。独立确认该轮 29 个账号/注册表和目录无残留后，Windows 11 同一 EXE 重跑通过（47.88 秒，不含开关机）；再次独立核对两轮 58 个精确身份和两个目录均无残留，基线摘要与重跑前一致。
  - Windows Guest release `f172b94bb12a81562b3fbc9c9e508ae6f993d5caffe5f6479f4b3aad9e218559` 交叉构建、Windows-target Clippy、本机 Rust workspace、Go 全量 test/vet 与 diff 检查通过。实测使用独立测试 EXE，不替换已安装 Guest；原开发库仍为 migration 013，无新库迁移。未改宿主驱动或业务 VM 158，其 XFCE PID 52975/启动时刻不变。
  - 临时测试账号/注册表/上传目录已删除，未触及用户业务资料；恢复核对所启动的 VM 9113 最终正常关机任务 `OK`，独立状态为 stopped。VM 9112/9113 均保留原 8192 MiB，VM 160 保持停止和 4096 MiB；没有遗留本轮测试服务或端口监听。
  - 新增明确的资料持久性验收边界：SYSTEM 密码重置与用户持旧密码改密不同，可能影响 DPAPI；须设计并实测受保护凭据跨轮换、注销后新登录及重启仍可读取，不能清除 Profile/密钥来通过测试。原理与官方来源见[身份架构](../architecture/identity.md#native-账号写入版本)。Windows 默认版本化 Native Broker/MCP 继续关闭，不能把这些 SAM 测试或 WTS 空列表当成完整登录域、服务恢复与客户端验收。REVIEW-12 和全部 P0–P2 总目标继续进行。

- 2026-09-07（默认 HTTP→真实 macOS 发布版的保留桌面、撤权与注销）：

  - 使用独立 ARM64 PostgreSQL 17 容器及新库（迁移至 028）、正常 `apps/control-plane` 主程序和正式 API 初始化管理员/普通用户，仅分配隔离 Debian 13 VM 160。导入该机已独立核验的现有会话策略与失效 epoch 下限，不清除 Guest 撤销历史；原开发库仍为 013。最初误用本地 AMD64 PostgreSQL 镜像，启动即失败，已删除其精确容器/卷后改用 ARM64；该失败不计产品通过。
  - 实际 `.app` 通过本地账号登录、卡片连接和桌面内鼠标/键盘输入。普通断开后 Guest 为 `retired`，重新签发后仍接入 Xorg PID 1796、XFCE PID 1861 和终端 PID 2158，终端测试输出保留。在线撤分配两轮分别约 18/20 秒收敛；恢复授权后同 UID 1003 建立新桌面，无人工清理失败标记。最后先断开保留真实终端，再从客户端注销，约 48 秒后正式队列收敛至 Native revision 11 `revoke/applied`。每次撤销均在测试夹具清理前独立确认账号禁用、UID 进程与登录写入者全部关闭、捕获的 Xorg 锁/socket 已消失。
  - 实测发现并修复两处客户端错误：主动断开不再宣称“远程会话已经结束”；非自动重试的传输结束后只读复核当前桌面权限，撤权显示“桌面不可用”且不提供无效重试，返回库没有旧卡片。控制面不可达不推断授权或网络原因；401 清理本地会话；独立检查标识、Token/服务器绑定及取消保护阻止迟到结果覆盖重试、返回、注销或关窗。修复版已重新构建后在真实 `.app` 再验，不仅是测试替身。最终应用 SHA-256 `32649a089777b2b5848a41d1dcf4aa43be9cd5108bca2feddc8b355dadd4b79e`，Bridge `e8bff95edc017a77d25c382e066746cbb875ab2d97dc16677582cab01e339265`。
  - Guest 实际窗口/全屏/退出全屏分辨率为 5120×2678→5120×2804→5120×2678，DPI 192，画面与原终端保留。默认 Native 身份只有自身组及 `render`，真实 `glxinfo -B` 返回 Intel HD Graphics 530、`Accelerated: yes`；不代表 GPU 视频编码、性能对照、跨 1×/2× 屏幕或完整显示矩阵通过。Swift 全量汇总 41 项通过（展示目录截图用例默认跳过），增加 200/401/503 与迟到结果矩阵；原生显示断言 24 项、自包含 macOS 14 包和签名校验通过。
  - 私有数据库最终 5 个 Connection 均关闭、4 个 Guest 撤销请求均收敛、无 pending 账号操作，审计存在真实登录、注销、签发/退役与分配变更。测试账号/Home/控制记录、临时单位/PAM 和显示比例记录已精确清理，共享显示布局、PAM 与原 Guest 摘要已恢复；新库及其卷已删除，两个临时控制面均退出，客户端退出登录并恢复原地址。VM 160 正常关机任务 `OK`、自身节点确认 stopped/4096 MiB；业务 VM 158 的 XFCE PID 52975/启动时间不变，Windows 9112/9113 保持停止。没有升级原开发库、修改宿主驱动或触碰业务会话。
  - REVIEW-02/12 的 Linux 默认客户端切片推进，不关闭总目标。旧账号升级、完整故障矩阵、Windows 默认执行端、MCP 观察/接管、模板实建及全部其余 P0–P2 条件仍按上表继续；本轮不计 OIDC、剪贴板全矩阵、Kubernetes、GPU 编码或生产发布验收。

- 2026-09-07（强制回收后的 Xorg 节点清理与连续服务回归）：

  - Linux 撤权在账号/birth 门内给予精确 root 创建者合计最多两秒正常退出机会，随后仍无条件复核并排空原 cgroup；不会把父进程退出当作孤儿回收完成。固定 UID、登录域及用户管理器任务全部关闭后，才处理该 UID 的规范 X11 锁/socket。持 root/sticky 目录句柄、限制扫描与数量，拒绝混合归属、链接、异常类型及活 PID 锁；未知节点保留并阻止后续签发。账号 worker 使用共享 `/tmp`，readiness 仍保留 `PrivateTmp=true`，没有增加 SYS_ADMIN/SYS_PTRACE 权限。
  - `make computer-session-check` 全套容器回归通过，新增正式 release CLI 的真实 Unix 节点矩阵，覆盖正常清理、其他 UID/非规范文件保留、混合归属/活 PID/软硬链接/不安全目录拒绝与 socket-only 中断恢复。初轮 GUI 夹具让普通用户创建 X11 公共目录，被新检查正确拒绝；已按完整 Debian 的 root-owned 1777 布局修正夹具，没有放松产品校验。最终测试镜像 `sha256:1905beca3caef458caa486a495cb8cb49f537cfdf39f58b7e65e233e2a639e5b`；Linux/macOS Rust tests/Clippy、Go 全量 test/vet、Computer/HTTP race 两轮及 Python/diff 检查通过。未配置新数据库实例，不把跳过的集成测试计为真库复验。
  - 隔离 Debian 13 VM 160 的 Native 真实 RDP/PAM 回归通过（106.03 秒）：同 Xorg/XFCE 退役重连、正常撤权及原始 50 秒期限回收；第三次登录冻结真实 Xorg，使正式 worker 必须处理无法正常退出的桌面。两种撤销均在夹具删除前断言原 socket/锁文件消失。早期“杀父进程后仍保持冻结”的夹具断言失败（56.08 秒），未计通过；冻结桌面与父进程死亡分开验证，后者由真实 PAM root 孤儿矩阵覆盖。不是 macOS `.app` 或默认 HTTP Handler 验收。
  - 同版 Guest 的完整服务故障矩阵先通过 302.25 秒；追加连续测试揭示已关闭 UID 的 systemd failed 元数据会污染下一轮预检（8.87 秒失败）。现仅在精确账号撤销、UID 无进程且相应单位无 Job 后清除其失败标记，不执行全局 reset，也不降低预检标准。修正后无人工整理地顺序通过服务矩阵 305.00 秒与丢失登录回执 54.04 秒：readiness SIGKILL、只读 `/etc` 失败/正式 timer 恢复、认证前/后及父进程退出的 root 孤儿、新租约复用 UID，以及真实用户管理器 Job 未结束时拒绝确认关闭均通过。
  - 实际 Guest SHA-256 `6023d4d996afa29bcd78b3dd69f02a34a6aecb2e02a86810de83c4fb44fe2fd4`；两个 PAM 模块未改协议。夹具的精确账号/Home 删除成功；独立检查控制记录、Xorg 节点、RDP 容器、服务/恢复目录和 PAM 备份全部清理或恢复，五个测试 UID 的用户管理器均 inactive、无 Job。原 Guest 已按哈希恢复为 `10ed602f6098f78b4323db002ab857dd34e0072d4e15cd3a1624c9181473d2b9`，仅本批新增的 Native PAM 模块移除，产物保留在本地缓存。VM 160 正常关机任务 `OK`，自身节点状态确认停止、仍为原 4096 MiB，未改内存或宿主驱动。业务 VM 158 的 XFCE PID 52975/启动时刻不变，Windows 9112/9113 停止；原开发库仍为 migration 013，开发服务未重启。
  - 产品 Xorg 残留切片已通过，REVIEW-12 和全部 P0–P2 总目标仍进行中。下一步继续默认 HTTP→macOS 的保留应用、撤权与恢复实测，再推进旧账号升级和 Windows 执行端；本批不计 Windows、正式模板、GPU 性能或客户端 UI 验收。

- 2026-09-07（Native 真实 RDP/PAM、保留桌面重连与运行中到期）：

  - 新增 `make pve-live-native-desktop-check`，只允许显式指定隔离 Debian 13 VM 160。通过真实 TCP RDP、QGA 核验的证书指纹及 stdin 凭据登录；每次连接使用独立日志游标，要求新 RDP 进程实际接入该固定 UID 的存活 Xorg，再验证 XFCE PID/启动标识。普通凭据退役与新连接保留同一 Xorg/XFCE，精确撤权清空 UID 进程和 root 登录写入者；重新登录建立新桌面，正式 timer 在原始 50 秒期限后回收仍连接中的会话，未修改时钟/账本或手动 reconcile。
  - 新 Guest amd64 产物 `45988a887097ec49601fed7b0fb12a72d4c1c799366a54c20c980ebf2729f741` 与 Native PAM `e5572ed7ab3f8e731783f97e74da0873caeee91b0c7dd95ac2cf4e890088074b` 已在隔离机实际运行；Agent PAM 摘要未变。使用的是临时 FreeRDP/Xvfb 客户端，不是 macOS `.app`，也未经过默认 HTTP Handler，不能据此关闭端到端身份与客户端 TODO。
  - 加强新 RDP/Xorg 连接观察后的独立 Native 两轮通过（106.85 / 104.48 秒）。最终共用服务联跑通过：真实 root 用户管理器 Job/丢失回执恢复 54.36 秒，Native 保留桌面/到期 104.74 秒，整轮 159.38 秒；后者包含相同 Xorg 和 XFCE 身份断言，以及先排空周期写入者、后删除账号的清理。Go 全量 test/vet、HTTP 相关 race、Python 语法和 diff 检查通过。
  - 首轮清理发现受管 Helper 的 `runtime` 文件未被夹具纳入清单，另一次严格预检发现 xrdp 日志实际属于服务用户而非 root；失败轮不计通过，恢复精确身份后再重跑。夹具现保存 root 私有归属记录，仅清理确认关闭的该 UID/Home、已校验的 Helper 节点和本轮 Xorg inode，且先停止周期写入者再删除账号。共用服务联跑还暴露上轮 UID 的 systemd failed 元数据影响下一轮预检，现仅在无进程/无 Job 后清理该精确 UID 的失败标记，不执行全局 reset。
  - 最终独立检查账号/Home/控制记录、临时 RDP 容器、恢复目录、PAM 备份和临时服务均已清理或还原；UID 无进程/Job，用户管理器为 inactive。Agent 回归遗留的 X10 节点经测试时间、PID、UID 和 inode 核验后单独移除，未按前缀清理未知资源。原 Guest SHA-256 恢复为 `10ed602f6098f78b4323db002ab857dd34e0072d4e15cd3a1624c9181473d2b9`，临时 Native PAM 模块移除，构建产物保留在本地缓存；PAM 摘要恢复原值。VM 160 正常关机任务 `OK`，再次确认停止、内存仍为原 4096 MiB（本批未改内存）。VM 158 的 XFCE PID 52975/启动时刻不变，Windows 9112/9113 停止；原开发库仍为 migration 013，无新库迁移或开发服务重启。
  - 实测确认强制关闭登录域会留下 Xorg socket/锁文件；夹具能按本轮已记录 inode 清理，不等于产品已自动解决。该项继续归属 REVIEW-12；剩余工作还包括默认 HTTP→macOS 保留应用的完整链路、旧账号归属/升级迁移、其他登录入口、服务故障矩阵和 Windows 执行端。全部 P0–P2 总目标保持进行中。

- 2026-09-07（默认 Linux Native 建联、原子提交与双副本恢复）：

  - 默认 Linux HTTP/QGA 已接入固定 UID 与凭据 revision：能力检查先于退役旧连接；签发预留先于一次性 stdin 写入；精确回执、独立 Guest 观察和当前授权均满足后，在同一事务确认账号版本、创建 Connection Session 并保存期限。旧用户名式 Linux 写入不再作为新连接的兜底。普通断开退役但保留桌面；注销/撤分配、过期和后台恢复走精确版本撤销，丢失签发回执不会重发密码。
  - 真 PostgreSQL + 完整 HTTP Handler 覆盖两套独立 Store/Server 的同 VM 并发、断开重连、旧 DELETE、注销中的在途签发、签发回执丢失、退役回执丢失和恢复。仅观察失败时保留 pending，不推断可杀死桌面；确认 Guest 已退役后才能无写入确认。数据库注入 Connection INSERT 失败验证整个签发提交回滚，旧 API 不能绕过已绑定账号。Native Store/HTTP 专项 race 两轮通过（18.059 / 8.357 秒）。Guest 使用模型，该结果不是实机登录证明。
  - 最终全量 Go 真库 race 通过（Store 30.378 秒、HTTP 16.502 秒、Computer 21.510 秒），Go vet 与 diff 检查通过。独立 PostgreSQL 17.11 的测试 schema 数为 0，测试容器和专用匿名卷已删除，不保留数据；原开发库只读确认仍为 migration 013，未升级或重启原开发服务。
  - Linux 无网络容器通过真实 shadow、进程保留/撤销、崩溃恢复及 Native PAM 安装/命名空间门禁。新账号补回仅针对实际渲染节点的本地 `render` 组权限，不添加 `video`、证书私钥或管理员权限；容器设备替身不是 GPU 加速验收。Rust workspace/Clippy、两个 PAM 硬化构建通过。
  - 新 Debian 13 配方在离线阶段显式安装并检查两个 PAM 入口；Builder 要求 Native 模块产物，CI 产物契约同步。`make images-check` 和 Builder 测试通过，但未执行 Web/Packer 实建或远端 CI。旧 Guest/PAM、未知归属的旧 `vcw` 必须先显式升级/迁移，否则新代码拒绝连接；Windows 未绑定路径暂不切换，已绑定 Native 账号不允许回退。
  - 本批尚无新的 PVE、Windows 或 macOS 图形验收，未修改 VM 158 或宿主驱动。下一步是隔离 Debian 的真实 Native sesexec/logind、保留应用的断开重连、撤权与离线期限，然后接默认客户端验收及旧账号迁移。REVIEW-12 和全部 P0–P2 总目标继续进行，不以接线完成关闭整体任务。

- 2026-09-07（Native Linux 凭据执行端与独立 PAM 前置保护）：

  - 新增独立 Rust Native 协议与 Linux `native-account-provision/inspect/credential` 命令，绑定固定 `vcw…` UID、连接 ID 和单调 revision；新账本不复用 Agent Lease/generation。先持久保存 pending，再执行真实 shadow 写入；密码仅走 stdin，不进入参数、日志或回执。退役及未到期重连保留用户进程，撤权、原期限到期和中断恢复才禁用账号、排空进程域。
  - 修复跨层边界：Guest 允许更高版本撤销覆盖未送达的新连接签发，相同版本不能改变连接或重放密码；Store 与 SQL 拒绝直接重开已到期的保留桌面，必须先确认撤销。一次性 PostgreSQL 17.11 的 Native 专项 race 两轮通过（13.709 秒）；全 Store/HTTP 真库 race 通过（21.703 / 8.159 秒）。
  - 独立 `pam_vcworkspace_native.so` 只处理 Native 账号，现有 Agent 模块仍只处理 Agent。Native 签发/退役要求已安装当前 Native PAM 保护，缺失时拒绝写入，登录写入者是否为空返回未知。新增显式离线安装选项 `--native`：要求先有当前 Agent 保护、会话创建进程已停止、模块权限安全，保留精确 root-private 备份；无参数安装不改变 Native 登录，正式模板暂不自动启用。
  - 无网络一次性 Linux 容器通过真实 shadow 密码轮换、重复退役不重写、保留 UID 进程的重连、完整撤权、四个实际进程退出故障点、`chpasswd` 在父进程被 SIGKILL 后继续持有账号锁、离线回收、精确到期、旧请求和损坏账本拒绝。另一个容器通过安装前后正式 CLI 的拒绝/允许、真实 libpam 命名空间隔离和错误服务拒绝；故障注入仅在受保护的测试二进制中，正式 CLI 不提供绕过。既有 Agent shadow/孤儿写入回归及双 UID Xorg/AT-SPI/输入隔离门禁通过。
  - Linux/macOS Rust workspace tests、Linux/macOS/Windows-target Clippy、Linux 两个 PAM 模块硬化编译、Go 全量 test/vet 通过。测试 schema 最终为 0，一次性数据库容器和专用卷已删除；原开发库仍为 migration 013。本批未执行 PVE 写入、Windows SAM 或 macOS 图形操作，没有把容器内 PID 保留当成真实桌面重连验收。
  - REVIEW-12 和全部 P0–P2 总目标继续进行。下一步仍须真实 Native sesexec/logind 登录域与断线重连验收、旧 `vcw` 归属迁移/排空、签发与 Connection Session 原子提交、Go 默认 HTTP/QGA 和撤权队列接线，以及 Windows 对等执行端；其他登录入口和服务异常矩阵也未关闭。当前默认 Native 路径没有被本批静默切换。

- 2026-09-07（Native 账号身份与凭据版本持久层）：

  - 新增 028 迁移与 `NativeGuestAccount` Store API：独立绑定 `vcw…` 的 UID/完整 SID；签发、凭据退役、账号撤销分别提交单调 revision，先保存 pending 意图，再按精确身份/连接/版本确认。迟到的旧签发不能确认较新的撤销，旧退役不能改变新凭据；历史连接 ID 保留，不能在丢失回执后重用于另一轮登录。普通退役保留原 OS 会话期限，不采用 Agent 登录前清空桌面的语义。
  - 签发预留/完成重新验证 Native Token、当前授权、平台和 Profile；Native 用户之间、Native 与 Agent 之间的同 VM UID/SID 不能别名绑定。未完成 Native 预留阻止新 Agent Lease，同 VM 只能预留一个 Native 控制者；Agent 清理尚未完成时拒绝 Native 新签发。SQL 约束保护身份、顺序、初始插入和原意图，恢复查询保留未完成操作与到期的已退役桌面。
  - 独立 PostgreSQL 17.9 完成 10 组测试，包括 Linux/Windows 身份模型、并发首次绑定/签发、跨主体身份冲突、令牌注销/到期、授权移除、用户停用、Profile/平台变化、旧回执、原始期限和 027→028 重复迁移。最终 Native 专项 race 两轮通过（17.206 秒），全 Store/HTTP 真库回归及 race 通过（Store 27.144 秒、HTTP 11.340 秒）；Go 全量 test/vet 与 diff 检查通过。
  - 首次全量回归使 192 MiB 临时内存盘的 WAL 写满，数据库日志确认因空间不足退出；失败轮不计通过。确认进程终止后改用有足够空间的一次性 Docker 卷重跑，未修改原库配置。正式原开发库仍为 migration 013。原生客户端用户名算法改为复用同一 Store 派生函数，输出保持不变。
  - 最终独立检查测试 schema 数量为 0，一次性容器和专用匿名卷均已删除，不保留测试数据。本批未执行 PVE 写入或 Guest/SAM/macOS 图形测试；只读复核 VM 158 的原 XFCE PID 52975/启动时刻不变、VM 160 停止且为 4096 MiB、Windows 9112/9113 停止，没有修改宿主驱动。
  - 028 不回填未知旧 UID/SID、不改变历史 Native 连接，也不把数据库迁移当作 Guest 排空。默认 HTTP/QGA 路径尚未调用新接口：下一步必须完成两端 Guest 的凭据版本门、Native 登录域和离线期限、签发与连接 Session 的原子提交、撤权队列接线及真实 macOS 断开/重连验收。不能用这批数据库测试宣称 Native 实际写入已经受保护，REVIEW-12 和全部 P0–P2 总目标保持进行中。

- 2026-09-07（正常关机期间到期与开机自动回收）：

  - 新增 opt-in `make pve-live-guest-cold-boot-check`。隔离 Debian 13 VM 160 创建真实 XFCE/Helper，正常关机并等待原始 90 秒租约到期，再启动虚拟机。确认新 kernel boot ID 后，正式开机 timer 自动将原精确版本收敛到 revoked、禁用账号、UID 进程和登录写入者均为空；未手动启动服务、执行 reconcile、修改时钟或账本。随后新租约建立新 Helper，原 UID 和 Home 内的标记保留。两轮完整实测通过（152.47 / 142.89 秒）。
  - 首轮探索暴露 PVE 集群清单缓存滞后：关机任务已 `OK`、所属节点已停止，集群清单仍短暂显示 running。该失败轮不计通过；保留持久记录后独立确认任务和电源，等待原租约到期，再单次恢复开机并清理。新增只读 `VMPowerState` 直接读取所属节点，拒绝缺失、未知状态和错配 VMID；验收只追踪原 UPID，读取失败不重发启停。
  - 冷启动夹具仅暂时启用两个正式开机链接，使用 `/var/lib/vc-workspace/acceptance-svc<标记>` 保存 root 私有恢复记录；原登录及开机后的新登录均先持久保存精确版本，再派发。结束后独立确认本轮账号/Home/控制记录、恢复目录、开机链接、PAM 备份和三个临时单元全部清理/恢复，未留下登录会话或用户管理器 Job。清理不确定时保留现场，不按前缀删除未知资源。
  - 共用运行时夹具的真实回执超时回归通过（54.60 秒）；Go 全量 test/vet、PVE race、Python 语法及 diff 检查通过。Guest 与 PAM 产物未改变，分别保持 `10ed602f6098f78b4323db002ab857dd34e0072d4e15cd3a1624c9181473d2b9` 和 `4b2793f3e289d1f4ced16478a29aa84291ece21ca6399c3c5f68b890700d3b4f`；授权历史仍为 epoch 45、revoked、target=null。本批没有新数据库、SDK MCP、Windows 或 Mac UI 验收。
  - VM 160 验收后正常关机任务 `OK`，以最新 digest 恢复原 4096 MiB，独立读取确认停止。业务 VM 158 的 XFCE PID 52975/启动时刻未变，Windows 9112/9113 保持停止，原开发库仍为 migration 013；未修改宿主驱动。测试业务数据已删除，不保留。
  - REVIEW-12 与全部 P0–P2 总目标继续进行：本批不等于断电恢复、未到期租约恢复、旧进程排空/升级或正式模板实建。下一批仍需在途 logind 重启、PID 1/systemd 异常、其他登录入口、Native `vcw` 与默认 Windows Broker，不能以这个电源周期关闭整体账号生命周期缺口。

- 2026-09-07（真实 D-Bus 排队请求与 logind 崩溃恢复）：

  - 新增 opt-in `make pve-live-login-queue-check`。隔离 Debian 13 VM 160 暂停精确绑定的 logind，私有监视器确认原生 PAM 的创建请求已经通过系统总线发送，UID/发送者 PID 与账本创建者相符。客户端超时并退出后，Guest 拒绝将登录服务超时解释为关闭；恢复原 daemon 后，捕获到对应消息序号的真实 `System.Error.ENXIO` 回复。未伪造回执、未发送测试替代创建请求，也不只凭瞬时 UID 无进程推断队列已失效。
  - 原租约收敛后以同一 UID 创建真实 XFCE/Helper，再对原 logind 发送 pidfd SIGKILL；系统自动恢复为新的 daemon/InvocationID，原 UID/Session/Helper 及 sealed 登录版本保持绑定。首两轮通过（42.60/42.70 秒），最后按精确版本回收桌面；正式 Guest/PAM 产物和控制面逻辑未修改。这不是 PID 1 重启、请求执行到一半时崩溃、冷启动升级或 Mac 可视输入验收。
  - 测试使用独立恢复 guardian，信号只指向原 pidfd；恢复记录先于暂停。早期监视器在 GDBus I/O 回调内同步读取发送者导致超时，已移至主上下文；失败轮恢复 daemon 后可能释放在途登录，清理现先观察原派发结果，再执行精确版本 stop。首轮残留会话经独立 sealed 观察后撤销并清理，没有直接删除活动账号。联跑另发现本轮 user@ 的停止失败标记影响下轮保守预检，已限定在本轮 UID/无 Job/确认关闭之后清理。失败轮不计通过。
  - 最终连续实机联跑通过：原用户管理器 Job 用例 54.29 秒，排队/崩溃恢复用例 42.59 秒，整轮 97.16 秒。旧请求的错误回复再次捕获，新登录和最终精确回收均通过；测试账号、Home、控制记录、监视器/guardian、恢复目录、PAM 和临时单元经独立检查已清理/恢复，`user@1003.service` 为 inactive 且无 Job。Go 全量 test/vet、相关 race、Python 编译与 diff 检查通过；本轮未运行新数据库、Windows 或 Mac UI 验收，不借用旧证据扩大范围。
  - Guest 产物仍为 `10ed602f6098f78b4323db002ab857dd34e0072d4e15cd3a1624c9181473d2b9`，新授权终态 epoch 45/target=null 保持。没有修改宿主驱动、原开发库或运行中的 VM 158；独立确认其 XFCE PID 52975/启动时刻不变、原库 migration 013、Windows 9112/9113 停止。VM 160 仅临时使用 2048 MiB，正常关机任务 `OK` 后以 digest 校验恢复原 4096 MiB，再次读取确认停止。临时测试数据已删除，不保留；没有把运行时验收当作正式模板升级。
  - REVIEW-12 和全部 P0–P2 总目标保持进行中：在途 logind 重启、PID 1/systemd 异常、冷启动/旧进程升级、其他登录入口、Native `vcw` 和默认 Windows Broker 尚未闭环。

- 2026-09-07（丢失 logind 回执与用户管理器任务隔离）：

  - 在隔离 Debian 13 VM 160 复现旧版错误：实际 `user@UID.service` 的 root 启动前任务暂停，PAM `CreateSessionWithPIDFD` 超时后父创建者退出，但用户管理器 Job 仍在运行；旧版仍报告关闭。基线故障验收按预期失败（22.31 秒），没有伪造回执，也不声称已经发生迟到降权。
  - 新版 `pam_logind_jobs_v3` 在 pidfd/scope/UID 进程检查之外，要求 logind 用户消失、用户 slice/管理器/runtime-dir 单元无待执行 Job 且已停止；只接受确切的不存在错误，其他错误拒绝确认。撤权有界等待 15 秒，未收敛保留终态意图并由 timer 重试，不派发可能迟到的命名单元停止任务。原生 v2 PAM ABI、策略和 schema 2 记录不变，控制面拒绝旧 v2 能力且不会派发密码。
  - 新增 `make pve-live-login-receipt-check`，修复版实际通过（55.88 秒）：超时/仍有 root Job 时拒绝确认关闭，恢复该启动任务后由正式 timer 收敛，新租约以原 UID 建立真实 XFCE/Helper。故障只针对夹具新建 UID 的独占 drop-in，未修改全局 user@ 模板；账号、进程、drop-in、恢复目录与 PAM 栈独立复查已清理/恢复。早期夹具的空 Job 字段解析和缺少 schema_version 问题已修正，失败轮精确清理后才重试。
  - 新 Guest SHA-256 `10ed602f6098f78b4323db002ab857dd34e0072d4e15cd3a1624c9181473d2b9` 已在隔离机核验；PAM 模块仍为 `4b2793f3e289d1f4ced16478a29aa84291ece21ca6399c3c5f68b890700d3b4f`。ARM64 容器真实账号/shadow/写入中断与双 UID GUI/Input 回归、amd64 构建、Go 全量 test/vet、Computer race、Mac Rust workspace/Clippy 和一次性 PostgreSQL Store/HTTPAPI race 通过；这不是本轮 Mac UI 或 Windows 实机验收。
  - 同一 Guest 的正式服务/PAM 整轮通过（299.90 秒）：正常桌面、readiness SIGKILL 恢复、原始期限回收、只读 `/etc` 失败后 timer 重试，以及认证前/密码验证后/root 孤儿三个派生中断场景均通过，新租约保持原 UID。
  - 完整 MCP 首轮暴露了控制面重连回归（53.40 秒失败）：UID 进程刚退出时，正常 logind 停止延迟被立即当作不可恢复错误。现仅在原 sealed 版本且 UID 已空时有界只读等待；身份、版本、权限、期限或进程状态变化即拒绝，全部关闭后才 CAS 预留下一登录，不能重发旧密码。新增 14 类确定性时间/错误/身份回归，以及真库的延迟收敛、取消后保留精确清理版本测试；最终 Store/HTTPAPI race 通过（8.53/5.77 秒）。
  - 修复后的 SDK HTTP MCP→控制面→PVE→Guest 完整实机通过（203.13 秒）：无人值守桌面与图像、AT-SPI/坐标/鼠标/键盘输入、同租约重连、旧 generation 登录/撤销拒绝、MCP 重建、凭证轮换、释放重领、后台停用、双用户 Home 隔离与撤分配；记录 12 次成功/3 次预期拒绝，审计未含输入或凭据内容。重连保持原 UID/Home/Lease/epoch/期限，只递增 login generation 并更新 Helper 实例。
  - 所有失败/通过轮的临时账号、root/用户进程、drop-in、PAM/服务与恢复目录已独立确认清理/恢复；一次性数据库随机 schema 数归零后删除容器/tmpfs，不保留测试数据。只保留隔离 Guest 产物及精确摘要备份，PAM 恢复后能力为 unavailable，不冒充正式模板升级。新授权栅栏保留 revoked epoch 45、target=null，旧 epoch 21 未清空；VM 160 正常关机任务 `OK`，已重新读取确认停止并恢复原 4096 MiB。业务 VM 158 的 XFCE PID 52975/启动时间不变，Windows 9112/9113 仍停止，原开发库仍为 migration 013。
  - REVIEW-12 尚未整体闭环：logind 未处理的排队请求、daemon 异常重启、冷启动/旧进程升级、其他登录入口、Native `vcw`、Windows Broker 与剩余 P0–P2 保持未完成。总目标不缩减、不关闭。

- 2026-09-07（真实 PAM 登录域接线与 root 孤儿回收）：

  - 新增小型原生 `pam_vcworkspace.so`，真实 PAM 父进程保留 logind FIFO 与 XDG 环境，原 `pam_systemd`/认证栈保留；Rust 经 root SOCK_SEQPACKET、父进程 pidfd、固定 UID/Lease/epoch/generation 和实际 systemd/cgroup 交叉验证，先持久提交 scope 再允许认证继续。能力升级为 `pam_logind_pidfd_v2`，旧 PAM 命令/schema 1 记录拒绝静默升级。C 与 Rust 均不接收密码或任意命令，子 helper 受父进程死亡信号约束。
  - 回收对 root 私有记录中的固定 scope 逐级 NOFOLLOW 打开，核对 boot、设备/inode，再经目录 FD 写 `cgroup.kill` 并等待整个域为空；父进程已经退出也不能跳过非空 scope。服务仅新增 `/sys/fs/cgroup/user.slice` 嵌套写例外，没有增加 SYS_ADMIN/SYS_PTRACE。Packer、Builder 配置和 Linux 构建产物已同时包含模块；新模板实建仍单独跟踪。
  - 隔离 Debian 13 VM 160 的正式单元/PAM 整轮通过（214.86 秒）：正常 XFCE/Helper、readiness SIGKILL 自动恢复、原始期限回收、只读 `/etc` 失败后 timer 自行恢复；认证前、真实密码验证后、sesexec 已死三个故障场景均真实派生 root 子进程并停在 setuid 前。最后一个场景确认父进程已死但孤儿仍活，仍报告登录域未关闭；timer 清除了暂停 helper/孤儿，不能迟到 setuid，每次新租约都能以原 UID 恢复。故障脚本不进入发布二进制，不是修改 xrdp 的 fork 指令。测试账号、进程、PAM/单元和恢复目录独立核对已清理/恢复。
  - Linux ARM64 容器普通 Rust 测试、Clippy、真实 pidfd/父进程死亡、shadow/账号写入中断和双 UID GUI/Input 回归通过，C 桥接以 `-Wall -Wextra -Werror` 编译通过；amd64 交叉构建/上传摘要核验通过，Go 全量 test/vet 与 Python 语法检查通过。当前隔离产物 Guest SHA-256 `2cc11f76b2639e037f536e4125e14dd26e805a7c0357609d8795e948d0034866`，PAM 模块 `4b2793f3e289d1f4ced16478a29aa84291ece21ca6399c3c5f68b890700d3b4f`；原二进制有精确摘要备份，旧 heartbeat 产物未替换。
  - 同一新版产物通过完整 SDK HTTP MCP→控制面→PVE→真实无人值守桌面（193.35 秒）：图片/语义/坐标/输入、同租约重连保留 UID/Home/原期限、旧 generation 登录/撤销拒绝、MCP 重建、凭证轮换、释放重领、后台停用、双用户隔离、撤分配和脱敏审计，共 12 次成功与 3 次预期拒绝。一次性 PostgreSQL 的 Store/HTTPAPI 全量 `-race` 回归通过；全部随机 schema 已独立确认为 0，临时容器/数据库已删除，不保留测试业务数据。最初误选的 amd64 PostgreSQL 镜像在 ARM64 主机无法执行，未运行数据库；清理该空实例后改用本机 ARM64 镜像，未调整宿主 binfmt。
  - 两轮临时账号、root/用户进程、PAM 栈、三个服务单元和恢复目录均独立核对已清理/恢复；保留的新 Guest 和 PAM 模块仅用于隔离复验，PAM 恢复后能力为 unavailable，不表示正式模板已升级。Guest 新撤销栅栏保留 epoch 35、target=null，不清空历史；旧栅栏 epoch 21 保持。VM 158 原 XFCE PID 52975/启动时刻、停止中的 Windows 9112/9113 和原开发库 migration 013 未变。VM 160 根据宿主余量临时用 2048 MiB 完成验收，已正常关机（任务 `OK`），重新读取确认停止、内存恢复原 4096 MiB。REVIEW-12 的丢失回执、用户管理器任务、异常重启/升级和全部其余 P0–P2 继续推进，不缩减或关闭总目标。

- 2026-09-07（登录派生进程的内核反例与 scope 机制验收）：

  - 新增显式 opt-in `make pve-live-login-tree-check`，在隔离 Debian 13 VM 160 连续通过（4.35/4.23 秒）。真实 root 子进程在父 pidfd 退出、目标 UID 暂时无进程后仍能切换进目标 UID，确认不能把现有父进程/UID 观察当作整个派生树的关闭证明；夹具不是在真实 xrdp 的 fork 指令处注入故障。
  - 同一夹具通过 systemd 257 的 `PIDFDs` 而非数字 PID 绑定独立 scope，在实际 cgroup 归属确认后才允许 fork；父进程死亡后的孤儿仍使 scope 报告 populated。内核 subtree kill 清除该孤儿，systemd 日志确认调用 `cgroup.kill`，另一账号与同 UID 新 scope 均保留，旧 scope 操作不能指向新 scope。PAM/xrdp 配置摘要保持不变，未替换正式 Guest 二进制，未增加发布 CLI 测试入口或把实验机制宣布为默认 MCP 修复。
  - 已进一步确认 PAM 接线约束：logind 会迁移会话归属，预注册后正常 `pam_systemd` 的 SessionBusy 路径不会重新设置环境，且临时 helper 退出后关闭最后一个 FIFO 引用会启动会话结束。因此下一步需把引用和环境交给真实 PAM 调用进程，并持久绑定 scope/账号版本，覆盖丢失回执、异常重启和完整 xrdp 桌面；不通过禁用正常 PAM 桌面服务来缩小目标。REVIEW-12 和全部 P0–P2 总目标保持进行中，细节见 [ADR 0003](../decisions/0003-computer-use-data-plane.md#版本化账号登录)。
  - 相关 Go test/vet、Python 语法和差异检查通过；本轮未启动数据库测试服务，未将跳过的数据库集成计为新证据。宿主启动前可用内存约 5784 MiB，隔离 VM 160 临时使用 2048 MiB 完成测试，保留不少于总内存 10% 的余量；结束正常关机任务 `OK`，重新读取确认停止且内存恢复原 4096 MiB。两轮账号、root/用户进程、scope/slice、脚本及恢复目录独立核对无残留，一次性测试记录已删除、不保留；正式 Guest 摘要保持 `6c2e32f2…`。未调整宿主驱动、VM 158、Windows 测试 VM 或原开发库。

- 2026-09-07（Linux PAM 登录创建者登记与真实认证中断）：

  - Linux 增加固定 PAM 前置登记、root 私有创建者账本、boot/PID/启动时刻核验与 pidfd 回收；固定 `xrdp-sesrun` exec 包装使用内核父进程死亡信号，避免 Guest 崩溃后登录客户端无限持锁。控制面新增 `login_writers_absent` 独立观察，缺失字段、未安装 `pam_pidfd_v1` 或仍有创建者时拒绝确认关闭/预留新登录。Packer 和维护入口使用同一 PAM 安装器，保留原栈；二进制替换不隐式改已有 Guest 的 PAM 或服务。
  - 首次真实登录成功，但能力受限的回收服务读取其他 root 进程 `/proc/PID/exe` 被拒绝，整轮失败（104.34 秒）；独立同权限只读探针确认 `EACCES`。修复为登记时认证可执行文件、回收时核对私有进程实例与 pidfd，没有增加 `CAP_SYS_PTRACE`。最终 amd64 Guest SHA-256 `6c2e32f289ba97a89859dadfbddb9eecf6593229e0f6706354bafb6f44c64305`，隔离安装摘要校验通过，旧二进制保留。
  - 隔离 Debian 13 VM 160 的源码服务/PAM 验收通过（152.17 秒），包含真实 root 创建者的自动回收和只读故障恢复。首次认证暂停扩展通过（173.61 秒）；最终四账号整轮通过（192.48 秒），在认证前登记后及 `common-auth` 密码校验后分别暂停 PAM，只杀发起登录的 Guest 进程，独立确认 SCP 客户端退出、root 创建者仍活着、UID 已无进程。旧登录重放被拒绝，正式 timer 自行回收，迟到 PAM 返回不复活桌面，新租约复用相同 UID 并拒绝更旧请求。故障脚本不进入发布二进制或模板；其他更晚阶段不由此推定通过。
  - Linux 容器完整账号/图形门禁通过，镜像 `3d317a536591817973e8270c57174105db6b320767e10db7aceb55f803470028`；29 项常规 Rust 测试、6 项共享状态机及显式内核 pidfd/父死亡和账号崩溃测试通过。真实一次性 PostgreSQL 下 Go 全量 test/vet、HTTP/Computer race 两轮通过（最终 8.452/37.888 秒），包含 UID 已无进程但 root 创建者仍在时不启动新登录。Mac workspace/Clippy、Windows 交叉 Clippy、Packer/Shell/Python 语法与格式检查通过。本轮未重跑完整 SDK MCP、Windows SAM/RDP 或 Mac UI，不以组件结果替代这些证据。
  - 每轮独立检查均确认测试账号/Home/控制记录、Xorg/sesexec、暂停 PAM helper、临时单元/drop-in/恢复目录无残留；原 PAM/readiness 和 xrdp 配置摘要恢复。原新/旧授权仍为 epoch 28/21 revoked，legacy heartbeat PID 792 和二进制不变。一次性 tmpfs PostgreSQL 的临时 schema 数为 0，随后删除该测试容器及不可恢复的测试数据；未触碰原开发库，migration 仍为 013。VM 160 正常关机任务 `OK`、恢复停止，Windows 9112/9113 仍停止；业务 VM 158 的 XFCE PID `52975` 和 `Sep 4 09:41:39 2026` 启动时间保持不变。仅保留隔离 VM 中的新测试二进制及可恢复旧版本备份。
  - 复核上游 xrdp 源码确认会话子进程先 fork、随后设置用户环境；据此识别尚未实机复现的 root 子进程降权窗口，仅登记 sesexec 本体不足以证明任意派生进程均结束。该边界优先留在 REVIEW-12，需 OS 进程域约束与对应故障验收；旧进程排空、Native `vcw`、Windows Broker、新模板和全部其余 P0–P2 继续推进，总目标未缩减或关闭。

- 2026-09-07（Linux 独立账号回收服务与故障恢复）：

  - 修复正式模板只读系统目录与 shadow 写入冲突：三个 systemd 单元集中维护于 `deploy/guest/linux`，Packer 上传同一份文件。readiness 仅写状态目录；独立固定命令的 root oneshot 获得账号工具所需 `/etc` 写入权限，系统/SSH/PAM/sudo 配置仍只读，并限制能力、地址族和执行时间。timer 不依赖控制面网络，失败后继续调度；没有在 readiness 中放宽整个系统的写权限。
  - 新增显式 opt-in `pve-live-guest-service-check`。隔离 Debian 13 VM 160 / systemd 257.13 连续两轮通过（152.06 / 151.90 秒）：分别创建两个真实 xrdp/Helper 用户；SIGKILL readiness 后自动重启且仍保留未到期会话，原 55 秒期限后 timer 自动锁定 shadow 并清除 UID 进程。另一会话通过仅收紧权限的 drop-in 复现 `/etc` 只读导致已 revoked 但登录/进程尚未关闭；恢复源码权限后 timer 自行完成回收，没有缩短期限或手动 reconcile 代替断言。服务回收不是冷启动/升级排空或默认 SDK MCP 证据。
  - amd64 Guest 安装校验通过，SHA-256 `a2d5ddb39253b27f222649bd6aeedf343da6d818f5b93f3d9b5e6e9f168198f5`，上一产物保留为 `.previous-e7c3370b6192`。两轮夹具清理及独立检查确认测试账号/Home/控制记录、临时单元/drop-in/恢复目录和 Xorg 均无残留，原 readiness 状态与 xrdp 配置已恢复；新/旧授权终态仍为 epoch 28/21。未替换 legacy heartbeat、重启 xrdp 或修改宿主驱动。
  - Linux 容器镜像 `343d5d962e74228ca2e743fce768201ff35d113f4c857f1b6acd25dc0a01e46a` 的账号故障/到期与双用户图形门禁通过；Linux Rust 28 项常规及独立崩溃检查、6 项共享状态机通过。Mac workspace/Clippy、Windows 交叉 Clippy、Go 全量 test/vet、HTTP/单元 race 两轮与 Packer 语法/格式、Shell 语法检查通过。未配置真库的集成跳过不计新数据库证据，本轮未重跑 Windows SAM/RDP 或 Mac UI。
  - VM 160 正常关机任务 `OK`，恢复原停止状态；Windows VM 9112/9113 仍停止。业务 VM 158 的 XFCE PID `52975` 及 `Sep 4 09:41:39 2026` 启动时间不变，原开发库仍为 migration 013。本轮仅删除新建的一次性测试账号和数据（不保留），保留升级后的隔离测试产物与原二进制备份；没有升级业务 VM 或原开发库。
  - 旧进程排空、sesman 异步登录恢复、Native `vcw`、默认 Windows Broker、新模板实建及其余 P0–P2 继续推进；REVIEW-12 和总目标均未关闭。

- 2026-09-07（Linux 账号阶段与 Helper 输入授权衔接）：

  - 修复旧 Helper 只检查全局输入授权、未核对账号登录状态的缺口。新版 `stdin_epoch_account_v2` 使用独立 `authority-account-fenced.json`，首次升级核对两个历史版本并先撤销；旧路径写入不能覆盖新授权。root 发布时持账号门核验真实 shadow 与 sealed Lease，从 Guest 账本绑定登录版本，调用方不能指定版本。旧 Guest/仍在运行的旧 Helper 明确拒绝，不能只替换文件后混跑。
  - 无秘密的生命周期记录改为 root 所有、单链接 0644，Helper 直接读取同一原子 journal；账号身份、锁和 provision marker 仍为 0600。动作、分段文本、缓存重放和返回结果均检查相同身份/Lease/epoch/登录 generation/期限及 sealed 阶段，pending/revoked 即使还没清除 OS 进程也不能继续通过校验。不维护第二份可能失步的生命周期副本，不把已经注入的事件视为可撤回。
  - 两个独立 `vca` GUI 用户实测：真实 open→启动 Helper→seal→图片/键盘输入；随后测试子进程在实际写入 opening generation 2 或 revoked 后直接退出，旧应用和 Helper 仍活着、全局授权未改，缓存图片和新输入仍被拒绝；本地回收后用更高 epoch 恢复，保留 UID/Home。该套件未手改 journal 伪造阶段。原通用双 UID 图形、AT-SPI、重放、Helper 重启、旧发布者/升级、真实 shadow/遗留 OS 写入及原期限回收继续通过。
  - 容器夹具修正了旧权限断言和错误输入字段；测试入口把 UID 复用的故障账本隔离到另一个一次性容器，并启用 init 回收 GUI 孤儿进程，避免以 PID 1 不回收的僵尸进程冒充实际系统服务行为。最终镜像 `0293930d54ad607decea19fca9b0cd75a66dfb1e441d3f3c956a8e2835150e4b` 的两容器门禁通过；Linux 27 项常规测试及独立崩溃检查、6 项共享生命周期测试通过，Mac workspace/Clippy、Windows 交叉 Clippy 通过。Go 全量带一次性 PostgreSQL、Store/HTTP/Computer/MCP race、vet 和旧协议拒绝测试通过。
  - 隔离 VM 160 新 Guest SHA-256 `e7c3370b61929eb0bf44edafd362c7f3fdcae6d542cfebf49c275c2683946e72` 安装校验通过（171.64 秒），上一版以 `.previous-e4c9fa44281c` 保留。完整真实 SDK MCP→PVE→xrdp 验收通过（193.28 秒）：截图、AT-SPI、实际输入、同租约重连保留身份/原期限、旧登录/撤销拒绝、重新领取、凭证轮换、双用户隔离、后台停用/撤分配与 12 次成功 / 3 次拒绝动作审计。容器故障矩阵不是 PVE 服务恢复证据，Windows SAM/RDP 本轮未重跑。
  - 临时账号/Home/账号记录、测试 schema 和 tmpfs PostgreSQL 容器已清理。独立复查 VM 160 无 Agent 测试账号/记录或 Xorg，新终态 epoch 28、旧 epoch 21 历史保留，正常关机任务 `OK`、恢复停止；VM 9112/9113 仍停止。业务 VM 158 的 XFCE PID `52975`/启动时间及原开发库 migration 013 不变，未改宿主驱动或 legacy heartbeat。
  - 当时发现正式模板仅允许状态目录写入，与新版账号回收更新 shadow 的权限冲突；后续独立服务修复及实测见上方最新记录。旧进程排空、异步 sesman 遗留登录、Native `vcw` 和默认 Windows Broker 仍未完成，REVIEW-12 与全部 P0–P2 总目标保持进行中。

- 2026-09-07（Linux 默认账号生命周期接线与真实 MCP 重连）：

  - 027 增加持久 `login_generation`；Guest 独立观察的 UID/SID 必须在任何登录凭据派发前入库，然后通过 CAS 预留单次登录版本。Ready、失败排队和回收确认全部匹配完整版本；旧确认不能清理新登录，旧活动记录升级时关闭租约并排队，不把数据库迁移当作实际 Guest 进程排空。
  - Linux 默认控制面取消用户名式启停/单独 QGA 改密，改为一次固定 `account-login` 请求，在 Guest 账号门内执行真实 xrdp-sesrun 登录并退役临时密码，精确 `sealed` 回执后才绑定 Helper。回执丢失不重发密码；健康会话复用，进程未证实消失时不重新登录。撤销需精确终态、禁用和独立 UID 无进程观察，未知身份不能冒充清理成功，清理失败保留队列。
  - 一次性 PostgreSQL 真库通过登录前身份绑定、并发 CAS、同租约新版本、旧 ready/失败排队/回收确认、027 升级和默认 HTTP 登录丢失回执/清理失败重试回归。Go 全量 test/vet、Store/HTTP/Computer race、Windows 交叉 Clippy 和 Linux 真实账号/双 UID 图形容器门禁通过；这轮未重跑 Windows SAM/RDP，不计新的跨端实机证据。
  - 隔离 Debian 13 VM 160 安装并校验 Guest `e4c9fa44281cebe908ec457132efb0f4351e98c25a891b42baf5a54852b8b4e1`，旧 V2 以 `.previous-d69eb996c8ef` 保留，原 legacy heartbeat/宿主驱动未更换。完整 SDK HTTP MCP→控制面→PVE→Guest 连续两轮通过（193.17 / 193.35 秒）：无人值守 1600×900 桌面、640×360 图片、AT-SPI、真实鼠标/终端输入、MCP 重启、凭证轮换、释放/重新领取、停用/撤分配后台收敛、两用户 Home 隔离和审计脱敏。最终轮含 12 次成功 / 3 次拒绝动作审计；固定命令拒绝日志不含密码。
  - 第二轮额外结束夹具自己创建的 UID 会话，再通过同一 MCP Lease 自动重建：UID、Home、Lease、epoch、原期限和账号 generation 均不变，登录 generation 从 1 到 2、Helper 实例更新。直接向 Guest 派发旧 generation 登录/撤销均被拒绝，新会话仍可截图且原文件保留；每次独立检查数据库版本与 Guest `sealed/revoked` 生命周期一致。
  - 两轮随机账号/Home、私有账号记录、安装暂存和测试 schema 已清理，临时 PostgreSQL 容器及其 tmpfs 数据已删除。独立复查 VM 160 无测试 Agent 用户/记录或 Xorg，保留单调终态 epoch 21；正常关机任务 `OK`，恢复停止。VM 9112/9113 仍停止，VM 158 的 XFCE PID `52975`/启动时间不变，原开发库仍为 migration 013。
  - REVIEW-12 继续进行：本轮不等于旧 heartbeat 已升级，也未覆盖异步 sesman 遗留登录与输入授权原子衔接、正式服务恢复/旧进程排空、Native `vcw` 或默认 Windows Broker。上述边界和其余 P0–P2 保留未完成，不以本轮通过关闭总目标。

- 2026-09-06（Linux 账号写入适配与登录 generation）：

  - 共享状态机新增必需的 `login_generation`：同一仍存活 Lease 可以使用更高登录版本，但不可延长原期限，旧 generation 的 open/seal/revoke 均拒绝；过期、未提交或 revoked 不能靠递增 generation 复活。Windows Go 请求/精确回执和测试夹具同步此字段；早期缺字段的实验协议不静默兼容。
  - Linux 新增固定 UID 的账号 lease/inspect 与本地回收器：root 私有持久 intent、不可替换账号 flock、一次性密码与退役、唯一 UID 核验，禁止删除后重新接管、UID 复用及损坏账本回退。实际 useradd/usermod/chpasswd/pkill 写入子进程继承同一锁，防止调度进程死亡后失去保护；锁不传给用户程序。主 Agent heartbeat 接入断网回收，shadow 按天到期仅作补充，不冒充秒级期限。
  - 内网源构建的一次性、无网络、无宿主挂载容器通过真实 shadow 密码生效/退役、同租约新 generation、旧写入/错 UID/锁冲突、实际进程退出后的持久恢复，以及原期限后台自动回收。另实际阻塞并保留 usermod、chpasswd，SIGKILL 调度进程后验证 OS 写入仍持锁、新写入被拒绝，写入退出并恢复终态后才允许继续。没有编辑账本或缩短期限模拟崩溃/到期；这不是 PVE xrdp、默认 MCP 或正式服务安装验收。最终镜像 `sha256:4d21d95c599ebdb84da02c5c1a4c079729b188e6a508cd4705cd18156af0adfa`。
  - 原有 Linux 双 UID 截图/AT-SPI/真实输入/重放、Helper 重启和授权 epoch 故障矩阵继续通过；既有 GTK bounds 警告仍有，不计新功能证据。Linux Rust 26 项常规、独立崩溃子进程测试及 6 项状态机通过，Mac workspace/两端 Clippy 通过，Go 全量 test/vet 与 Computer/Guestidentity race 两轮通过（38.20/1.43 秒）。本轮未重跑 Windows SAM/RDP 或独立数据库集成，不能把上批结果计为新版全链路验收。
  - 默认 Linux 仍调用旧式改密/启停，尚须先持久绑定 UID、分配登录 generation，再把登录派发、授权和撤销接到新协议；两端 Native `vcw`、Windows Broker、升级排空/服务恢复及其余 P0–P2 保持未完成。测试容器自动清理，未替换任何 PVE Guest；读取确认 VM 9112/9113/160 停止、VM 158 的 XFCE PID `52975`/启动时间不变，原开发库仍为 migration 013。REVIEW-12 和总目标继续进行。

- 2026-09-06（Guest 账号写入版本保护，继续实施）：

  - 新增 `crates/guest-lifecycle`，固定 UID/SID、Lease、单调 epoch 与不可变登录窗口；Windows `account-lease` 在 SYSTEM-only 账号门内先刷新 `opening/sealing/revoked` 意图，再执行实际 SAM 改密/启停，最后提交完成状态。`open` 不接受同版本重放，`seal` 退役密码但保留桌面，终态或旧 epoch 不能复活。已有 Lifecycle 的账号拒绝无版本 enable/disable，不开放默认 Windows MCP。
  - 原生测试新增真实网络认证的密码生效/退役检查，以及在实际意图写入后、启用后、退役后直接结束专用子进程的恢复断言；不创建交互 Profile。Windows 11 VM 9113 首轮 53 项常规 + 17 项账号测试通过（111.43 秒，产物 `dc4805e6e0a42dc0088c690a73f709e0a19ffd4e46fe36ac2be20fac0e3821b4`）。随后收紧失败路径为只立即锁定登录、不再多跑一轮 30 秒注销；最终 Windows 10 VM 9112 同样 53+17 通过（137.33 秒，产物 `5a321076002912b1abc765298382f1ecbc7cc5d00495ebd56eb1565f11ef8d28`）。两轮均独立检查 SAM/注册表无残留并恢复停止。
  - Go 组件只发送一次绑定的账号写入，缺失、旧/错配、未完成、取消或包含额外秘密字段的回执均拒绝；只读观察携带生命周期记录，未提交状态不当作清理完成。Computer/Guestidentity race 两轮通过（38.17/1.41 秒），Go 全量 test/vet 通过；Mac Rust workspace、Windows Clippy/release、5 项可移植状态机测试通过。数据库集成未配置独立实例的跳过不计新增数据库证据。
  - `make pve-live-windows-lease-check` 在 Windows 11 VM 9113 通过（626.94 秒）：两个独立 SID 在真实 RDP 前分别固定五分钟窗口，经同一 Go API open 后认证；seal 退役引导密码后，RDP、原 WTS 登录和 Helper 实例均保留。首用户主动 revoke，第二用户等待原期限后调用 Guest 本地巡检入口；独立观察确认禁用、WTS 消失及原 Lease/epoch 的 `revoked` 终态。未改账本或缩短期限模拟到期，未替换正式安装的 Guest，因此不计后台服务安装/自动恢复验收。产物 SHA-256 `6bcca28c33e93a45189efb20c2b6b3a590199ee84829fa863ba12c1b59af2642`；OS `10.0.26200.0`、QGA 文件 `109.1.0`/Session 0。没有输入矩阵、默认 SDK MCP、首次登录模板或可视 Mac App 验收。
  - 使用内网源重建并通过 Linux 一次性图形隔离门禁：Rust Guest 26 项、状态机 5 项及 workspace/clippy/release 通过，真实双 UID 截图/AT-SPI/输入/重放、Helper 重启、权限/期限及原授权 epoch 矩阵通过；既有 GTK bounds 警告不计为新增功能证据。这是 Linux 现有链路回归，不代表 Linux 已接入账号生命周期 fence。
  - 本批账号、Profile、注册表、任务、暂存文件及下载服务已清理，VM 9112/9113 恢复停止；未改宿主驱动，VM 158 的 XFCE PID `52975`/启动时间保持，原开发库仍为 migration 013。REVIEW-12 仍未关闭：一个 epoch 仅允许一次凭据打开，后续需持久登录 generation 支持同租约重连，不能临时伪造更高版本。Linux Agent 与两端 Native `vcw` 的用户名式改密/启停、登录前身份绑定、旧 Guest 命令排空和 Windows Broker/输入授权整体接线尚待实施；其余 P0–P2 总目标不变，协议和边界见 [ADR 0003](../decisions/0003-computer-use-data-plane.md)。

- 2026-09-06（Windows 执行器、注销与 QGA 恢复，继续验收）：

  - 新增 `WindowsSessionExecutor`，正式组件实现协议探测、SYSTEM 账号归属、SID/WTS/LUID/实例发现、授权和原字节结果重取；成功响应必须匹配操作类型。实机正常操作经过同一组件，负向请求仍到达 Guest 验证。默认 Windows MCP 仍关闭，不能把单组件通过当作账号/Lease/进程所有权、安装恢复和无人值守模板闭环。
  - 修复了实际暴露的输入与授权问题：Unicode 每块最多 32 字符、代理对不拆开、虚拟键快捷键批量提交；每块重验权限，不用剪贴板/控件赋值。WinForms 夹具改用框架内置的多行快捷键支持，先检查 24 个 UTF-16 单元的真实选区，再核对 549 字节混合长文本及两次精确回显。两个 OS 的撤权统一使用过期值 `0`，避免 Guest 时钟略慢时拒绝控制面当前时间写成的 tombstone；epoch 与活动期限检查不放宽。
  - Guest `7df4b2f5ac7b6a797e4a61e2cf294aeddb15120151beab34d2a301fb55d58d38` 的 Windows 11 两用户短/长输入、精确重放、隔离、Helper 重启和撤权曾全部通过，但第二用户到期注销未在 10 秒内收敛，整轮仍失败。早期失败还包括首登页/Store 遮挡、只观察到文本前缀、旧单行夹具不处理 Ctrl+A、QGA 回执超时和 PID 丢失；均未以重发新输入、扩大授权期限或假回执掩盖。各轮资源已独立清理；其中最后注销失败轮的残留复查及关机通过（70.78 秒）。
  - WTS 注销现对完整 SID/WTS/LUID 每实例只成功派发一次，再持账号门最多观察 30 秒；身份改变重新核验、最多记录 1024 个实例，不把权限失败当作缺失，不强杀其他用户。观察/注销只跳过 Session 0 和无登录用户的监听端点，不再把重置/连接中等过渡状态当作缺失；Helper 输入就绪判定没有据此放宽。最终 Windows 11 VM 9113 的 53 项常规原生及 13 项账号测试通过（108.77 秒），含状态筛选、异步派发、编号复用、账号删除/SID 替换、权限与 gate 拒绝；产物 SHA-256 `46825cf7272e17696f8d9fbed72794a0a1bf167d6025d6660e56672f6788fdac`。前一构建在 Windows 10 VM 9112 也通过 53+13（142.56 秒），但未含最后的过渡状态筛选修改，不混为同版证据。两轮均清理并关机。Windows Clippy/release、Mac Guest 24 项与可移植身份 2 项通过。
  - 新增固定 SID 的结构化账号观察：同一受保护账号门内读取 SAM、实际 WTS Token/SID/LUID，再复核身份/状态一致；删除 SAM 仍保留账本 SID 及可能存在的登录实例，不接管同名用户。Go 拒绝旧协议、缺失字段、null 列表、不一致状态、重复登录编号及零值“成功”；真实登录验收已用此 API 检查登录前、已连接和注销后状态，不再匹配英文 stderr。该回执仅是当次观察，不是持久化 Lease 撤销确认。
  - 上一版 Guest SHA-256 `97cc8624503ae13252f9774f17fc1b3c6432a293eaafa9d820486e166b7d20ee` 的 Windows 11 完整交互轮因输入回执超过原 15 秒期限失败（324.58 秒），独立清理并关机。新增聚焦入口 `make pve-live-windows-expiry-check`，只测试真实 RDP 登录、显式停用、本地到期与 SAM/WTS 后置检查，不计输入/MCP/模板验收；Windows 11 首轮在第二用户到期处 QGA 超时并报告不可用，失败（294.32 秒），随后按精确 manifest/SID 恢复并独立确认无残留、关机（236.61 秒）。恢复时未见最近 QGA 服务崩溃记录，不能断言传输故障根因。
  - 固定只读读取和原字节动作重取新增单次最多 5 秒的回执观察预算，在原请求截止时间内最多三次；保留短调用方期限、取消和明确拒绝，不重放初始化/授权/账号写入。新增真实等待超时后取回同一结果、字节不变、调用方取消及迟到回执拒绝测试；最终 Computer/Guestidentity race 两轮通过（37.82/2.01 秒），Go 全量 test/vet 通过。未启用独立数据库的集成测试仍跳过，不重复计数据库验收。
  - Windows 10 VM 9112 使用上述上一版 Guest 和冻结的原生后台 Worker（SHA-256 `88836e528c9a4bcd44fea96b0f063b019815d229624faeb41086d9d8de909a34`）通过聚焦注销实测（338.34 秒）：两个独立 SID 真实登录、首用户显式停用、第二用户保持 RDP 时本地到期回收、独立确认账号禁用/WTS 消失；一次 capabilities 读取超时被新的限次重取实际恢复。Guest build `10.0.19045.0`、QGA 文件版本 `109.1.0`、进程 Session 0。账号、账本、任务、暂存资源独立清理确认，恢复停止；不是新版输入/MCP 或无人值守模板的整套通过。
  - 当批 Guest SHA-256 `0bfe119997088255b8af33818417c093db2f6850c6f5449dd44224092a5b86d8` 与同一冻结后台 Worker，在 Windows 11 VM 9113 通过最终聚焦验收（454.56 秒）：两个独立 SID 真实 RDP/首帧、重复 Helper 启动保持实例、结构化观察匹配实际登录、首用户主动停用、第二用户维持连接时本地到期回收，以及独立 SAM 禁用/WTS 消失断言。Guest build `10.0.26200.0`、QGA 文件版本 `109.1.0`/Session 0；capabilities 和 account-inspect 各一次 5 秒只读回执超时由限次重取恢复，未重发有副作用命令。账号/账本/任务/暂存资源独立确认已清理，正常关机；该轮没有输入、默认 SDK MCP、首次登录模板或可视 macOS 客户端验收。
  - 代码复核新增 REVIEW-12：Linux 现有账号调用和 Windows 实验接口按用户名执行，尚不能在 Guest 写入点挡住迟到的旧租约改密/启停。先补持久生命周期版本与凭据阶段保护，再完成 Windows Broker 所有权、安装恢复及默认接线；不能以动作授权 epoch 或单次注销观察替代。正式安装中的 Agent 未替换，未升级 QGA/宿主驱动。结束时 VM 9112、9113、160 均停止，业务 VM 158 的 XFCE PID `52975`/启动时间不变；原开发库仍为 migration 013。其余 P0–P2 总目标不缩减，协议及验收入口统一见 [ADR 0003](../decisions/0003-computer-use-data-plane.md)。

- 2026-09-06（独立后台 RDP 组件）：新增 `apps/session-worker` 与 `internal/rdpsession`，只维持真实交互连接，不依赖人的 Mac App、可见 RDP 应用或 Xvfb。一次性二进制 stdin、私网地址/受管用户名/证书摘要/有界期限校验；NLA 与严格 pin、拒绝重定向、禁用设备通道；首帧和协商双回执、取消/父管道/双时钟期限、异常回执收回与强制子进程回收。首次实机两轮失败（96.40/99.35 秒），均在进入真实桌面前退出且完整清理，不计通过；本机受控 TLS 端确认是 WinPR 初始化需要用户 Home，已原样保留实际 Home 并隔离 XDG/FreeRDP 运行目录，另补了连接初始化重置可能丢失取消的持续中止保护。

  Windows 10 VM 9112 最终双用户实测通过（604.49 秒，含首次登录与人工截图检查，不是性能指标）：后台 Worker 实际收到 1280×720 解码帧，Guest Helper 固定 SID/WTS/LUID、重复启动、截图/UIA、真实点击与中文/emoji、精确重放/冲突拒绝、重启/旧实例拒绝、第一用户显式撤权，以及第二用户保持连接时本地期限到达后的 SAM 禁用/WTS 消失均通过。每用户首次登录的 Edge 欢迎页按实际截图关闭，没有修改系统策略。冻结的 macOS 原生 Worker SHA-256 `0c5f29b9cbb514dc698260488db6fde2ac11d0c3477c5c6d8c51619695b361dc`；Guest SHA-256 `8ace2caf2fb04498062ccaffa0585fdda0be27ae3ce6ec9e3200330d2311d046`。独立确认临时账号、账本、任务和产物不存在，VM 正常关机，安装中的 Agent 未替换。Windows 11 使用只链接核心库的冻结 Worker 已到达首帧、Helper 就绪及真实测试窗口，但 Store 抢走焦点后的辅助点击已超过截图期限，保护逻辑中止该轮（644.70 秒），未计通过；临时资源独立清理确认通过，VM 9113 恢复停止。随后改为丢弃过期点击并重新截图，必须再次显式提交新命令，不把旧坐标重放到新画面；60 秒点击期限、5 分钟总预算和 20 帧上限不扩大，边界 race 测试两轮通过，Windows 11 整轮仍待复验。

  macOS CTest、Go 监管 race 两轮和实际原生 TLS/管道/期限回归通过；最终只链接核心库的 Worker SHA-256 `88836e528c9a4bcd44fea96b0f063b019815d229624faeb41086d9d8de909a34`。`session-worker.Dockerfile` 使用内网 Debian/安全更新镜像，固定 FreeRDP 3.31.0 源码摘要，最终通过 Make 入口构建并验收的 Linux ARM64 镜像为 `sha256:fded46555181af9ce65150b5b4815be30e553113de6969d9b629ea4cb76cb22f`；在无外网的专用 Docker 网络、非 root、只读根文件系统、无 capability 条件下，真实原生错误证书在认证数据之前拒绝，以及握手中父管道关闭、额外输入、期限到达和 Go 监管全套测试通过。可重跑的容器脚本含资源身份验证与清理，CI 已接入但未远端运行。Mac 当前 Docker 的 amd64 模拟执行返回 `exec format error`，没有修改宿主模拟器；amd64 目标构建/实测仍待完成。ARM64 构建期间补齐 ICU 并去除不必要的客户端公共库/FUSE 依赖，不把失败构建算成通过。Go 全量 test/vet 通过（未配置独立数据库的集成测试仍为跳过，不重复计为数据库验收）。测试容器与专用网络已清理；原开发库仍为 migration 013，VM 158 原 XFCE PID `52975`/启动时间不变，VM 160 保持停止。

- 2026-09-06（Windows 本地账号到期回收，交互验收继续）：补充 SYSTEM Guest 心跳内的本地到期检查及 `computer-v2-accounts-reconcile`。SAM 读取改为同一 `USER_INFO_4` 快照，Helper 启动拒绝过期/无限期账号；到期回收仅枚举受保护账本，持账号门复核固定 SID 与最新有效期，再停用和 WTS 注销，不删除身份/Home、不按名称前缀接管，续期不会被旧观察回收。Windows 11 VM 9113 的 43 项常规原生测试和 11 项显式账号测试全部通过（107.81 秒），包括到期/无限期、门冲突、续期复核、同名替换和不安全账本拒绝；原生产 Agent 未替换。原生验收产物 SHA-256 `c20a012bdc0f6c4f77a841b78f5382b77edbcfdd4455e46b583c75e0468a79f8`。

  完整桌面首轮到达 Helper 就绪但测试窗口不显示，后续截图确认首次登录的跨境数据确认页以及 Shell 自动打开的 Store 会阻挡/抢走前台。验收顺序调整为先检查已绑定桌面，再要求测试窗口 Shown、前台焦点与真实输入，不删除这些断言。此前四轮分别因窗口未显示、QGA 超时、过期截图保护、人工检查超时而失败，均不计通过；各轮均独立确认临时资源已清理并正常关机。辅助截图新增采集/点击截止时间，人工检查预算与实际动作期限分离；只在明确授权下逐帧点击该确认页、关闭临时用户的 Store，不改系统隐私/地区策略。

  最终 Windows 11 VM 9113 两个顺序真实 RDP 用户整轮通过（1153.73 秒，包含首次登录与人工截图检查，不是性能指标）：固定 SID/WTS/LUID、重复启动、JPEG、UIA、物理鼠标焦点、中文/emoji、精确重放不重复输入、冲突/错身份拒绝、Helper 重启/旧停止拒绝及撤权均通过。第一用户显式停用注销；第二用户保持 RDP 连接，设置 30 秒本地登录期限，到期后执行同一个本地回收函数，独立确认 SAM 已禁用及 WTS 登录消失，未调用远程 account-disable 完成该断言。显式清理回执与独立身份/任务/产物残留检查通过，VM 恢复停止，安装中的 Agent 未替换。Guest SHA-256 `8ace2caf2fb04498062ccaffa0585fdda0be27ae3ce6ec9e3200330d2311d046`。Windows 10 VM 9112 同一原生产物亦通过 43+11 项回归（115.59 秒），测试产物清理并正常关机；实际心跳进程/安装恢复、无人值守登录、Windows 10 到期图形复验和默认 SDK MCP 仍未验收，默认 Windows MCP 保持关闭。Go 全量 test/vet、PVE/Computer race 两轮、Guest 24 项与可移植身份 2 项测试、Windows/macOS Clippy 及 Windows release 构建通过。VM 158 原 `vdi` XFCE PID `52975` 与启动时间不变，VM 160 保持停止，原开发库仍为 migration 013，未升级。

- 2026-09-06（Windows Helper 生命周期与 QGA 完成语义）：新增受管账号生命周期门内的真实 WTS 用户进程启动，固定 Agent 镜像/命令和用户环境、不继承 SYSTEM 句柄；挂起创建、Job 回滚和握手的实际进程绑定通过后才允许存活。Helper 存活与 `input_ready` 分开，等待首次桌面或锁屏不反复重建，授权发现和实际动作仍拒绝不可交互桌面。停止绑定完整实例，旧请求不能关闭替代实例。交互夹具改用实际 Agent 账号与启动/停止路径，只有测试输入框仍使用登录任务，不以该任务代启动 Helper。

  Windows 10 VM 9112 与 Windows 11 VM 9113 各通过 42 项普通原生测试和 9 项显式账号测试，新增真实 Job 回滚/解除回滚、同登录身份下的错误进程管道拒绝，以及启动门与账号停用互斥。原生测试 SHA-256 为 `c2145611b423878c847896d99c4f889ef2ffe611581bb6c7754c91c70c9a31d9`。Windows 11 还通过真实 Go/PVE 客户端的普通非零退出和异常式退出断言；Windows 10 原始 QGA 探针确认 `signal=-1073741819` 时不存在 `exitcode`。Linux VM 158 等价探针通过，仅运行输出/退出/终止自身的短命子进程，没有写入 Guest 状态。

  实测暴露并修复通用 PVE 执行器将缺失 exitcode 当作 0 的错误，异常和截断输出不再确认成功。先前一次交互夹具错误报告清理完成，后续开机检测到残留并拒绝进入新测试；按旧 manifest、完整 SID 与账本独立核验后精确清理并确认不存在。原失败没有保留原始 signal，不能据此断定那次 PowerShell 异常的具体原因。清理现在要求固定完成回执，再独立检查精确用户名/登记键/暂存资源；manifest 已消失的恢复检查须显式提供两个原用户名，不能靠 Agent 前缀推断归属。

  最终 Windows 10 VM 9112 的完整交互夹具通过，用时 486.65 秒：两个顺序独立用户均完成真实 RDP 登录、同一 Helper 从等待到可交互且重复启动不换实例、JPEG/UIA、物理鼠标坐标、中文/emoji 文本输入、精确重取不重复输入、冲突和跨身份拒绝、Helper 停止/重启及旧停止请求拒绝、授权重绑、撤权与账号停用/WTS 注销。两次人工只读查看隔离桌面截图后继续自动断言，没有点击隐私/许可确认；输入框实际获得前台焦点。测试 Guest SHA-256 为 `94147fd4108b045fb9b65f2b1c05488b98e11de9de10b0cdba8d21b9a5a1807a`，没有替换已安装 Agent。清理回执和独立账号/注册表/任务/暂存目录缺失检查通过，VM 正常恢复停止。该证据是测试 RDP 客户端→真实 OS→新 Guest Helper，不是 macOS `.app` 或默认 MCP HTTP 链路，也不是 Windows 多会话服务器。

  Mac 上 Guest 协议 24 项、可移植身份 2 项、Windows 交叉编译/Clippy、Go 全量测试/vet 和 PVE/Computer race 两轮回归通过。Windows 11 完整交互、默认 MCP、无人值守登录与服务恢复仍未验收，不关闭 REVIEW-04。两台 Windows 验收机均恢复停止，VM 160 保持停止；VM 158 的原 `vdi` XFCE PID 52975 和开发库 migration 013 不变。Guest 测试账号/任务/登记及暂存资源、临时 RDP 容器与产物服务已清理，本机仅保留两组隔离桌面 QA 截图及帧绑定元数据。

- 2026-09-06（Windows Agent 账号归属与本地生命周期）：新增独立 SYSTEM-only 注册表账本，固定 `vca…` 命名空间、完整 SAM SID、持久 Pending 随机标记和排他生命周期门。创建为禁用标准账号，按 SID 加入 Users/RDP Users；已有外部账号、SID 被替换、损坏或权限被放宽的账本均拒绝。启用接受有界登录有效期；停用不删除固定身份，创建前失败和中断创建后的清理可重复执行。Guest 已接入 provision/enable/disable/inspect 实验命令并通过 Windows 交叉编译；默认 `computer_actions=false` / `unattended_login=false` 保持，Helper 安装启动、无人值守登录、真实双用户桌面/RDP 注销和控制面接线仍是 REVIEW-04 的剩余工作。

  Windows 11 VM 9113 与 Windows 10 VM 9112 均以真实 SYSTEM 执行同一最终测试产物，各通过 39 项普通原生测试和 8 项显式账号实测：稳定 SID/禁用状态、外部同名拒绝、中断恢复、删除/同名重建拒绝、互斥、内核禁止账号读取账本、不安全 ACL 拒绝及失败 Bootstrap 幂等清理。首次实测发现 WTS Listen 端点查询 Token 返回 Win32 error 2，导致账号测试清理失败；已排除非用户端点并补单测，清理失败也不再因析构器二次 panic 中止整个套件。两名残留禁用账号按完整 SID/归属标记和无 Profile 条件精确清理后复验通过。原生测试 SHA-256 为 `ad91bc894640041464a0dc07adcfe1b7a8dc571ef93c9f3fa8fc6cb3e635cc07`；这不是已部署 Guest Agent 的摘要。

  Windows 10 首轮读取回执遇到 QGA 超时，未把超时当作套件未运行或立即重发；旧测试句柄终止、清理完成后才复验。读回执已补有界观察超时分类与测试。两台并行启动曾使 node4 可用内存降到不足 0.5 GiB，正常关机 VM 9113 返回 `VM quit/powerdown failed`；在已独立确认无用户登录、测试账号/归属键全部清理后，仅对此验收 VM 执行 PVE stop 释放资源。VM 9112 后续验证及正常关机通过。入口现增加启动前内存余量检查；后续同节点串行，不把该快照视为生产调度预留。最终两台恢复停止，正式 Agent/模板/宿主配置未改，原 VM 158 的 `vdi` PID 52975 与原开发库 migration 013 保持。临时账号、随机归属键、产物服务和精确暂存文件均已清理，不保留测试业务数据。

  Mac 上 Guest 协议 22 项、跨平台身份 2 项、Windows 交叉编译与 Clippy、Go 全量测试/vet 及 Computer 模块 race 回归通过；新增的两项 Go 验收门禁另有单测。未运行本轮真实 RDP 登录/注销或 Mac UI，不以无登录账号测试关闭 Windows 整体功能。

- 2026-09-06（Agent 账号持久绑定与 MCP 复验）：026 将 OS 平台、Linux UID / Windows 完整 SID 与临时登录会话分开持久化。首次确认后禁止清空、改绑或由另一 Agent 复用；释放/停用仅清空 Session/Helper 实例，重新领取保留账号和 Home。控制面必须先核对已绑定身份才能发布授权，UID 改变会关闭该精确租约并排队清理。Windows 数据库支持不等于开启 Windows MCP，原默认拒绝门禁保留。新增 11 项测试，覆盖两个平台、SQL 直接修改、并发首次绑定、旧库迁移保留活动 Linux 身份、Helper 重建、身份错配时不发布授权和过期安装产物拒绝；PostgreSQL 真库 `-race -count=2`、Go 全量测试、vet 与 diff 检查通过。025 旧库升级测试修正为写入当时的旧表结构，不用当前 Store API 伪造旧库。

  隔离 VM 160 的旧镜像缺少 AT-SPI/PipeWire 运行依赖，已从既有 `10.31.0.2` 源补齐，未升级其他已安装包。旧缓存 Agent 虽可启动，但真实 MCP 被 `stdin_epoch_v1` 门禁拒绝；安装测试现会在暂存中核对协议能力，再替换产物。当前源码 amd64 构建安装后，真正 SDK HTTP→控制面→PVE→独立 xrdp 桌面完整验收用时 130.26 秒：首个独立账号 UID 1003，桌面 1600×900、图片 640×360；语义定位、坐标换算、输入、MCP 服务重建、凭证轮换、释放/重新领取、后台停用、第二账号 Home 隔离、撤分配及脱敏审计通过，共 10 次成功 / 3 次拒绝动作。每个阶段同时核对数据库持久账号与真实 Helper 身份；重新领取 UID/Home 不变而实例更新。

  结束后已删除本批两个一次性账号/Home/归属记录、暂存目录和测试数据库 `vc_workspace_agent_binding_20260906`，不保留测试业务数据；没有遗留 Xorg 或随机 schema。Guest 撤销栅栏保留在 epoch 7，不清空历史。xrdp 配置和旧服务二进制摘要不变；保留新 Agent、运行依赖及旧缓存产物备份供隔离复验，新 Agent SHA-256 为 `d69eb996c8ef3ecdbc74ee2a3e0cd133fd06e4f836dc7b505df9ebac66364238`。VM 160 正常关机，GVT-g V5_4/V5_8 容量恢复 1/2；VM 158 原 `vdi` PID 52975 保留，原开发库仍为 migration 013。旧共享 `vdi` MCP 运维流程已改为历史说明，升级文档明确 026 不支持旧控制面混跑/直接回退。Windows Guest 生命周期、原生客户端与其他 P0–P2 仍按上表继续，不标记总目标完成。

- 2026-09-06（macOS 生命周期、冷登录显示与全屏收尾复验）：新增可注入但不改变原生 C ABI v4 的传输/控制面边界；原生会话先注册后显式开始观察，任务和会话各自绑定 UUID。覆盖立即失败、旧任务收尾、同 VM 旧回调、关闭/注销的同步终止、取消恢复后立即新连、首帧稳定期和传输收尾瞬间取消；另拒绝越界 RDP 端口，避免整数转换崩溃。新增 9 项生命周期测试。临时副本分别取消所有权保护、恢复构造内观察、退回旧显示重试上限时，对应回归确实失败；仓库没有保留这些故意破坏的实现。

  真实签名 Release `.app` 在隔离 VM 160 的全新 OS 用户上复现冷登录仍停留 1440×900：xrdp 明确记录五次 `Not allowing resize. Login in progress.`，Xorg 就绪前原有重试已经耗尽。显示请求改为前四次 500 ms、随后 2 秒退避，总窗口 30 秒且最多 20 次；8 秒回退仍保留。修复后另一全新用户无需点击重试，约 2.6 秒从初始尺寸自动收敛到 5120×2678。

  独立、最长 15 秒的 Guest 故障注入仅匹配测试 Mac 的 TCP/3389，最终包实测首帧前 `0x0002000d / firstFrame=0` 后约 7.82 秒自动恢复；另两次已连接 reset 后约 7.7–7.8 秒恢复，XFCE PID `1653`、DISPLAY `:11` 和终端现场保持。全屏下仍有遮挡的问题确认为独立的 AppKit 工具栏窗口：透明标题栏和 auto-hide 不足以消除它，改用 SwiftUI 显式隐藏全屏系统工具栏、退出显式恢复可见。最终包确认重连后 Guest 顶栏完整、全屏 5120×2804、退出后 5120×2678 及红黄绿按钮恢复，XFCE DPI 为 192。前一条记录中“透明标题栏已修复”的判断由本条取代。

  Swift 39 项（含 92 张明暗/双尺寸离屏图）、原生 C 24 条断言、Go 全量回归、弱网脚本安全门禁、Release 与 macOS 14 自包含深度签名验证通过；恢复页两个主题/两个尺寸均抽查。未将全套键盘/剪贴板记为通过：后段自动化输入出现缺字和意外快捷键，Guest 只读探针未见粘住按键，尚不能区分自动化输入机制与客户端问题；意外粘贴确认已取消，未执行其中内容。Windows、跨 1×/2× 屏幕、系统减少动态效果和持续弱网负载仍待验收。Intel HD 530 / Accelerated yes 再次通过；当前 RFX 软件编解码的限制和后续方案见 [GPU 基线](../operations/pve-development.md#gpu-基线)。

  测试结束后原生账号注销，两名临时 Guest 账号均完成撤权 `1/1`、锁定且无进程，再删除各自 Home/缩放标记；VM 160 正常关机，原 GVT-g 配置不变、V5_4/V5_8 可用容量恢复 1/2。VM 158 原 `vdi` PID `52975` 保留。一次性测试库已删除、测试服务和客户端退出，原开发库仍为 migration 013。负向测试副本移入本机废纸篓，渲染产物保留于 macOS `.build/ui-lifecycle-review`。最终 `.build/app/VC Workspace.app` 主程序 SHA-256 为 `7ca3d8596b41d8ad13415d10cc5f5914d772b1d243f3226215abddd28f86049d`。MAC-03/NET-01/GPU-02 的剩余项不标记完成。

- 2026-09-06（macOS 网络恢复与全屏稳定性）：在隔离 GVT-g VM 160 上使用签名、自包含的真实 Release `.app` 验收。明确复现原版在 TCP reset 后直接失败（`0x0002000d`），发现上游 Mac 循环未实现自动重连；现由 Swift 在 120 秒内最多恢复两次，每次重新签发授权凭据。多次 TCP reset 后约 7–9 秒恢复；Guest 的 XFCE PID `1128`、DISPLAY `:10` 与终端内容保持。35 秒出站断网期间取消后没有自行再连；恢复过程中撤销分配时，控制面拒绝新连接，最终界面显示“桌面不可用”，返回后卡片消失。

  弱网脚本实测 120 ms delay/20 ms jitter、6 Mbit/s 与 1% loss 的组合，45 秒窗口统计 366 包、丢弃 1 包；真实终端输入与全屏通过。另一次 10 秒完全丢包共丢弃 35 包，链路恢复后约 5 秒回到原桌面。数据只代表本次交互，不是持续负载性能分布或双向 RTT。正常回滚以及 SIGKILL 后独立 systemd 定时器兜底均恢复原 `mq + fq_codel`，不影响 QGA、Mac 全局网络或 PVE 宿主。

  全屏重连稳定复现原生标题栏遮挡 Guest 面板；仅限制 MRDPView 的独立全屏调用不足以解决，最终通过外壳统一透明标题栏/全屏系统按钮生命周期修复，并修复重连后的全屏按钮状态。Release 实测完整 Guest 顶部面板、全屏 `5120×2804`、退出 `2000×1260`、系统缩放 `5120×2678`；普通窗口的红黄绿控件恢复。Intel HD 530 硬件渲染再次通过只读门禁，编码日志为 RFX；H.264/硬件编码没有冒充完成。历史首次失败没有错误码，本批修复了构造期间过早终止回调并补脱敏日志，不能断言旧失败全部同因。

  Swift 30 项测试（含 92 张明暗/双尺寸离屏图）通过，抽查恢复/不可用页面；Go 全量测试与 vet、弱网脚本安全门禁、原生 C 11 条断言、Release 构建和 macOS 14 自包含深度签名验证通过。未执行 Windows、跨 1×/2× 屏幕、完整键盘/系统减少动态效果或持续负载基准；MAC-03/NET-01/GPU-02 保留剩余 TODO。完成后移除本批测试账号/Home、网络脚本与缩放标记；撤销队列最终 `3/3`，无残留 timer/qdisc，VM 160 恢复停止、GVT-g 容量 V5_4=1/V5_8=2。VM 158 原 `vdi` 会话 PID `52975` 仍运行。测试库 `vc_workspace_network_20260906` 已删除，测试控制面和客户端已退出；原开发库仍为 migration 013，未升级。最终包位于 `clients/macos/.build/app/VC Workspace.app`，主程序 SHA-256 为 `ba3e574d8fedc689a22c59922227c07c3058fab69521f70a12ed5a021f009b20`。

- 2026-09-06（GVT-g 实际渲染与 Guest 显示适配）：在原本停止的隔离 VM 160 启动测试，没有给 VM 158 加 GPU 或改宿主驱动。同一受管账号修复前 `renderD128 open failed`、`llvmpipe / Accelerated: no`；初始化流程补充 render 组后，新 OS 会话为 `Intel HD Graphics 530 / Accelerated: yes`，Xorg 加载 iris 并启用 glamor。新增 opt-in `pve-live-desktop-renderer-check`，同时验证指定用户的唯一活动会话、renderer 和加速标志，不能用设备枚举或 `direct rendering: Yes` 代替。

  复用已签名的自包含 Mac `.app`，3 次新 OS 会话和 1 次保留会话重连均拿到画面；普通窗口 `2000×1260`、最大化 `5120×2678`、全屏 `5120×2804` 及退出恢复值均从当前用户会话独立核对，终端输入通过。新无特权显示辅助程序把顶栏行高从 26 调到 52、Dock 从 48 调到 96，重连保持幂等；把终端放到 5K 右下角再缩回后，实际外框回到工作区内，标题栏和底边完整可见。实测修复缺少 x11-utils，以及 wmctrl 的重挂接坐标/窗口装饰偏移，依赖已加入正式模板配方；旧模板升级需要补依赖并新建 OS 会话，不在普通建联时自动 apt 或强制关闭用户应用。

  Go 全量测试/vet、Guest 显示 11 项回归、相关 Go 测试、镜像语法检查与原有 `.app` 自包含深度签名验证通过；临时在测试进程中禁用窗口恢复时，4 项断言如期失败，未修改仓库实现。新模板未完整实建，Mac 本轮未改二进制，也没有测编码/带宽/输入延迟基准。首次建联问题这轮未重现但不能宣称已修复；一次大屏拖动自动化后连接结束，Guest Xorg/Intel 会话仍存活；全屏截图还需复核原生顶栏与 Guest 面板重叠，MAC-03/GPU-02 保持未完成。

  收尾已确认 Guest 撤销 revision `3/3`、账号锁定且无进程，删除一次性账号/Home、显示请求标记和独立测试数据库；原开发库未升级。VM 160 正常关机、V5_4 容量恢复为 1，保留已安装的显示辅助程序及诊断依赖供后续隔离验证；VM 158 的原 `vdi` 会话仍在。测试控制面与客户端均已关闭。

- 2026-09-06（macOS 最大化/缩放修复与 GPU 复核）：修复 FreeRDP 把远端像素尺寸写入内嵌 AppKit 视图的问题；ABI v4 分开跟踪请求尺寸、resize 确认与实际非空 EndPaint。最大化、拖动和全屏统一按最新目标画面结束过渡，8 秒后有界回退并允许显式重试，状态刷新不重发协商；过渡只覆盖画布，保留顶部操作。原生补丁仅把位图释放/发布放到主线程，GDI/EndPaint 保留协议线程；调试时曾因整体搬迁 resize 产生主队列自同步崩溃，已依据 crash stack 修正，不能用该失败构建当验收。另修复 BSD patch 反向探针自动改向而误报“已应用”的构建门禁。

  最终签名、自包含 `.app` 连接隔离账号的 Debian 13 VM 158，普通窗口 `1360×844`、系统缩放 `5120×2678`、全屏 `5120×2804`、宽窗口 `2000×1260` 及恢复值均从该用户的 Guest 会话独立读取通过；XFCE DPI 为 `192`。实际完成两轮最大化/恢复、两次全屏往返、拖动边缘，以及最大化后的 Home 双击、终端输入和全屏/宽窗口输入。过渡采样未再见白屏，顶部断开/全屏按钮始终可见；并非逐帧录像或弱网性能验收。首次建联中断后重试成功的问题仍可复现；XFCE 面板 DPI/位置和缩小后应用窗口留在屏外属于仍需修复的 Guest 体验，不能据此把 MAC-03 标为完成。

  Swift 25 项通过、1 项离屏渲染按默认条件跳过；Release、原生依赖编译、自包含深度签名及 macOS 14 目标检查通过。新增 C 状态回归 11 条断言全部通过；临时把等待阈值退回 600 ms，断言确实失败，恢复后重新通过。GPU 只读确认 VM 158 无 render 设备且使用 DRISWRAST，VM 160 保持停止且带原 GVT-g 配置；GPU-02 补入 Linux 对照验收，未修改 GPU 或宿主驱动，详见 [GPU 基线](../operations/pve-development.md#gpu-基线)。

  验收后撤销测试授权，确认 Guest 撤销 revision `1/1`、账号已锁定且无进程，删除本轮临时账号/Home 与 `vc_workspace_resize_20260906` 测试库；原 `vdi` 会话 PID 52975 保留，原开发库仍为 migration 013。测试控制面/客户端均关闭，测试凭证已注销。已验证的新包复制到常规 `.build/app/VC Workspace.app`，旧包可从 `.build/previous-app.4uasO9/VC Workspace.app` 恢复；未推送仓库或发布 Release。

- 2026-09-06（macOS 原生外观实机复验）：Mac 解锁后运行独立签名、自包含的 `.app`，使用隔离临时 PostgreSQL 库和专用本地用户，仅授权既有 Debian 13 VM 158。实际通过空库、卡片、无结果/多词搜索、⌘F/⌘R、Return 提交及错误恢复、Esc 取消、搜索往返保留、Keychain 重启恢复、账户退出、设置检查和离线启动。最小窗口实测宽 680 pt；用户名/密码/服务器地址已具备可访问名称。离线启动不再等待网络恢复，system 探测请求超时收紧为 8 秒，Guest 建联长请求预算保持不变；错误密码统一中文，设置检查结果在已登录时也显示。

  实际进入独立用户的 XFCE 桌面并打开文件管理器；从该用户的 Guest 会话读取分辨率，普通窗口 `1640×980`、全屏 `5120×2804`、最大化 `5120×2678`，XFCE DPI 为 `192`，菜单断开和重试可用。只读分辨率验收新增 `VC_WORKSPACE_LIVE_DESKTOP_USERNAME`，不再把旧共享 `vdi` 的结果当成新用户证据。仍捕捉到最大化瞬间白屏，首次连接发生一次中断后重试成功（原因未定位），XFCE 顶栏时钟有截断；这些未通过项保留在 MAC-03。Dock 自动化入口超时，未证明实际 Dock 点击恢复；系统外观/减少动态效果未改动，完整键盘与动效矩阵仍待实测。

  Swift 单测 25 项通过、1 项离屏渲染测试按默认条件跳过，Release 编译和自包含签名检查通过；Guest 分辨率探针三轮通过。结束时撤销临时授权，确认 Guest 撤销 revision `2/2`、账号锁定且无进程，再删除临时 Guest 账号/Home 与测试库；原 `vdi` 会话保留，原开发库仍为 migration 013，未升级原库。测试控制面及客户端已关闭；独立测试包保留，未替换原启动包。MAC-03 不标记完成。

- 2026-09-06（macOS 原生外观首轮）：拆分登录、桌面库和共享样式，移除装饰水印、重复占位及设置检查时的重叠小转圈；增加统一按压弹簧、焦点边框、SF Symbol 替换和前台活动条。复核离屏原生渲染时修复空库/错误页不填满窗口、VM 编号被加千位分隔符，以及 macOS 26 系统表面同色造成的层次缺失；登录重入保护、搜索状态保留与 ⌘F 入口已实现。Swift 测试 24 项（含渲染目录）通过，生成 84 张明暗/双尺寸 PNG 并抽查关键页面；主操作文字对比度、活动时钟条件和背景覆盖有自动断言。Release 编译通过，独立测试包复用已验证的内嵌 RDP 依赖，不替换正在运行的旧版。Mac 当前锁屏，真实 `.app` 交互、动效与 RDP 回归尚未执行，MAC-03 保持未完成；本批未修改 PVE/Guest。

- 2026-09-06（全面 review 修复批次十四，Windows 交互复验）：部署方明确授权后，已通过真实绑定会话截图与一次性点击处理临时 Windows 用户的“个人数据跨境传输”确认页，未改全局隐私/地区策略，也未伪造注册表同意状态。新增默认关闭的辅助验收入口，截图摘要/坐标/时效受限；首次 Shell 弹出的开始菜单另作遮挡处理，实测输入框真正取得 UIA 前台焦点。失败诊断改为当时截图，只读就绪截图允许在一分钟内最多四次新读取，键鼠和授权不沿用此规则。

  实机随后发现中文/emoji 的 QGA stdin 被 PVE `Wide character` 拒绝，已增加等价 ASCII JSON 编码、代理对/长度边界和不可变重放回归；修复后的实机轮次尚未重新走到文本输入断言。QGA 暂不可用时只在原调用期限内 GET 同一已知 PID，不重发启动命令；清理恢复先只读核对精确标记，遗留恢复实测通过 92.98 秒。后续多轮因 QGA 回报超时或动作拒绝失败，不计为通过；最终一轮 218.98 秒在授权启动回报超时处终止，未盲目重发授权。各轮临时账号/Profile/任务/V2 注册区/产物目录和 RDP 容器均已清理，Windows VM 9113 恢复原停止状态，已安装 Agent 未被替换；本批 Guest 验收产物 SHA-256 `f72e9fac8474375a11e061bd9c903840b50174aab8662a85aa8c9bf9dba6201b`。Go test/vet 与 PVE/Computer/MCP race 两轮通过（未配置真实 PostgreSQL）；最后的只读重试次数/总期限收紧另经 PVE/Computer race 回归，未重跑实机。双用户整轮、Windows 默认 MCP、生产账号生命周期及无人值守初始化仍未闭环；本批只解除了临时验收的隐私点击阻塞，不能视为模板初始化完成。

- 2026-09-05（全面 review 修复批次十三）：Linux 默认授权发布不再使用共享暂存文件，改用 `stdin_epoch_v1`、root 私有排他锁、持锁比较/Helper 复验及 `authority-fenced.json` 原子提交。拒绝迟到旧 active/revoke、同版本撤权后激活、换 Lease/用户和缩短有效期；保留过期历史排序意义，损坏历史不重置。025 数据库迁移统一分配 VM 版本、关闭旧活动租约并排队清理，Bootstrap 失败关闭精确租约，新领取持续递增，旧清理不借用新 active 的 epoch。真实 PostgreSQL 的 024→025 升级、重复迁移、历史租约删除、旧撤权/新租约隔离和 HTTP tombstone 版本回归通过；Go 全量 test/vet、Store/HTTP/Computer/MCP race 两轮通过。Linux Guest 24 项、macOS Guest 22 项、Mac/Linux/Windows Clippy、Linux amd64/Windows release 交叉构建及 OpenAPI YAML 检查通过。

  图形容器进一步验证真实发布者持锁冲突、被杀后恢复、旧路径写入不影响新 Helper、仅清理自身遗留暂存、首次迁移必须先撤销和损坏旧记录拒绝。一次实机首图失败后复查发现租约 Base64URL 的 `-` 被 Guest 误拒，已修正 Linux/Windows 校验，并把固定 `-`/`_` 样例纳入协议单测和完整图形输入回归。最终 Linux amd64 Guest SHA-256 `d69eb996c8ef3ecdbc74ee2a3e0cd133fd06e4f836dc7b505df9ebac66364238` 已安装到 Debian 13 VM 158，原二进制以 `.previous-<hash>` 保留。最终真实 SDK MCP HTTP→控制面→PVE→Guest 实测通过 129.85 秒：无人值守首图约 10.2 秒、640×360 图片/1600×900 桌面；图片坐标、AT-SPI、真实输入、服务重建、令牌轮换、释放/重领及 UID/Home 保留、双 Agent 隔离和后台停用/撤分配回收通过，审计 10 条成功/3 条拒绝且敏感内容不入库。各轮临时账号/Home/schema 均已清理，原 UID 1000 的 Xorg PID 52909 和 xrdp 配置摘要不变；最终 Guest 保留 epoch 18 revoked 栅栏，不重置授权历史。一次性 PostgreSQL 和图形容器已回收，业务数据库未迁移。本批不包含 Windows 实机/默认 MCP、macOS 观察接管、新模板或弱网基准；Windows VM 9113 未启动或修改。协同升级步骤、025 撤销旧租约的行为和数据库回滚后的拒绝边界已同步 ADR/MCP 使用文档。

- 2026-09-05（全面 review 修复批次十二）：Windows 实验授权改为 VM 级单调 epoch 栅栏和注册表事务，一次提交全局 Fence 与所有登记用户的授权；禁止旧 epoch 覆盖新版本、同 epoch 撤权后激活、换 Lease/换用户及缩短有效期。提交期间重新发现 Helper，过期历史仍参与排序；没有 Fence 的旧记录必须先撤销，畸形记录拒绝迁入。跨平台 Guest 单测增至 21 项；Go 全量 test/vet、Computer race 两轮、Mac/Windows Clippy、Windows Guest release 构建及 Linux 双 UID 图形回归通过。Windows 11 VM 9113 原生最终通过 36 项（另有 9 个由正式测试调用的 ignored fixture），覆盖普通读取不见未提交值、并发写意图冲突、整批提交、重复/失败 stage、5 秒超时、进程直接退出回滚、模拟用户提交拒绝和 Fence 私有性；原生 12.70 秒、完整流程 85.80 秒。测试产物 SHA-256 `8df7db254f80c1a8c3db61aa0a77ab20019b56223ed1a606285e78478d0daa8e`，Guest release SHA-256 `b23f430fd222d4f9d2a15658f2f5985160cea525bdbb03aeec3f640338357fda`（仅构建，未替换安装中的 Guest）。

  补充测试先暴露了损坏 REG_SZ 样本的编码断言问题，已改为合法 UTF-16 错误类型样本后复验。另一次 QGA 启动结果超时无法计为通过，清理时测试文件仍被占用；随后按精确标记、ACL 与原产物摘要恢复，确认没有遗留注册表测试键，再删除该次暂存目录。原生验收现在只启动一次并保存私有完成回执，返回值不确定时只读取同一结果，不重跑套件；目录/文件白名单、进程占用和有界回执解析均有检查，新流程实机通过。所有本轮测试文件、下载服务及一次性 Linux 容器已回收，VM 恢复停止，未创建 OS 账号、接受隐私同意或改动已安装 Agent。Windows 双用户图形/生产生命周期仍未闭环，默认 MCP 仍关闭。复查新发现的 Linux 共用授权暂存、缺少 Guest 单调比较和控制面固定 tombstone epoch 由 REVIEW-10 作为下一项 P0 跟踪；不能据 Windows 基础修复宣称默认跨端授权链已经完整。

- 2026-09-05（全面 review 修复批次十一，Windows 交互验收推进中）：新增独立的真实 RDP 双用户验收入口，随机标准账号/私有任务/产物下载、证书指纹固定、账号归属核验与精确清理均可重复执行，不覆盖已安装 Guest。真实首用户登录已推进到 SID/WTS/LUID 发现、JPEG 与 UIA 读取；发现初次登录的桌面未就绪、逐属性 UIA 调用超时、点击后输入框未获焦点等问题。已补正常输入桌面检查、单元素属性批量读取与有界队列、物理像素鼠标定位，以及 QGA JSON 的 Unicode 转义；键鼠完整行为、第二用户、Helper 重启和撤权实机断言尚未全部通过，默认 Windows MCP 仍关闭。QGA 多次发生超时/结果丢失，这些轮次不计为授权拒绝或功能通过；验收仅对有防重放保护且身份/实例/请求字节/截止时间不变的动作有界重取，不自动重发账号、授权或清理命令。另修复验收时钟检查，把独立测量的往返不确定性纳入 3 秒偏差边界；失败遗留按精确标记恢复清理，VM 恢复原停止状态。跨平台 Guest 单测增至 17 项，Go 全量 test/vet、Computer race 两轮、Mac/Windows Clippy 及 Linux 双 UID 图形回归通过。REVIEW-04/AI-01 仍在进行；本条不是生产账号生命周期或完整 Windows MCP 验收结论。

  同批 Windows 11 VM 9113 底层原生回归通过 29 项（另外 8 项为正式测试调用的 ignored worker fixture），原生 6.58 秒、完整流程 75.84 秒；测试产物 SHA-256 `8c2a351ca29d84e8812307186b659eccfa1c482177c84ec07dfe409c5791db82`。新增桌面对象名称/借用句柄读取与 Session 0 输入拒绝断言；未创建 OS 用户或改变已安装 Agent，私有下载服务和测试文件已清理，VM 恢复停止。底层验收现在也可自动启动临时产物源，并按本轮 QGA 报告的 Guest 地址限制访问，不依赖过去的 DHCP 地址。

  后续真实失败截图确认，“输入未生效”轮次被新用户首次登录的“个人数据跨境传输”全屏确认页遮挡，不能据此判定键盘注入本身失败。已把测试应用的真实前台焦点纳入断言；临时测试 Profile 中的用户级受管 OOBE 策略未消除该页面，实验代码及 Profile 已清理，不采用为部署方案。未接受数据传输同意、伪造 Consent 状态或更改地区/全局隐私设置。Windows 交互验收目前等待部署方明确镜像首次登录与隐私初始化方案；键鼠、第二用户、重启和撤权整轮仍未通过。最后一轮测试资源已清理，VM 恢复停止；最近一小时的 QGA 服务日志未见异常退出事件，尚不能确定结果丢失的根因。

  本批次还发现 Guest 授权写入缺少单调 generation/epoch 栅栏，控制面互斥和输入防重放不能证明 QGA 丢失结果后的旧发布已经结束。后续 Windows 基础修复与 Linux/控制面剩余项统一由 REVIEW-10 跟踪，不能在未验证的路径上直接增加授权自动重试。

- 2026-09-05（全面 review 修复批次十）：Windows 实验 Guest 路径接入每用户 Helper、实例发现、stdin 调度与 Job worker。Helper/worker 拒绝 SYSTEM 和非交互身份；活动授权发布前通过内核认证的管道核对当前 Helper 实例，动作和缓存响应都验证 Lease/Epoch/SID/WTS/实例，执行期间撤权会丢弃结果并保留防重放预留。worker 改用单条长度定界 stdin，不等待 EOF；验证输出帧完整性、Request ID 和尺寸。Mac/Linux Guest 单测增至 16 项并通过，含重复输入只执行一次、缓存撤权、在途撤权及旧实例拒绝；macOS/Windows Clippy、Windows Guest release 构建、Linux 双 UID 图形隔离回归、Computer race 两轮和 Go vet 通过。Windows 11 VM 9113 的底层原生测试 27 项通过，新增认证后独立 I/O 阶段及真实 SYSTEM LSA LUID 核对；原生运行 6.54 秒、完整流程 84.93 秒，测试产物 SHA-256 `ddbb735da202e6690f2b59400d1a679982b7c076dd8e1370ba7f437dba2deec1`。该套实机测试仍是底层 SYSTEM/受限令牌测试，不是新 Helper 的真实用户图形验收；未替换已安装 Guest、未创建账号，测试文件与下载服务清理，VM 恢复停止。新增原生 arm64 的 Debian 13 FreeRDP/Xvfb 一次性验收镜像，普通/安全仓库可独立配置内网源，修正了镜像替换漏匹配和 Xvfb 作为 PID 1 等待就绪信号的启动问题；使用 tini 管理容器进程。镜像的离线启动与 stdin 参数/无凭据回显检查由 `make windows-session-client-check` 维护；它不代替真实 RDP、双用户、账号生命周期、默认 MCP 或生产无人值守登录验收。REVIEW-04/AI-01 继续进行中。

- 2026-09-05（全面 review 修复批次九）：新增 Windows 每用户注册表授权区，SYSTEM 创建最终私有 ACL，目标完整 SID 只读；拒绝权限放宽、SID 错配、注册表链接、错误类型、超大值和模拟身份线程的写入。初始化不覆盖已有 revoked，授权按单个有界值替换；固定 Guest 命令使用 QGA stdin 接收严格版本化快照。另补持久管道监听，整次认证/交换后断开客户端但保留实例，避免每次重建的抢占间隙，异常清理失败后拒绝复用。Windows 11 VM 9113 最终 26 项原生测试通过，原生运行 5.98 秒、含冷启动/下载/清理 81.19 秒；覆盖 12 次连续 256 KiB 响应、保留旧客户端句柄、空闲超时、身份拒绝、卡住客户端后的恢复，以及既有 Token/Job 回归。授权部分用受限令牌验证内核 ACL，这不是两个真实登录用户或 GUI/MCP 验收；8 个 ignored 项仍为正式测试调用的 worker fixture。最终测试产物 SHA-256 `8a5664e6aedde023a1781a68dc21bff29f690dc89f40794b924df6751e6439d0`，未替换安装中的 Guest、未创建 OS 账号。首次原生运行发现测试清理会跟随注册表链接，已改为打开链接本体后按句柄非递归删除，并验证不影响目标；留下的一个测试私有键经 ACL/内容/链接目标核对后精确清理。清理只读预检遇一次 QGA PID 回报丢失后重试成功，未放宽生产动作重试。所有测试文件与临时下载服务已回收，VM 正常恢复停止。Go 全量测试/vet、Computer race 两轮、macOS Rust tests/Clippy、Windows Clippy/Guest release 构建及 Linux 双 UID 图形隔离回归通过；本批没有重做 PostgreSQL 集成、Windows 账号生命周期、真实双用户、完整 MCP 或原生客户端验收，REVIEW-04/AI-01 保持进行中。

- 2026-09-05（全面 review 修复批次八）：修复 Windows 旧 Helper 只终止直接 worker、可能留下派生进程的问题，并为 V2 提供同一 Job Object 执行器。worker 在恢复执行前进入不可脱离的私有 Job；正常退出、超时与 Helper 异常退出均清理关联进程，标准流采用有界可取消 I/O，句柄仅继承明确的三个标准流，stderr 不进入日志。Windows 11 VM 9113 第一轮 15 项通过；增强版最终 17 项实机测试通过，原生测试 5.49 秒、含冷启动/下载/清理共 84.03 秒，覆盖 60 KiB 输入与 Unicode 参数、阻塞输入、输出超限后恢复、直接/派生进程回收、禁止 breakaway、未列出句柄隔离及独立对照进程不受影响。8 个 ignored 项是由正式测试显式执行的子进程 fixture。最终测试二进制 SHA-256 `0fce63de4805295018e7c4b0af6b2a05c70f3e9190d08b05df07aabe3cdcbd0d`，未替换已安装 Guest，也未创建 OS 用户或操作交互桌面。期间 QGA 分片路径出现一次全文件摘要不符及后续 PID 回报丢失/上传超时，均未执行不完整文件；增加逐块完整性检查和仅针对幂等测试块的有界重试，没有放宽真实动作的重放边界。最终使用一次性私有 IP 下载源，禁重定向/代理、限制长度与读取期限并核对全文件摘要。失败留下的 45 KiB 分片按精确路径、ACL 和本地构建块摘要核对后清理，测试机正常恢复停止，临时下载服务已关闭。Go 全量测试/vet、Computer race 两轮、macOS Rust tests/Clippy、Windows Clippy/Guest release 构建与 Linux 双 UID 图形隔离回归通过；本轮没有重做真实 PostgreSQL、Windows 双用户/GUI 或完整 MCP 验收。QGA 间歇故障根因尚未确认，默认 Windows MCP 仍关闭，REVIEW-04/AI-01 保持进行中。
- 2026-09-05（全面 review 修复批次七）：新增 `crates/windows-session`，把 Win32 unsafe 限定在 SID/WTS Token 和 Named Pipe 基础库；Guest 主 crate 保持 `forbid(unsafe_code)`。Go/Rust 目标校验区分 Linux UID 与完整 Windows SID/Session/LUID，拒绝 Session 0、内置/系统身份、混合身份和非规范别名；Linux Executor 不接受 Windows 目标。Windows 只读 inspect 按本地账号的完整 SID 查 WTS，不选择前台用户。管道双向核对真实 Token、显式私有 ACL、限制客户端写权限、拒绝抢占实例及远端连接，客户端只授予身份识别权限；overlapped I/O 到期取消并等候完成再释放缓冲区。Windows 11 VM 9113 上两轮最终测试通过；加强后的最后一轮共 223.63 秒，9 项原生测试覆盖 SYSTEM 回环、错误登录 LUID、抢占、连接/读写期限和 WTS 无匹配拒绝。测试二进制 SHA-256 为 `ff6cf439d6dfedc21c9e2fcb2704058ebce748abacb416db70c2b570963174bf`；先校验 amd64 PE 和摘要，在 SYSTEM/Administrators 私有新目录运行，未替换已安装 Guest、未创建用户。首次上传遇 QGA 短时不可用后已改为固定偏移有界重试；每轮精确清理临时文件并将原本停止的 VM 正常关机。macOS Rust tests、macOS/Windows Clippy、Windows Guest release 交叉构建、Linux 双 UID 图形隔离门禁、Go 全量 PostgreSQL race/vet 和格式检查通过，临时数据库测试 schema 已清空。CI 加入 Windows 原生测试但尚未远端执行。此证据仅为 SYSTEM 底层回环，不包含两个真实用户 SID、Windows 交互桌面、无人值守登录或 Native 接管；Windows 默认 MCP 继续明确不可用，REVIEW-04/AI-01 保持进行中。
- 2026-09-05（全面 review 修复批次六）：修复 `desktop_screenshot` 被返回成 Base64 JSON 文本的问题，改为标准 MCP JPEG 图片块和无重复图片字节的结构化/文本元数据；Guest 返回主屏原始坐标边界，截图缩小后仍能映射鼠标坐标。新增有界 Base64/JPEG 尺寸/SHA-256/坐标验证，旧 Guest 缺少元数据明确报错；修复 SDK 覆盖 `no-store` 的响应头问题，在写出时强制禁止存储并保留 SSE Flush。真实 SDK HTTP 单测通过初始化/工具目录、图片与元数据、坏图脱敏拒绝、已连接客户端凭证撤销、取消向控制面传播及缓存头验证；取消证据覆盖当前 SDK 协议，不扩大为所有旧 MCP 客户端。Debian 13 VM 158 的完整 SDK HTTP→控制面→PVE→新 Guest 三阶段实测最终通过 128.90 秒：无人值守首图 640×360、原始桌面 1600×900，含 Bootstrap 约 11 秒；缩放坐标点击、AT-SPI、真实终端输入、MCP 服务实例重建、令牌轮换、释放/重新领取及 UID/Home 保留通过；第二 Agent 无授权不能访问，分配后使用不同 UID 且无法读取前者 Home，停用/撤分配由真实每分钟维护循环清除进程。审计核对五类操作、10 条成功/3 条拒绝及敏感内容脱敏通过。实机脚本已替换旧共享会话假设，并修正审计查询上限及断言；两轮调试和最终通过运行的临时账户/Home/schema 均清理，xrdp 配置哈希不变。新 amd64 Guest 已安装，上一版本保留 `.previous-<hash>` 备份；Go 全量 PostgreSQL race/vet、MCP race 两轮、macOS/Linux Rust tests/Clippy、图形容器的原图/缩图元数据和既有隔离矩阵、OpenAPI YAML 通过。这不是 macOS 可视接管、Windows V2、新模板或弱网性能验收，REVIEW-04/AI-01 仍在进行。
- 2026-09-05（全面 review 修复批次五）：Linux 默认 HTTP/MCP 动作改走独立 Agent OS 账号与 V2；024 迁移登记账号、Lease/generation/实际交互实例，并把创建中断、撤分配、停用、释放、到期的账号清理加入可恢复撤销链。账号启用前检查 root 归属记录并先应用权限，xrdp-sesrun 的随机密码只走 stdin、成功登录后立即轮换；普通动作不重复配置 xrdp。真实 Debian 13 VM 158 上安装 amd64 新 Agent，旧系统服务保留；修复了 QGA 默认新文件权限过宽导致 authority 拒绝的问题，改为固定 Guest 子命令预建 0600 新 inode。两个临时 UID 的控制面 API→PVE→无人值守 Xorg/XFCE 验收通过 1280×720 截图、原生 AT-SPI 和真实终端输入；释放、撤分配、到期后均锁定账号、清除该 UID 进程并拒绝旧动作；重新领取保留 UID/Home、使用新 Helper 实例。完整三段复验通过，xrdp 配置哈希不变，未重启服务；临时账号/Home 与独立测试 schema 已清理。新旧 v1 控制目录使用无链接目录描述符封锁，不再留下共享 `vdi` 授权；升级前新版测试二进制以 `.previous-<hash>` 保留。Linux 图形容器另验证账号归属/幂等/软链接、旧打开文件描述符不能改写新的 QGA 暂存、双代旧目录撤权；Store/HTTP/Computer/Agent API 的真实 PostgreSQL race、Go 全量/vet、macOS/Linux Rust tests/Clippy、镜像语法检查通过。Computer Use API 总期限 65 秒且响应上限 16 MiB，普通 API 保留 1 MiB；策略更新与动作共用桌面锁。此证据不是完整 MCP HTTP、Windows、Native 接管或新模板实建验收，REVIEW-04/AI-01 继续进行中。
- 2026-09-05（全面 review 修复批次四）：新增 Rust Linux V2 会话传输与 Go `SessionExecutor`，不再让 root 从用户可写响应文件读取结果；请求绑定受管用户名、UID、OS Session/Display 和 Helper 实例，root/Helper 互验 Unix Peer UID。新增一次性图形容器门禁，使用 `10.31.0.2` 安装依赖，在本机原生 arm64 Linux 上验证两个不同 UID 的 1280×720 截图、真实 `linux_atspi` 控件树、鼠标/文本输入、同请求不重复输入、旧用户/epoch/实例拒绝、Helper 重启后重新授权、卡死 AT-SPI 的 250 ms worker 期限与恢复、第二 Helper 拒绝、权限/软链接拒绝及遗留请求清理。Go 全量测试/vet、Computer race、macOS/Linux Rust tests 与 Clippy 通过；CI 已增加相同 Linux 图形测试，尚未远端执行。容器测试结束后自动回收，无宿主挂载，也未改动 PVE VM。V2 尚未接入默认 HTTP/MCP，独立 Agent OS 账号、xrdp Bootstrap、Windows 和显式人工接管继续由 REVIEW-04/AI-01 跟踪，未宣称整个缺陷已修复。
- 2026-09-05（全面 review 修复批次三）：MCP 控制权前置修复已实现。Native 建联、Agent 动作和租约领取/释放统一使用 PostgreSQL 单桌面锁，锁连接池与业务查询分离；租约创建重新校验分配并拒绝活动人工连接。新增 023 迁移，以触发器在租约失效事务中记录 Guest authority 吊销，失败重试、revision 确认及完成审计原子化；移除跨副本不可靠的 authority 内存缓存。真实 PostgreSQL 上的双 Store/双 HTTP 实例回归验证了释放等待在途动作、撤分配后丢弃截图、失败后重试、旧确认拒绝、重复清理不执行、锁取消后可再取得和锁池满时业务查询仍可用；Store/HTTP/Computer 的 race 两轮、Go 全量测试/vet 与 OpenAPI YAML 检查通过，临时测试数据库已删除。该批次未修改实际 PVE VM，也没有完成每用户 Helper，REVIEW-04/AI-01 仍未闭环。

- 2026-09-05（全面 review 修复批次二）：新增 022 Guest 独立撤销队列，连接关闭后仍保留受控的 OS 会话期限。真实 PostgreSQL 的 Store/HTTP race 两轮回归覆盖六类保留会话撤销、旧库升级回填、在途 Native 签发、改密前旧验证结果、队列 revision/失败重试、到期清理后重连与先换密再启用；Go 全量测试与 vet 通过。Debian 13 VM 158 实测普通 DELETE 后临时进程仍在，撤分配和 Native 注销后仅目标进程结束、账号锁定并过期、xrdp PAM 拒绝，对照账号继续正常，重新启用通过；临时账号均清理，未重启 xrdp、未修改已有用户。测试期间修正了 root `runuser` 不适合作为登录断言、PAM 归一化返回码及测试脚本引用/失败清理；一次 PVE 只读预检超时后重新执行通过。此证据不是可视 RDP、Windows WTS 或网络分区撤权验收，REVIEW-02 继续保持未完成。

- 2026-09-05（全面 review 修复批次一）：真实 PostgreSQL 16 的 Store/HTTP 集成测试启用独立 schema，`go test -race ./internal/store ./internal/httpapi -count=2` 连续两轮通过；覆盖跨用户幂等拒绝、主体/路径/内容隔离、并发指纹冲突、旧连接快照、八类授权变化、并发迁移和策略升级。`go test ./...`、`go vet ./...` 通过；本地控制面 Docker 镜像已实际构建。Web TypeScript、30 个 Vitest 与 production build 通过，新增可重复执行的 Chromium 恢复测试：在 production preview 上模拟 API 故障，完成四组语言/主题、320/1280px、320–1280 全宽度溢出扫描、键盘过期重登、502 后 Job 恢复与只提交一次。该浏览器测试使用拦截的 API fixture，不等同真实 PVE/macOS/RDP 验收；新增 Guest 注销命令尚未实机执行，Windows/macOS/集群/远端 CI 不能据此标为完成。

- 2026-09-05：M11.1 每桌面会话策略纵向切片完成实现与 Debian 13 实机收敛。第 18 个数据库迁移和 PostgreSQL 16 生命周期测试覆盖默认值、revision 递增/幂等及 applied；Web 使用一个简洁对话框原子维护本地权限、文本剪贴板、驱动器重定向和受管背景，中文/英文、明暗主题、1280px/320px、键盘焦点与 44px 触控目标通过。控制面向原生描述符加入版本、applied revision 和确定性 SHA-256，macOS ABI v3 按快照开关 FreeRDP 通道。真实 VM 158 完成 allow/deny xrdp 可恢复测试，并从 Web 经 API/QGA 下发最终 `clipboard=true`、`drive=false`、`background=managed`；数据库为 revision 3/3 applied，Guest 配置、背景资源/helper 与 xrdp 服务只读复核通过。实测发现并修复 `sesman.ini` 缺少 `EnableFuseMount` 时无法收敛的问题。`make check`、`make images-check`、`make k8s-check`、OpenAPI YAML、PostgreSQL 集成测试和 macOS 自包含深度签名验证均通过。多作用域、密码学签名、Guest 摘要回报、Windows 实机、macOS 禁用行为和水印仍未完成，因此 POLICY-01 保持进行中。
- 2026-09-05：新增本地账号自助改密与管理员重置。自助改密要求当前密码并保留当前 Web 会话，同时撤销其他 Web/Native 会话、Native 授权码与活动桌面连接；管理员重置不保留被重置账号的会话；OIDC 账号明确交由 IdP 管理。真实浏览器完成自助改密→注销→新密码登录、创建测试用户→管理员重置→测试用户新密码登录，以及一次性 IaC 凭证创建/撤销；1280px 中文浅色与 320px 英文深色无根级横向溢出，Esc 焦点返回和 44px 窄屏账号目标通过。新增统一暖黑品牌桌面背景 SVG/4K PNG，主要图形在 5:4、16:9、21:9 填充裁剪预览中均完整可见；Debian 13 XFCE 与 Windows 10/11 Packer 配方均在登录时应用，静态/语法验证通过，实际新模板构建仍归 IMAGE-01/POLICY-01 验收。IaC 首批实现最长 90 天、只显示一次且只存摘要的 Web 管理员 API 凭证，以及 Terraform/OpenTofu 的桌面授权资源和受管桌面数据源；真实 PostgreSQL HTTP 验证了 Bearer 鉴权、无 CSRF 的授权写入、禁止 Token 继续签发凭证和 Web-only 路由隔离。Terraform 1.15.6 除本地 Provider `init`/`validate` 外，还对真实控制面完成授权 `apply`、数据库落地核对、人为删除后的漂移检测与重建，以及 `destroy` 回收。`make check`、镜像 syntax-only、OpenAPI YAML 均通过；Provider 尚未正式发布，资源覆盖与 OpenTofu CLI 验收仍由 IAC-01 跟踪。
- 2026-09-05：从 Debian 12 模板 901 完整克隆 VMID 9301 `vc-workspace-ad-client-20260905`，与复用为 Samba 4.17 AD DC 的 VMID 9300 组成两个独立 PVE Guest 的域测试。客户端从 `10.31.0.2` 安装 realmd/adcli/SSSD/Kerberos/PAM，VC Workspace 生成的 `linux_sssd_ad` 收敛计划通过 QGA 完成首次入域和幂等重应用；NSS、Alice PAM 与自动 Home、Bob 有效 Kerberos 凭据但被允许组拒绝、域控移除 Alice 后的 PAM 拒绝及恢复后重新允许全部通过。实测修复 PVE `agent/exec input-data` 被客户端提前 Base64，以及重启后 DHCP DNS 覆盖域 DNS并让 SSSD 离线缓存继续放行的两个问题；验收目标固定 `-count=1`，并在客户端重启后再次确认域 DNS、SSSD Online 和完整撤权。域控 DNS 只保留 `10.31.0.159`，客户端当次为 `10.31.0.160`；两台带 `temporary` 标签的 VM 最终正常关机保留。该证据不扩大为生产 Microsoft AD、Debian 13 正式模板、FreeIPA、Windows 或 macOS 目录登录已通过。
- 2026-09-05：从 PVE Debian 12 Cloud-Init 模板 901 完整克隆 VMID 9300 `vc-workspace-identity-lab-20260905`，配置 2 vCPU、4 GiB、Ceph 30 GiB、QGA、公钥与 DHCP；当次地址 `10.31.0.159`。Docker Engine/Compose 从 `10.31.0.2` 的 Bookworm 仓库安装；PostgreSQL、Keycloak、Debian 13 基础镜像按 `linux/amd64` 在 Mac 拉取并离线导入，自建 OpenLDAP/SSSD 镜像在 VM 内从 `10.31.0.2` 的 Trixie/安全仓库构建。完整构建运行与缓存复验各通过一次，两轮均完成 OIDC、组撤权、Native Code/重放/注销及 LDAP StartTLS/NSS/PAM/Home/Access Filter 验收；容器、卷、随机凭据和端口随后清理，VM 正常关机后以停止状态保留。该证据是 PVE VM 托管实验室，不等同 Guest OS 直接入域或生产 AD/FreeIPA。
- 2026-09-05：新增一次性 Docker 身份实验室并完成真实对接。官方 Keycloak 26.7.3 通过 Discovery、Authorization Code + PKCE/state/nonce、ID Token/JWKS、首次与重复 Web 登录、Session、OIDC 两组同步和 IdP 侧撤组后的成员收敛；Native 自定义 Scheme、一次性 Code 兑换、重放拒绝与注销的服务端流程也通过，本地管理员继续存在。Debian 13 OpenLDAP/SSSD 通过临时 CA + StartTLS、`sssctl config-check`、NSS 用户/组解析、PAM 密码认证、Access Filter 拒绝未授权用户和自动 Home。实测修复了 OIDC 用户 `NULL password_hash` 重登 500、Guest 收敛 shell 换行未执行、SSSD 2.10 已移除选项及 LDAP 系统 CA 信任四类问题。测试完成后容器、数据卷和临时凭据均清理；AD/FreeIPA/Windows、生产 IdP、真实 macOS `.app` Open URL 与目录交互仍未据此宣称通过。
- 2026-09-05：统一身份 P0→P2 代码阶段完成。PostgreSQL 17 从空库完整应用 16 个迁移，并通过 Personal Owner 冲突、Shared 组授权、禁用用户触发连接撤销、旧授权无损升级和桌面 OS 类型保持的事务测试；Go 覆盖 Guest 用户稳定映射、一次性 Secret 不进入 QGA argv、Profile 注入校验与 OIDC Group ID，Web 身份管理页通过 TypeScript/Vitest/production build，并在中英文、明暗主题、1280px/320px 下完成渲染与实际表单交互。Linux 桌面只列出 Linux Profile，平台不匹配同时由 API 拒绝。该阶段最初只有配置生成证据；随后同日补充的 Keycloak 与 OpenLDAP/SSSD Docker 实测记录以上一条为准。
- 2026-09-05：使用部署方提供的真实 PVE 集群执行非破坏性复核：Debian 13、Windows 10、Windows 11 三个停止状态模板的 `ostype`、Agent 和硬件基线通过；6 个节点均继续暴露 Intel HD 530 `8086:1912`，`i915-GVTg_V5_4` 可用 1 个、`i915-GVTg_V5_8` 可用 2 个。此次复核未启动、创建或修改任何 PVE 资源。
- 2026-09-05：在明确的 Debian 13 验收机 VM 158 运行每用户 Guest 身份 opt-in 测试：真实 QGA 创建稳定 `vcw…` 用户，连续两次 Connection Session 使用不同密码且旧连接先被撤销，首次与重复 DELETE 都返回 204；测试账号随后删除、一次性数据库销毁，VM 保持原运行状态。该记录覆盖 Broker→PVE→Guest 生命周期；本轮 macOS 可见窗口因主机重新锁屏未复测，客户端 API 释放由 19 个 Swift 测试覆盖，既有 Debian/Windows 可视 RDP 实机记录仍有效。

- 2026-09-04：macOS 从冷启动、恢复登录、登录提交、桌面加载、准备、连接、取消、主动断开、断线恢复、重连到全屏调整统一为大尺寸可辨识状态页，并遵守“减少动态效果”。本地 AWS WorkSpaces 实际走查了冷启动和登录外壳，其 Pre-session / Reconnect 编译资源用于补充无法登录部分的结构对照；没有把未持有账号的 AWS 连接环节记为实测。VC Workspace 则在真实 Debian 13 VM 158 完成整条链路复测，修复 RDP 握手后首帧白屏和全屏重建画布白屏；全屏 Guest 为 5120×2804、XFCE DPI 192，18 个 Swift 测试、自包含构建与签名验证通过。
- 2026-09-04：macOS 菜单与窗口生命周期完成真实应用走查。客户端收敛为单窗口并移除默认“新建窗口”与标签页命令；修复了 Help Book 缺失警告，新增服务器设置、项目主页和 Issue 入口。真实 VM 158 上通过菜单完成桌面刷新、断开、断线后重连及全屏往返，同时覆盖编辑菜单、窗口缩放、最小化后再次激活、关于窗口、设置连接检查和显式退出。
- 2026-09-04：macOS 会话外壳按 AWS WorkSpaces 的 Pre-session / Transition / Reconnect 分层完成改造，并在真实 Debian 13 VM 158 上覆盖准备状态、取消、连接、主动断开、原位重连、返回桌面库及全屏往返。普通界面不再展示轮询秒数、弱网档位、Guest Agent 或 xrdp 诊断；停止回调另有 8 秒有界回收。
- 2026-09-04：品牌与技术标识迁移完成。新的 Go/Rust/TypeScript/Swift 包、Guest Agent、环境变量、PVE 标签、模板名、来宾目录、Cookie、Bundle ID、可执行文件和 `vc-workspace://` App Link 均使用 VC Workspace；旧环境变量、PVE 标签、Keychain、App Link 和来宾 Agent 路径只在兼容层读取。现有 PVE VM/Mapping 不冒险就地改名。
- 2026-09-04：macOS Retina 动态显示在真实 Debian 13 VM 158 上通过。先把 XFCE 重置为 96 DPI，再由新版 Native Connection 自动恢复为 192 DPI；签名、自包含客户端协商默认 1640×972、系统“缩放”最大化 5120×2670、当前构建的原生全屏 5120×2804，退出全屏回到 5120×2670。最大化与全屏后的远程终端输入均成功；客户端使用 AppKit 逻辑点 × backing scale（上限 2×）、180 ms 防抖、远端确认和最多 5 次有界重试，保留 Smart Sizing 作为过渡与兼容回退。
- 2026-09-04：最终仓库检查通过：`make check`、`make images-check`、`make k8s-check`、Go Race Detector、Rust Clippy `-D warnings`、PostgreSQL 17 授权生命周期集成测试、OpenAPI YAML 解析、macOS 自包含依赖及深度签名验证均为绿色。
- 2026-09-04：AI-01 在真实 PVE 完成。Debian 13 VM 158 返回 1280×800 JPEG 与 30 个 `linux_atspi` 节点，直接 Guest 任务和完整 MCP 任务均通过，完整链路产生 6 个成功动作审计事件；Windows 11 VM 9113 返回 1280×800 JPEG 与 `windows_uia` 控件树，直接任务和完整 MCP 任务均通过，完整链路产生 8 个成功动作审计事件。两端均验证 Native Connection 人工接管、Guest authority 吊销、epoch 递增、旧 Lease 拒绝且审计不保存文本正文。
- 2026-09-04：签名、自包含的 macOS 客户端在 Debian 13 与 Windows 11 的真实桌面完成连接、全屏往返、macOS 原生窗口缩放、Smart Sizing、主动断开和 Dock 等价再次激活回归。Windows helper 以隐藏、单实例的交互登录任务运行；Windows VM 9113 验收后通过 Guest 正常关机恢复为停止状态，Debian VM 158 保持验收前运行状态。
- 2026-09-04：MCP `tools/call` 身份/epoch 转发、Guest 请求边界、QGA 文件通道、人工接管/释放/移除分配后的 epoch 失效，以及 Agent 审计身份解析的自动测试通过；人工接管在无活动 Lease 的重试中仍强制写 Guest revoked tombstone，旧 Lease 动作保留拒绝审计；管理端仅列出未过期的活动控制租约，并在接管后清除。
- 2026-09-04：Guest Agent 在 macOS、Linux amd64 和 Windows amd64 完成编译；最终 Linux amd64 与 Windows amd64 产物已分别装入真实 Guest 验收，原生 AT-SPI/UI Automation 与动作超时隔离均通过。
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
