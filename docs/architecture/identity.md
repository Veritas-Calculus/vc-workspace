# 身份与访问

## 双栈身份

本地用户与 OIDC 是并列入口。OIDC 配置成功不会禁用本地用户；系统禁止删除或降级最后一个可用管理员。

内部用户使用 UUID。OIDC 身份以 `issuer + subject` 绑定内部用户，邮箱和显示名不是稳定主键。平台登录凭证、OIDC Token 与 Guest OS 凭证相互隔离。

## 初始化

- 全新部署由部署系统注入高熵 Setup Token。
- Setup Token 只允许创建首个本地管理员，完成后立即失效。
- 不提供默认用户名或默认密码。
- 初始化完成后，即使 OIDC 不可用，本地管理员仍可登录。

## Web 会话

- 本地密码使用 Argon2id。
- Web 使用随机、Opaque、服务端存储的 Session；Cookie 为 `HttpOnly`、`SameSite=Lax`，生产环境必须 `Secure`。
- 状态变更请求校验 CSRF。登录与初始化限速是生产加固项。

## 凭证生命周期

- 本地账号可以从 Web 账号入口修改自己的平台密码；必须提交当前密码，新密码为 12–128 个字符且不能与当前密码相同。
- 自助改密保留当前 Web 会话，撤销该用户的其他 Web Session、全部 Native Session、未兑换 Native 授权码和活动桌面连接。管理员可以重置其他本地用户的密码，此时不保留被重置用户的任何会话。
- OIDC 用户的密码由身份提供方管理，VC Workspace 不显示不可执行的改密表单，也不在本地创建第二套密码。AD、FreeIPA、LDAP 与 Entra 的 Guest 登录密码同样由目录拥有；平台改密不会改 Guest 目录密码。
- 平台管理员可以为 Terraform/OpenTofu 创建最长 90 天的 `vcwi_` API 凭证。明文只显示一次，数据库只保存摘要；凭证可随时撤销，不能用一个 API 凭证继续签发或撤销其他凭证。

## OIDC

- Web：Authorization Code + PKCE，BFF 持有 Token。
- Native：复用控制面的 Code + PKCE 回调，控制面签发两分钟有效的一次性授权码，再由客户端换取独立 Bearer Session；Session 保存在 Keychain。
- Web Cookie Session 与 Native Bearer Session 相互独立并可撤销。生产部署可进一步拆分 OIDC Client Registration。
- `VC_WORKSPACE_OIDC_GROUPS_CLAIM` 指定 ID Token 中的字符串数组 Claim，默认是 `groups`。每次成功登录都以 `provider + external group ID` 做稳定映射并事务性同步成员关系；单次最多接受 256 个组、每个值最多 512 个字符。
- Discovery、JWKS、Redirect URI 与 Claims 诊断必须脱敏。

