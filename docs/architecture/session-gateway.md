# Session Gateway

Gateway 将短期连接授权与 Guest OS 密码生命周期分开，并为无法直达 Guest 的客户端提供受控数据通路。当前已有数据库授权、TCP relay、独立 HTTPS/WebSocket 服务、专用 mTLS 控制接口、Native Broker 的显式路由、Linux 可信证书发现，以及 macOS 进程内隧道和 Guest 指纹校验。路由默认关闭，只有 infra 环境显式启用；其 Debian 桌面、保留会话重连及在线撤权已有真实客户端证据，但稳定性、Guest 防绕过和 WAN 验收尚未闭环。完整交付状态只在 [NET-01 / REVIEW-12](../plan/status.md) 维护。

## 已实现的信任边界

`internal/store/gateway_ticket.go` 是控制面的授权实现。029 迁移给 Connection Session 保留不可变的发起 Native Session 摘要；同一用户的其他设备不能代为领取，旧连接的未知来源不回填、不放行。版本化 Native 签发在同一事务内保存这个来源，再清除待处理凭据意图。

票据是 256 位随机一次性值，数据库只保存 SHA-256 摘要。最长有效期 30 秒，受原 Connection / Native Session 期限进一步约束。一个逻辑连接只能签发一张票据；兑换先提交消耗记录，再允许拨号，提交结果不确定时不能重放或重新开放票据。票据绑定具体 Gateway、Guest 的 RFC1918 IPv4 字面地址、固定 TCP/3389、RDP 证书 SHA-256，以及给客户端的已应用策略 revision。目标地址、证书和策略来自可信 Broker，不接受客户端请求指定。Gateway 自身再以显式 CIDR 白名单收窄出口，不解析 DNS、不接收任意端口。

签发、兑换及每次续租重新检查原 Native Session、用户与桌面授权、Guest 绑定、启用且匹配的平台 Profile、待撤权/凭据意图和已应用策略。策略 revision 改变后，即使新策略已经收敛，也不能给旧客户端续租。生成、成功兑换和关闭各产生一次事务内审计，不记录票据、密码或数据内容。

版本化 Linux Native 的 OS 凭据回执、Connection Session 和一次性票据在同一数据库事务中提交；票据或审计写入失败会连同凭据回执一起回滚，保留可恢复的 pending 意图。丢失提交回执不能重放密码或票据。客户端未收到描述符、未兑换票据或隧道意外关闭时，后台维护会将到期/关闭的 Gateway 连接纳入既有凭据退役流程，不要求客户端成功发送 DELETE；普通退役保留 OS 桌面，实际退出/撤权仍走注销路径。维护目前按分钟轮询，这不是立即清除 OS 密码的 SLA。到期检查与 Gateway 兑换/续租共用短授权事务锁，避免旧快照错误退役刚续租的连接。

Linux 证书发现使用固定 `/usr/bin/python3 -I` 命令通过 PVE/QGA 执行，只连接 Guest 自己的 `127.0.0.1:3389`，协商 TLS/CredSSP 后取得当前监听器的 DER 叶证书并立即断开，不提交任何 OS 凭据。它不采纳客户端端点、DNS、跳转或磁盘证书作为信任来源；本地观察通过既有可信 PVE 通道返回。Broker 校验 DER 大小、有效期及服务器用途后计算 SHA-256，票据与描述符使用同一指纹。探测最长 5 秒，进程另有 7 秒闹钟，PVE 调用最长 10 秒；失败不能回退 TOFU。自签名证书的信任根是受管 Guest 的特权本地观察，不是网络链验证；PVE TLS 和 Guest root 管理权限必须可信。协议结构见 [Microsoft RDP_NEG_RSP](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpbcgr/b2975bdc-6d56-49ee-9c57-f2ff3a0b6817)，二进制证书读取见 [Python SSL](https://docs.python.org/3/library/ssl.html#ssl.SSLSocket.getpeercert)。

`internal/gateway` 的 relay 内核持有可信 Gateway ID、Authority 接口、目标白名单和并发上限，上层 TLS 服务负责交付连接与票据。数据面为透明双向字节转发；任何一端结束会关闭另一端，不保留半开连接。一次租约至多 5 秒；以控制 RPC 发起时的本地单调时钟计算期限，扣除调用、提交和拨号耗时，不用远端绝对时间延长授权。两端 socket deadline 与独立计时器共同执行有效期；续租仅允许延长相同连接、目标、证书和策略。授权失败、控制面不可达、迟到响应、取消、失效租约均停止转发。

关闭网络在等待数据库收尾之前完成。关闭审计失败会返回明确错误，不能谎报已持久化；数据库中的短租约仍会过期，不能恢复。控制 RPC 实现必须遵守 context deadline。这个内核的本地测试期限不是生产端到端撤权 SLA，也不表示 Guest OS 会话已注销。

## TLS 与传输协议

HTTP 契约独立维护在 [`api/gateway.openapi.yaml`](../../api/gateway.openapi.yaml)，不挂入现有 `/api/v1`。客户端使用 TLS 1.3、HTTP/1.1 `GET /gateway/v1/rdp` 和 `vc-workspace-rdp.v1` 子协议。Host 必须匹配公开域名，任何查询字符串、Origin、Cookie、Authorization 或 HTTP 请求体都会被拒绝；此阶段只支持原生客户端，不支持浏览器桌面。

升级后的首个消息必须在 3 秒内提交 47 字节文本票据；票据不进入 URL、HTTP Header 或代理访问日志。101 只表示升级成功，服务端在票据已提交兑换且 Guest TCP 拨号成功后才发送文本 `ready`。其后仅接受至多 64 KiB 的二进制消息，承载连续 RDP 字节；不再接受控制文本或启用 WebSocket 压缩。

数据流使用固定 `github.com/coder/websocket v1.8.15`，不是自写帧解析器。[官方 NetConn](https://pkg.go.dev/github.com/coder/websocket#NetConn) 默认取消消息上限且按正常关闭握手处理 Close；适配器显式恢复消息上限，并先 `CloseNow` 再释放 NetConn 的上下文/计时器，确保不响应关闭帧的客户端不能拖延撤权。新模块的 ISC 许可证随 Gateway 镜像保存于 `/usr/share/licenses/session-gateway/`；完整发行版 SBOM/许可证验收仍按状态页跟踪。

Gateway 调用独立控制监听器的 `ready/redeem/renew/close`，使用专用 CA 验证的 mTLS。每张客户端叶证书的 SHA-256 必须登记到唯一 Gateway ID；CA 签名有效但未登记的证书也被拒绝。每个 ID 可登记两个证书以安排轮换，不允许同指纹映射到多个 ID。每次 HTTP 请求还检查当前 CA、指纹和证书到期，不能借助已建立的 Keep-Alive 连接延长失效身份。转发证书头、自报 Gateway ID、普通用户 Token 和 MCP 内部 Token 均不能代替此认证。

控制调用不接受任意主机/端口，不提供签票能力；请求限定 1 KiB、拒绝重复/额外字段，响应限定 4 KiB。Gateway 使用固定 HTTPS 控制来源，验证证书链与主机名、不跟随重定向，不将私有错误或请求正文写入日志。公开和控制监听器在 TLS 握手前限制已接受 socket 数量，HTTP Header 限制 8 KiB、读 Header 2 秒。`/health` 只判断进程，`/ready` 通过实际 mTLS 及 PostgreSQL Ping 验证依赖。退出时显式取消并等待 WebSocket，因为普通 HTTP Shutdown 不负责已升级连接。

### 证书与信任重载

文件路径、地址、Gateway ID 和目标白名单仍在启动时固定。当前源码的环境加载器为两个 TLS 监听器安装 `GetConfigForClient`：每次新握手从原路径读取新的证书/密钥和 CA，构建独立 TLS 1.3 配置并关闭会话恢复。不会修改正在使用的 `tls.Config`，也不设置 `InsecureSkipVerify`。实现依据 [Go TLS Config](https://pkg.go.dev/crypto/tls#Config)。

控制面每次 HTTP 请求按当前 CA 重新验证客户端链与用途，再检查当前叶指纹；已移除的 CA 或指纹不能借 Keep-Alive 继续授权。Gateway 在控制请求前加载客户端证书与控制 CA，材料变化时更换 HTTP 连接池并关闭旧空闲连接；请求不重放，正在进行的调用仍受原两秒期限约束。控制响应还重新检查实际服务端证书链、主机名和期限，拒绝已过期长连接上的授权结果。

文件损坏、密钥不匹配、用途错误、到期或缺失明确拒绝当前操作，不回退旧的信任配置；正确文件恢复后无需重启即可恢复。CA bundle 不接受普通叶证书、尾部垃圾或只解析成功前缀。两个证书文件跨投影版本短暂不匹配时也会拒绝，因此应以完整 Secret/目录代际发布，不能逐个原地覆盖生产 PEM。已接受的授权仍受既有短租约控制，不把配置更新描述成已在途操作的即时撤回。

热重载不负责替部署方登记新身份。无中断轮换必须先分发新旧 CA/双指纹并验证接收方已生效，再启用新证书，最后撤销旧材料；不能在新指纹尚未登记时直接让 cert-manager 替换正在使用的客户端身份。部署版本、投影延迟及目标集群验收见 [Kubernetes 维护](../operations/kubernetes.md#证书轮换)。

## 启动配置

所有证书、私钥、CA 与证书登记文件由部署方提供；测试生成的 CA 不能用于生产。服务只读取显式配置，不读取 PVE 凭证或数据库 URL。私钥应作为只读 Secret 挂载，控制监听器不要暴露到公网。

| 独立 Gateway 环境变量 | 用途 |
| --- | --- |
| `VC_WORKSPACE_GATEWAY_ID` | 已登记的 `gw_…` 身份 |
| `VC_WORKSPACE_GATEWAY_ADDR` | IP:端口，默认 `127.0.0.1:8443`；容器需显式配置监听地址 |
| `VC_WORKSPACE_GATEWAY_PUBLIC_URL` | 公开 HTTPS 来源，不带路径、查询或用户信息；Host 必须由代理原样保留 |
| `VC_WORKSPACE_GATEWAY_ALLOWED_TARGETS` | 逗号分隔的规范 RFC1918 IPv4 CIDR 白名单；没有全网出口默认值 |
| `VC_WORKSPACE_GATEWAY_MAX_CONNECTIONS` | 最多 1–4096 条连接，默认 128；另保留 32 个握手/HTTP socket 名额 |
| `VC_WORKSPACE_GATEWAY_TLS_CERT_FILE` / `VC_WORKSPACE_GATEWAY_TLS_KEY_FILE` | 对客户端提供 HTTPS 的服务证书和私钥 |
| `VC_WORKSPACE_GATEWAY_CONTROL_URL` / `VC_WORKSPACE_GATEWAY_CONTROL_CA_FILE` | 固定控制接口 HTTPS 来源及受信 CA 文件 |
| `VC_WORKSPACE_GATEWAY_CLIENT_CERT_FILE` / `VC_WORKSPACE_GATEWAY_CLIENT_KEY_FILE` | 调用控制接口的已登记客户端证书和私钥 |

控制面另外配置 `VC_WORKSPACE_GATEWAY_CONTROL_ADDR`、`VC_WORKSPACE_GATEWAY_CONTROL_TLS_CERT_FILE`、`VC_WORKSPACE_GATEWAY_CONTROL_TLS_KEY_FILE`、`VC_WORKSPACE_GATEWAY_CONTROL_CLIENT_CA_FILE`、`VC_WORKSPACE_GATEWAY_CONTROL_PEERS_FILE`；五项必须齐全且控制面已有数据库配置，否则拒绝启动。不设置时监听器关闭，不改变原 HTTP 端口。登记文件是 Gateway ID 到一个或两个小写十六进制 SHA-256 指纹的 JSON 数组映射；不含票据或私钥。控制监听器使用的数据库由现有控制面管理，升级仍需正常备份/迁移流程，不能为了试用网关直接升级原开发库。

编译入口为 `go build ./apps/session-gateway`，容器定义为 `deploy/container/session-gateway.Dockerfile`，以 UID/GID 65532 运行。按上述字段配置后启动；证书/身份配置缺失不会降级到明文。此入口不提供手工生成任意桌面票据的命令。

Native Broker 路由由控制面的三个附加变量一起启用：`VC_WORKSPACE_NATIVE_GATEWAY_ID`、`VC_WORKSPACE_NATIVE_GATEWAY_URL`（HTTPS 来源）、`VC_WORKSPACE_NATIVE_GATEWAY_ALLOWED_TARGETS`（私有 IPv4 CIDR）。必须已有上述控制监听器配置，ID 必须在证书登记表中；部分配置、非私有范围和未登记 ID 使启动失败。启用后整个 Native Connection 入口必须走此路由，不允许请求挑选 Gateway 或直连。当前只接受 managed-local Linux，Windows 在修改凭据之前被拒绝。

客户端在控制面的创建连接请求中声明 `X-VC-Workspace-Transport: vc-workspace-rdp.v1`；这只是能力声明，不是授权。缺失或未知声明返回 426，不触发 Guest 凭据操作。成功描述符的 `protocol` 为 `rdp-gateway`，`gateway` 含固定 `wss://…/gateway/v1/rdp` URL、子协议、一次性票据及其兑换期限、Guest `certificate_sha256`；`host/port` 仍标识真实 Guest，不能用作直连回退。OS 连接的 `expires_at` 与票据的短兑换期限不同。完整 API 契约见 [`api/openapi.yaml`](../../api/openapi.yaml)。

## 客户端与部署接线约束

RDP 的 TLS 保持客户端到 Guest 端到端。macOS `GatewayTunnel` 使用 Foundation WebSocket，在系统信任校验的 TLS 1.3 连接内兑换票据；不接受 HTTP 重定向，不使用 Cookie、共享凭据或缓存。URL、子协议、票据规范编码、期限、Guest 指纹及连接策略先本地校验，握手和文本 `ready` 全部通过才创建 FreeRDP。握手期限不超过票据剩余时间且最多 10 秒，成功后不再用票据期限截断有效流。

客户端只对 `issued_at` 的合理性检查容忍最多 5 秒服务器时钟领先，避免毫秒级偏差拒绝刚签发的连接；仍要求签发早于会话结束、票据不晚于会话结束，已过期票据/会话不增加宽限期。此容差不能延长上述握手或 Gateway 单调时钟租约。

Swift 与 FreeRDP 通过进程内、匿名 `AF_UNIX/SOCK_STREAM` socket pair 相连，不启动外部应用，不开放本机 TCP 监听器。Swift DispatchIO 每批最多读取 32 KiB，并等待 WebSocket 写入；反向每个二进制帧最多 64 KiB，并等待本地写入。双向背压、EOF、异常帧和取消均关闭完整通道，不保留半开连接。C Bridge ABI v5 复制并拥有独立 fd，只替换官方传输的 `TCPConnect`，不解析目标 DNS，也不在失败后拨号 Guest；Swift 在移交后释放自身副本。

断流诊断仅记录固定错误分类、数字错误/关闭码及 FreeRDP 编译期函数名/行号；不记录任意 peer 错误正文、NSError 用户信息、端点、凭据、票据或桌面数据。Gateway 的 `eof/authorization/lease_expired/transport/capacity/shutdown/internal` 是终止分类，不单独证明哪一端是根因。

网关模式使用外部证书回调：严格核对实际 TLS 叶证书 DER 的 SHA-256 和有效期，禁用内建历史指纹接受、TOFU、明文 RDP 降级和服务器跳转；认证失败不能弹出上游客户端的凭据对话框。证书/跳转错误显示固定安全文案且不自动重试。原部署未配置 Gateway 时仍保留既有直连 TOFU；不能把直连的安全边界当作 Gateway 已生效。新应用和 ABI v5 Bridge 必须成套升级，旧 ABI 不可混用。

如果把 Gateway 用作 Windows 稳定 OS 凭据的短期授权边界，必须同时确保客户端不能绕过 Gateway 直达 Guest 登录入口，并验收 Guest 防火墙、受管账号、凭据存储/重启、撤权和保留桌面。Gateway 本身不会修复 Windows DPAPI，不允许借此开放未验收的 Windows Native/MCP 路径，也不对已获 Guest Administrator/root 权限的恶意用户承诺隔离。

部署数据流为客户端到 Gateway 的 HTTPS/443（进程常用 8443）、Gateway 到控制面专用 mTLS 监听器（建议内网 8444）、Gateway 到受管 Guest 的 TCP/3389。`deploy/kubernetes/overlays/infra` 已将精确 WSS 路径挂入 `ws.infra.plz.ac`，Ingress 使用 TLS 1.3 重新加密、内部 CA/主机名验证并保留 Host；普通明文上游会被拒绝。mTLS 控制端保持实际双向认证，不用代理转发证书 Header 代替。该环境单独启用路由、限制测试 Guest `/32`，原开发环境不变；不公开 Guest 3389。实际配置、证书重载限制和验收边界见 [Kubernetes](../operations/kubernetes.md)。

## 验证方法

对独立、可销毁的 PostgreSQL 设置 `VC_WORKSPACE_TEST_DATABASE_URL`，运行 `make gateway-check`。入口在缺少数据库时直接失败；各测试仅迁移随机 schema 并自行删除，不迁移原开发库。CI 的 Store / HTTP / Gateway race 门禁也包含这些测试。

数据库测试覆盖两个 Store 并发兑换、同用户不同设备、过期票据、授权锁等待期间过期、租约不可复活、不可变字段，以及用户/桌面/Profile/策略/撤权变更。relay 测试使用真实本机 TCP socket，覆盖双向字节、续租、取消、EOF、容量、错误地址/证书/策略、控制调用卡住及关闭回执失败。独立测试 CA 驱动真实 TLS 1.3/mTLS/WSS，包含未登记身份、错误 CA、失效证书、伪造 Header、重放、消息边界、不回应关闭及服务退出；真实 PostgreSQL→mTLS→WSS→TCP 的注销与审计也有单独联合用例。目标 IP 只在测试内部映射到回环夹具，运行测试不会拨号 PVE Guest；这些证据不是 RDP、WAN 或 macOS `.app` 验收。

`go test -race ./internal/gateway -run 'TestTLSReload|TestGatewayRuntimeTLS'` 不需要数据库：以临时私有目录模拟 Kubernetes `..data` 符号链接投影，验证双指纹/CA 轮换、新监听证书、旧 Keep-Alive 拒绝、损坏与过期材料、128 次并发请求和实际运行时接线。同一 WSS→回环 TCP 流跨轮换继续续租及双向传输，票据只兑换一次。控制后端是模型，不将此测试算作 cert-manager/实际集群、PostgreSQL 或 OS 桌面轮换验收。

Broker 集成测试使用真实 PostgreSQL 与模拟 PVE/QGA 回执，覆盖默认 HTTP 入口、可信路由/证书绑定、注销、重放、旧客户端/Windows/越界地址/证书故障拒绝，以及票据与审计故障的全事务回滚、后台恢复和隧道关闭后的保留桌面语义。未兑换票据实际等待数据库 30 秒期限后验证回收，不关闭不可变触发器来缩短测试。嵌入式 Python 探测另对真实本机 TLS、CredSSP 协商、明文降级、损坏与停滞 peer 回归；不发送登录数据。

PVE 实机证书验收只使用隔离、已启动且无 Xorg 会话的 Debian 13 VM 160：设置 `VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID=160`、PVE endpoint/credential file 后运行 `make pve-live-gateway-certificate-check`。它执行产品同一 QGA 探测，独立对照配置证书，并前后核对账号/配置；不会启动/关停 VM、改密或登录桌面。该证据不是 Broker→Gateway→macOS 的完整远程桌面验收。

Mac 本机先运行 `make macos-app`，再运行 `make macos-gateway-check`（也由 `make macos-check` 调用）。后者启用完整 Swift 回归和明确 opt-in 的互操作套件，启动带 Go race detector 的正式 Transport/Relay 测试服务，并加载刚构建的真实原生 Bridge；没有当前 Bridge 时明确失败。测试 CA 仅通过 DEBUG、限定回环地址的信任锚入口生效，仍执行证书链/主机名/有效期验证，不写系统钥匙串；Release 不含这个入口。

互操作覆盖分片和 256 KiB 双向数据、空闲续租、票据重放、撤权断流、重定向、子协议/ready/数据帧错误、未受信 CA、取消和资源关闭；使用真实内层 TLS peer 对照正确/错误 Guest 指纹，错误指纹时不发送 RDP 应用数据。测试 peer 只协商至 TLS 后的首个 RDP 应用数据，不提供 OS 桌面；这不是 macOS `.app` 经 Broker 登录 PVE 桌面、图形/输入、WAN 性能或 Guest 防绕过验收。普通 `swift test` 未设置夹具路径时会跳过该套件，不能将跳过计为通过。

`make macos-gateway-soak-check` 是独立的长传输回归：正式 Swift 隧道与 Go Transport/Relay 在同一授权连接内往返 60 × 2 MiB 伪随机数据，按 SHA-256 校验完整性，加入慢消费者背压，并空闲 60 秒后继续传输；断言只有一次授权和一次关闭。用例约需三分钟，不连接 PVE、不运行 FreeRDP 图形解码，也不注入真实 WAN 丢包，不能代替全屏稳定性或弱网体验验收。

真实 Mac 桌面验收的环境专用夹具为 `tools/test-infra-gateway.mjs`，必须显式设置 `VC_WORKSPACE_LIVE_INFRA_MAC_LAB=true`，目标固定为隔离 VM160；它不是通用安装器或可盲目重复执行的 smoke test。夹具通过普通 API 创建/分配测试主体，桌面登录和重连由真实 `.app` 完成；Guest 操作使用受限 QGA，单次 stdin 不超过 64 KiB，未知执行结果只查询、不重发。私有操作日志与状态保存在 `.cache/infra-gateway-lab/`（0700/0600），不能提交或公开。清理核验账号、进程、摘要和撤权回执后恢复 Guest，平台禁用身份与审计保留。

下一轮使用 `prepare-next`，仅在上一轮已清理、身份已禁用、无分配或活动连接时归档私有状态。它保留历史固定 UID/SID 墓碑，按现存 UID 上界为隔离 Guest 临时设置 `UID_MIN`，再通过 `install-agent`、`resume-setup` 恢复夹具流程；真正的 OS 账号仍由正式 Broker 首次连接创建。配置清理包括 `/etc/login.defs`，先写准备摘要、后原子替换，恢复前校验所有权与摘要，不得删除身份栅栏来复用 UID。这只是反复实测的夹具，不是生产账号迁移方案。具体通过项与失败项只记录在状态页。