当前实现已通过一次性 Keycloak 的真实 Authorization Code 流程，覆盖首次绑定、重复登录、Web Session、Group Claim 增删与事务性成员收敛，以及 Native 自定义 Scheme 回调、一次性 Code 兑换/重放拒绝和注销的服务端契约；测试方法见[身份集成实验室](../operations/identity-lab.md)。这证明协议实现可互操作，但不等同于生产 IdP、真实 macOS `.app` Open URL、过期/拒绝、JWKS 轮换和组织 Claim 策略已全部验收；剩余项由 [OIDC-01](../plan/status.md#todo) 统一跟踪。

## 三层身份边界

VC Workspace 明确分开三层身份，避免把 OIDC 登录误当成 Guest OS 已经登录：

1. 平台身份：本地账号或 OIDC `issuer + subject`，用于 Web、Native API 和 MCP 鉴权。
2. 桌面授权：决定该平台用户或 Agent 能否看到、启停或连接某个 VM。
3. Guest OS 身份：Linux 本地账号/SSSD 或 Windows 本地账号/域账号，决定进入桌面后是谁以及具有什么系统权限。

平台 OIDC Token 和密码不会转发给 Guest。SSSD、AD Domain Join 或 Entra Join 是 Guest 层能力，不能替代平台初始化所需的本地管理员。

## 桌面授权

- `managed_desktops` 是 PVE 受管 VM 的本地注册表。只有非模板 QEMU VM 且带 `vc-workspace` 标签才会进入；滚动迁移期间仍识别已有 VM 的旧标签。同步同时读取 PVE `ostype` 并持久化为 `linux`、`windows` 或 `unknown`；已知类型不会因一次读取失败被降级。旧记录在重新同步前保持 `unknown`，此时服务端拒绝绑定无法验证平台的目录 Profile。同步会保留已经离开集群的记录，但它们不再可访问。
- `personal` 桌面只有一个 `owner_user_id`；`shared` 桌面可直接授权用户，也可授权本地、OIDC 或 SCIM 风格同步的组。Agent 分配始终单独维护，不因人的组成员关系隐式获得权限。
- 从旧模型升级时，只有一个用户分配的桌面转成 Personal；存在多个用户分配的桌面自动转成 Shared 并保留所有既有授权，不静默撤销访问。
- 管理员也必须被授权后才能从 Native 客户端看到桌面；Web 管理视图仍可读取全局清单。普通用户的 Web/Native 清单、电源操作和连接描述符都在服务端按 `user_id + vmid` 校验；未授权目标返回 404。
- 每个 Agent 有独立的 Opaque 凭证，数据库只保存摘要。MCP HTTP 在每次请求上解析凭证并注入 Agent ID，工具 Schema 不包含 `agent_id`；`stdio` 在启动及每次工具调用前完成同样的凭证解析。
- 停用用户会撤销 Web/Native 会话、未兑换 Native 授权码及活动桌面连接；后台维护循环重试尚未完成的 Guest 凭据撤销。停用 Agent 或移除 Agent 分配会吊销相应 Lease 并递增 `control_epoch`。
- 列举、准入和撤权共用 `effective_user_desktop_access`。所有会减少人类授权的 Store 事务与连接记录签发共用短事务锁；删除 Owner/用户/组分配、移除成员、停用组/用户和清单同步后，失去最后一条有效授权的活动连接在同一提交内转为 `revoking`，其 Guest Binding 同时禁用并进入独立持久化撤销队列。普通断开后没有活动连接的 Binding 也会排队；保留另一条有效授权时不误撤权。
- Job 幂等键按主体、HTTP 方法和路径隔离，并保存请求内容的 SHA-256 指纹；相同键、不同内容返回 409。重放前仍验证当前授权，不能借已完成任务绕过桌面访问限制。
- 升级前只有全局原始键、没有指纹的历史任务不能安全推断请求归属与内容；遇到对应旧键统一返回 409，既不泄露旧任务，也不自动重新发起副作用。管理员先确认旧任务结果，再决定是否提交新的业务请求。

## Guest OS Identity Profile

管理后台把 Guest 身份配置保存为结构化 Profile，不允许保存密码、Token、Client Secret、私钥或多行配置片段。域加入密码与实验性 OIDC Client Secret 只在点击“应用”时输入，经 PVE REST `agent/exec` 的原始 `input-data` 发送到标准输入；PVE 负责对 QEMU Guest Agent 做协议编码，控制面不得提前 Base64。Secret 不进入 Guest 命令参数、数据库和审计详情。

Profile 与桌面 OS 平台必须一致。控制面在保存和应用时都强制校验；Web 只展示当前桌面平台兼容且已启用的非默认 Profile，默认本地托管账号以单一“Managed local”选项表达，不重复列出 Linux/Windows 内置实现。

| 模式 | 平台 | 当前行为 | 状态 |
|---|---|---|---|
| `managed_local` | Linux / Windows | 为每个授权平台用户生成稳定、不可反推的 `vcw…` Guest 用户；每次连接轮换随机密码 | P0 默认 |
| `linux_sssd_ad` | Debian 13 | `realmd/adcli` 加域，SSSD AD Provider，并强制 `simple_allow_groups` | P1 已实现；独立 Debian 12 PVE Guest→Samba AD 已验收，正式 Debian 13 模板与生产 AD 待验收 |
| `linux_sssd_freeipa` | Debian 13 | `freeipa-client` 无人值守注册并启用自动 Home | P1 已实现，待真实 FreeIPA 验收 |
| `linux_sssd_ldap` | Debian 13 | SSSD LDAP Provider、StartTLS/LDAPS、LDAP access filter | P1 Docker 真实目录已验收，待 PVE/生产目录与客户端闭环 |
| `windows_ad` | Windows 10/11 | 首次在线 `Add-Computer` 后转为 `restart_required`；重启并再次应用时核对域并把允许组加入 Remote Desktop Users | P1 已实现，待真实 AD 验收 |
| `linux_sssd_oidc` | Debian 13 | SSSD IdP Provider 与设备授权端点 | P2 实验性，默认关闭 |
| `windows_entra` | Windows 10/11 | 校验 Entra Join、Tenant ID 与目标主机，再授予指定 `AzureAD\\principal` RDP 权限 | P2 实验性，默认关闭 |

Debian 13 模板内置 `sssd-ad`、`sssd-ipa`、`sssd-ldap`、`sssd-idp`、`freeipa-client`、`realmd`、`adcli`、Kerberos 和自动 Home 依赖；Windows 模板写入能力清单。旧模板缺少能力清单时，控制面拒绝应用而不是猜测。Debian 13 当前提供 SSSD 2.10；生成配置不使用该版本已经移除的 `config_file_version`，LDAP StartTLS/LDAPS 统一从系统 CA Bundle 验证服务端证书。

实验能力必须同时通过服务端开关和真实环境验收：`VC_WORKSPACE_EXPERIMENTAL_SSSD_OIDC_ENABLED` 默认 `false`；`VC_WORKSPACE_EXPERIMENTAL_WINDOWS_ENTRA_RDP_ENABLED` 默认 `false`，且默认 macOS FreeRDP 构建尚未启用 AAD，因此不能宣称 Entra RDP 可用。

当前 Native Broker 只为 `managed_local` 签发自动填充的短期凭据。目录 Profile 已能配置和收敛 Guest，但 AD/FreeIPA/LDAP 用户凭据、Kerberos SSO 或设备码对话尚未接入 macOS 客户端；在该客户端闭环完成前，目录模式不能标记为最终用户可用。

## 短期桌面连接

- 每次 Native 建联以 PostgreSQL advisory lock 串行化同一 VM；同一桌面至多有一个 `active/revoking` 的人工连接。
- 控制面为当前平台用户解析 Guest Binding、撤销前一凭据、吊销 AI Lease/Guest authority、设置一次性随机密码，并保存 8 小时过期的连接 Session。
- 描述符返回 `connection id + expires_at`。macOS 在取消、断开、远端结束、退出登录、关窗和退出应用时调用 `DELETE /native/connections/{id}`；服务端立即旋转 Guest 密码并将 Session 标记为已撤销。
- Native 注销先原子失效当前 Native Token、标记该用户的人工连接并为其所有 Guest Binding 排队；204 表示 Broker 已失效且撤销任务已持久化，不代表 QGA 已完成。改密和停用走同一短事务边界；改密前验证过的旧密码也不能在改密后新建 Web/Native 登录会话。在途建联必须在最终保存描述符时再次验证 Native Token 与当前授权。
- 普通断开保留 Guest 应用，但 Binding 独立保存最后一次签发的 OS 会话有效期。授权移除、改密、注销和到期均触发账号禁用及 OS 清理，即使对应连接早已关闭。连接准备会先处理本机到期与待撤销任务，不能通过抢在后台定时扫描前重连来复活过期会话。
- 清理任务在取得单桌面锁后重新读取连接与队列，不对旧快照执行 Guest 操作。队列使用请求/完成 revision；旧任务无法确认后来产生的撤销，失败保留任务重试。确认清理后清空旧 OS 到期时间，再允许签发新连接，避免旧期限误杀正在重新准备的账号。重新启用时先更换随机密码，再解除账号禁用。
- Linux 用 [usermod](https://manpages.debian.org/trixie/passwd/usermod.8.en.html) 同时锁定密码、设置账号过期，再终止受管 `vcw…` 用户的 systemd 登录与残留 UID 进程；不只轮换密码。Windows 先禁用本地账号，再用 [WTS 会话 API](https://learn.microsoft.com/en-us/windows/win32/termserv/terminal-services-administration) 精确匹配本机用户并注销，不解析本地化命令输出。Debian 13 已实测账号锁定、xrdp PAM 拒绝、残留进程清理、对照账号不受影响和重新启用；Windows WTS 与 macOS 可视 RDP 注销仍由 REVIEW-02 跟踪。
- 后台维护启动即扫描，每轮后等待一分钟；PVE/QGA 不可达、锁等待和任务积压会延后 Guest 收敛，不能把一分钟当作强制断线 SLA。待清理期间 Broker 拒绝该 VM 的新连接，但当前直连 RDP 架构无法保证离线 Guest 立即注销；需要 Gateway/Guest 本地有效期执行后才能承诺网络分区下的强制撤权。此边界也不承诺对已经取得 Guest root/Administrator 权限的恶意用户形成隔离。

### Native 账号写入版本

持久层、独立 Native 凭据协议、Linux 执行端和默认 Linux HTTP/QGA 路径已接线；隔离 Debian 已验证真实 RDP/PAM 登录域、桌面保留与到期，默认 HTTP→macOS 已通过保留应用重连、撤分配与注销切片。Windows 新增独立 SAM/WTS 执行端，但默认 Broker、完整登录/进程边界及客户端链路尚未验收。新代码要求 Guest 显式升级及固定账号归属，不回退用户名式写入。

028 迁移新增 `native_guest_accounts`，把固定 OS 账号与临时 RDP 凭据分开。只有独立验证 Guest 所有权后，调用方才能绑定用户派生的 `vcw…`、Linux UID 或完整 Windows SID；数据库不从旧用户名、旧连接或成功登录猜测身份。已绑定身份和 Profile 不可改绑，Native 用户之间以及 Native/Agent 之间不能共享同一 VM 的 OS 身份。

账号操作使用单调 revision 和 `pending → applied`：`issue` 预留新凭据，`retire` 退役凭据但保持原 OS 会话期限，`revoke` 撤销账号并以更高版本覆盖未确认的签发/退役。只有已完成且未到期的退役、已完成的撤销或首次空闲账号才能再签发；保留桌面到期后必须先确认撤销。同一旧版本不能回退、跳号或确认新操作。`native_guest_credential_ids` 保留历史连接 ID，回执丢失后不能把该 ID 重新用于另一轮登录。记录不含密码，待签发记录只持有 Native Token 摘要用于完成前再次鉴权。

签发预留与确认均核对当前 Native Token、桌面访问、平台和启用的受管本地 Profile；待撤权或 Agent 尚未清理时不得新签发。Native 待处理/已签发操作与同 VM 的新 Agent Lease、其他 Native 控制预留互斥。重启恢复可枚举未完成意图和已到期的保留桌面；未知 Guest 结果不能通过重发密码确认成功，应独立观察或预留更高版本的撤销。清理不要求用户仍持有效登录，但必须匹配原身份、连接和完整版本。

Linux 执行端使用 `crates/guest-lifecycle` 的独立 `native` 协议，以固定 UID、连接 ID 和 revision 校验 stdin 请求。`native-account-lifecycle.json` 不复用 Agent 的 Lease/generation 账本；发布 `issuing/retiring` 意图后才写 shadow。普通退役只轮换密码，未到期的退役后重连也不清空用户进程；显式撤销、原期限到期或写入中断由正式离线回收器禁用账号并排空受管进程域。密码不进入参数、账本或回执。Guest 允许更高版本撤销覆盖尚未收到的新连接签发，但相同版本重试必须匹配原连接，且不能重新安装密码。

Windows 实验执行端提供相同的 `computer-v2-native-account-provision/credential/inspect` 命令，只接受规范 `vcw…` 账号。SYSTEM-only `HKLM\SOFTWARE\VCWorkspace.NativeAccountsV1` 保存独立所有权，`NativeLifecycle` 值保存固定 SID、连接 ID 与 revision；Agent 的 `vca…`、所有权根及 `Lifecycle` 不混用。未知旧账号、同名替换 SID 或混入 Agent 记录均拒绝接管。每次写入持独立账号门，先 `RegFlushKey` 保存意图，再改变 SAM，完成后另存回执；密码只走有界 stdin，不写账本或日志。退役只随机轮换密码，保持启用状态和原到期时间；未到期退役后的新签发不主动注销 WTS，撤销则禁用精确 SID 并等待其 WTS 登录消失。

Windows 共用本地巡检入口检查 Agent 与 Native 两个账本，其中一个失败仍执行另一个。Native 过期或未提交意图只转为撤销，不重放密码；损坏生命周期在固定 SID 归属可证明时关闭该账号/WTS，但保留损坏记录并返回错误，不清空历史后重新签发。`inspect` 只报告真实 SAM/WTS 和生命周期，不提供 `processes_absent` 或 `login_writers_absent`，也不把 WTS 空列表当作全部令牌/进程创建路径已受保护。默认 Windows Native 版本化 Broker 与 MCP 能力仍不开放；实验命令或原生账号测试不能替代真实桌面保留、并发登录与完整撤权验收。

Windows 还有资料持久性边界：实验 SAM 轮换使用 SYSTEM `NetUserSetInfo`，属于重置，而非持旧密码的用户改密。现在 `issue/retire` 在任何账本或 SAM 修改前检查固定 SID 的 ProfileList；已有 Profile 时明确拒绝不安全的轮换，读取异常也拒绝。`revoke` 和本地到期回收不受此保护阻挡。这是防止资料损坏的临时限制，不是完成了 Windows 保留桌面重连，也没有替换旧版未绑定账号路径。

Windows 10 另做了仅对夹具固定 SID 添加五类 LSA 登录拒绝权的对照：账号保持启用，旧密码/新密码的五种 `LogonUserW` 登录均被拒绝，移除自身新增权利后新密码可登录，但原 DPAPI 资料仍不可读。LSA 全局账号与拒绝权枚举已恢复原基线；未改其他用户、组或产品登录策略，也未据此验证真实 RDP/WTS 登录入口。此候选仍未接入产品。登录拒绝权语义参见 [Microsoft Account Rights](https://learn.microsoft.com/en-us/windows/win32/secauthz/account-rights-constants)。后续的短期连接隔离由 [Gateway 边界](session-gateway.md) 跟踪，不能以网络票据替代 OS 凭据与资料完整性验收。

Microsoft 明确说明管理员重置可能使此前的 DPAPI 数据无法恢复；不能用相同 SID、密码可登录或保留 WTS 证明凭据库/加密资料仍可用。隔离 Windows 10/11 的真实 Profile 实验进一步发现：持旧密码调用 `NetUserChangePassword`，账号启用时数据可读，先禁用或设置过去的账号到期时间再改密时，新密码虽可登录，旧 DPAPI 数据却不可读。`CryptUpdateProtectedState` 在 SYSTEM 线程模拟和真实用户主进程中均报告迁移计数，但新登录仍未恢复原数据，因此未采用这些候选作为产品修复。参见 [Microsoft DPAPI 限制](https://learn.microsoft.com/en-us/windows/win32/seccrypto/example-c-program-using-cryptprotectdata)、[改密与重置的区别](https://learn.microsoft.com/en-us/troubleshoot/windows-server/active-directory/password-change-mechanisms)及[密钥迁移 API](https://learn.microsoft.com/en-us/windows/win32/api/dpapi/nf-dpapi-cryptupdateprotectedstate)。这些结果是当前隔离镜像的实测，不推断所有 Windows/域策略均有相同行为。

候选凭据封装采用 SYSTEM 用户范围 DPAPI，而非所有本机用户可解密的机器范围模式；密文绑定用户名、SID、所有权 nonce 和 revision，输入及解密内存有界并清零。目前只完成封装原语与测试，尚未接入持久化 current/pending 账本、改密中断恢复或正式签发。默认链路开放前仍须设计保留加密资料的完整轮换，并验收真实凭据库、注销后新登录和重启；不得清除 Profile、删除密钥或自动重置资料来通过测试。

Native xrdp 登录保护使用单独的 `pam_vcworkspace_native.so`，Agent 模块继续只处理 `vca…`。签发/退役要求当前 Agent PAM 前缀之后紧跟 Native 前缀，并校验模块归属/权限；未安装时拒绝写入，inspect 的登录写入者状态返回未知，而非宣称已排空。Native 登录域绑定连接 revision，只允许未到期的 `issued` 凭据进入。`deploy/guest/linux/install-login-fence.py --native` 是独立离线维护入口，要求先安装当前 Agent 保护、停止会话创建进程并排空 Xorg，保存精确 0600 备份后才启用；无参数的 Agent 安装不会自动启用 Native。新 Debian 13 Packer 配方在离线阶段安装两个模块、依次调用两个入口并检查能力，Builder 显式要求 Native 模块产物；这不是已有模板或旧账号的自动迁移，也不代表已完成模板实建。

默认 Linux 建联先检查 Guest/PAM 能力，再处理旧连接；独立确认受管账号所有权并绑定 UID 后，预留版本、通过一次 QGA stdin 请求写入、独立检查回执与 Guest 状态，最后在同一事务中确认版本、创建 Connection Session 和更新原始期限。回执或提交结果不确定时不重发密码，持久保留更高版本撤销。普通 DELETE 只退役凭据；注销/撤分配队列和后台恢复使用同一精确身份协议。丢失退役回执可由独立观察确认，观察本身失败不会推断可以杀死保留桌面。不同控制面副本均在同 VM 数据库锁内重读状态。

028 不回填未知旧 UID/SID，不代表旧 Guest 已升级或排空。旧版 Linux Guest/PAM 或没有归属记录的旧 `vcw` 会拒绝新连接，须经过显式离线升级/迁移；Windows 未绑定账号仍沿用既有路径，已绑定版本化账号不得回退。原开发库和运行中的服务不随源码修改自动升级。验收必须同时覆盖真实登录、保留应用重连、撤权与本地到期，并独立检查固定 UID/登录域和 Xorg 节点，不能把数据库 `applied` 当作完整 OS 登录证明。已通过的默认 HTTP/macOS 切片及尚未完成的升级、跨端和故障矩阵统一记录在[项目状态](../plan/status.md)。

在独立测试数据库设置 `VC_WORKSPACE_TEST_DATABASE_URL` 后，运行 `go test -race ./internal/store -run '^TestNativeGuest' -count=2`；每个测试使用随机 schema 并自行清理。该门禁包含 PostgreSQL 的 Linux/Windows 身份模型、并发、失效授权、旧回执和升级，不是 PVE/Windows SAM/macOS 桌面验收。当前退出条件统一见 [REVIEW-12](../plan/status.md)。

`make computer-session-check` 使用独立、无网络的一次性容器验证 Native 真实 shadow/UID 进程、写入进程退出、持锁写入者孤儿、到期和损坏账本恢复，并通过真实 libpam 验证两个模块的账号命名空间隔离。shadow 故障测试的 PAM 前置绕过只存在于显式 opt-in 的 Rust 测试二进制，正式 CLI 没有故障注入或绕过选项；另一个容器测试正式 CLI 在显式安装前后的拒绝/允许行为。这些测试不包含真正的 sesexec/logind 登录或桌面重连。

显式 opt-in `make pve-live-native-desktop-check` 仅用于预置当前 amd64 Guest/两个 PAM 模块的隔离 VM 160。它使用证书指纹固定、stdin 凭据的临时 RDP 客户端，验证真实 xrdp/PAM、退役与重连保留同一个桌面、精确撤权和正式 timer 按原短期限回收；不会绕过登录保护或修改时钟/账本来模拟到期。第三次登录后用已核验的 pidfd 冻结真实 Xorg，要求正式回收器处理无法正常响应退出的桌面。正常撤销及冻结会话到期后，必须在夹具清理前观察到原 Xorg socket/锁节点消失；夹具的精确 inode 清理仅作失败恢复，不充当通过证据。独立记录新连接所接的 Xorg/UID，以及账号和恢复资料；最终恢复 PAM/服务并删除精确测试身份。该客户端不是 macOS `.app`，测试也不经过默认 HTTP Handler；每批真实结果统一记录在状态页。

产品回收在锁定登录、排空固定 UID 与完整登录域后，使用受保护的目录句柄处理该 UID 的规范 X11 节点；正常退出机会有上限，最终仍强制核查原 cgroup。混合归属、链接、活进程锁或不安全目录使清理失败并阻止下一次签发，不清除未知对象。正式账号 worker 因此使用共享 `/tmp`，readiness 服务仍使用私有临时目录；完整权限和进程边界见 [ADR 0003](../decisions/0003-computer-use-data-plane.md#版本化账号登录)。

## 数据库恢复后的 Native 版本核对

克隆访问前置保护采用同一 Job 条件（用户有效授权视图由迁移 031 更新，Agent 查询使用兼容旧库升级夹具的等价条件）：存在目标 VMID 相同、尚未成功的 `pve.template_clone` Job 时，用户有效授权以及 Agent 列表、租约和 Guest 会话签发拒绝放行。清单发现 VM、已有用户分配和登记表 enabled 都不能替代克隆成功；没有平台克隆记录的既有导入桌面保留原规则。失败或结果不确定的克隆记录保持拒绝，不能通过删除 Job 或复用目标 VMID 绕过；其人工恢复与 VMID 所有权流程仍须继续完善。该保护不回收备份中丢失的 Job 历史，也不替代 Guest 本地到期或现有会话撤权。

后台克隆收尾持桌面控制锁重读 Job，并在 GPU 配置前和登记前重新观察目标的节点、QEMU 类型、非模板状态、名称、Job 标记及无锁条件。不匹配或读取失败时保持待收尾，不依据历史任务成功放行当前 VM。该数据库锁仅串行化平台实例；PVE 外部管理员不受此锁约束，可变标记也不是不可伪造的资源身份，因此重复观察不能被解释为跨系统原子所有权保证。

GPU 写入额外绑定刚读取且身份条件符合的配置 `digest`（40 位十六进制 SHA1），完整 PCI 和 GVT-g/mdev 两条路径均必填，不允许降级为无条件 PUT。摘要由 PVE 生成，不接受管理员请求传入；缺失或无效时保留待收尾，PVE 拒绝写入时沿用 GPU 配置失败隔离流程，不自动移除摘要重试。此机制利用 [PVE 配置 API 的并发检查](https://github.com/proxmox/qemu-server/blob/master/src/PVE/API2/Qemu.pm)，保护该次配置写入，不为后续数据库登记提供跨系统事务，也不能识别完全相同配置的资源替换。

若已核对的 `hostpci0` 与原请求生成的完整参数集合一致，后台收尾跳过 GPU PUT，继续登记与 Job 收敛。这使 GPU 成功但数据库登记失败后的恢复不重复设备配置。比较忽略参数顺序，但拒绝重复键、缺少参数、额外参数及隐式默认值；该判断不证明 Guest 驱动或实际加速可用。

已保存 Linux OS 快照的克隆在收尾时补齐主网卡 cloud-init 默认值：存在 cloud-init 磁盘且 ipconfig0 为空时，要求 net0、正确目标标记、无锁及合法摘要，再条件写入 ipconfig0=ip=dhcp 并回读。已有 ipconfig0、不使用 cloud-init 或非 Linux 克隆不改变网络；其他网卡不被覆盖。观察或写入失败保持 Job 待收尾，已应用 DHCP 的重试不重复写入。这不是静态地址分配器，已有静态地址继承、多网卡策略及 Windows Cloudbase-Init 需要独立管理规则。

恢复旧备份不能回退 Guest 的单调版本。管理员可调用 `GET /managed-desktops/{vmid}/native-recovery` 读取固定账号身份的诊断结果；`guest_ahead_closed` 只表示 Guest 已撤销且版本领先，不自动授权恢复。失败或无法判断的观察不伪造 Guest 版本。

Linux 的显式恢复入口为 `POST /managed-desktops/{vmid}/native-recovery/{user_id}`，提交预期库版本、预期 Guest 版本和原因。服务端持桌面控制锁重新观察相同 UID/用户名；仅接受库中已完成撤销、Guest 同一身份已撤销且无进程/登录写入者的前向版本。事务内再次检查管理员未禁用，以及桌面的连接、Native 意图、Agent 会话、租约和撤销队列没有未完成工作。

迁移 030 增加恢复记录：操作者、原因、前后版本与连接标识在同一事务内与账号版本前移提交。原版本触发器仅在本事务存在精确匹配的恢复记录时允许这类跳跃；普通跳版本、回退、失败比较后残留记录，以及对记录的 UPDATE/DELETE 均被拒绝。该表不是对数据库超级用户的防篡改承诺。恢复不改 Guest、不签发密码、不恢复丢失的历史审计；后续连接仍须正常授权并使用更高版本。状态改变或响应丢失后的旧请求重复提交返回 409，应重新核对，不盲目重放。

POST 超时或响应读取失败时，结果应视为未知，而不是恢复失败。重新 GET 核对：若为 `aligned_closed` 且库和 Guest 均达到此次预期版本，则账号已对齐，无须再次导入；若仍为原库版本的 `guest_ahead_closed`，也应重新确认完整恢复条件后再显式提交。任何其他版本、身份、生命周期或检查不可用结果均应停止自动重试并调查，不能清除 Guest 栅栏。诊断对齐只证明当前账号状态，不补回丢失的历史记录，也不替代后续连接授权。

此流程目前的真实环境与故障矩阵验收范围以[状态文档](../plan/status.md)为准；不能将独立数据库和模拟 Guest 的通过结果扩展为灾难恢复已完成。

## Guest 本地权限

管理员为每台受管桌面选择固定的 `privilege_mode`：

- `standard`：Debian 从 `sudo`/`admin` 组移除所有已绑定 Guest 用户，Windows 从本地 Administrators 组移除这些用户。它阻止系统级软件安装和系统配置变更，但不承诺阻止用户目录内的便携软件。
- `local_admin`：Debian 将已绑定用户加入 `sudo` 并为每个账号生成受控的 `NOPASSWD` 规则；Windows 将其加入本地 Administrators。Windows 仍保留 UAC。切回 `standard` 会删除对应规则并反向验证提权已失效。

为兼容尚未重建的模板，策略收敛仍同时检查旧 `vdi` 用户；新连接不再让所有平台用户共享该账号。控制面在 PostgreSQL 保存 desired/applied revision 和 `pending`、`applied`、`failed` 状态。运行中的 VM 立即通过 QGA 收敛；关机 VM 在建联前应用。Guest Agent 不可用、OS 不支持或验证失败时不得签发新的 RDP 描述符，失败与恢复均写入审计。
