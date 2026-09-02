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

## OIDC

- Web：Authorization Code + PKCE，BFF 持有 Token。
- Native：复用控制面的 Code + PKCE 回调，控制面签发两分钟有效的一次性授权码，再由客户端换取独立 Bearer Session；Session 保存在 Keychain。
- Web Cookie Session 与 Native Bearer Session 相互独立并可撤销。生产部署可进一步拆分 OIDC Client Registration。
- Discovery、JWKS、Redirect URI 与 Claims 诊断必须脱敏。

当前实现已经覆盖协议和 Session 交换，但尚未接入真实 IdP 完成端到端验收，因此状态是“已实现，待实测”，不能标记为生产可用。验收项由 [OIDC-01](../plan/status.md#todo) 统一跟踪。

## 桌面授权

- `managed_desktops` 是 PVE 受管 VM 的本地注册表。只有非模板 QEMU VM 且带 `vc-vdi` 标签才会进入；同步会保留已经离开集群的记录，但它们不再可访问。
- 用户和 AI Agent 使用两张显式分配表。管理员也必须分配后才能从 Native 客户端看到桌面；Web 管理视图仍可读取全局清单。
- 普通用户的 Web 清单、Native 清单、电源操作和连接描述符都在服务端按 `user_id + vmid` 校验；未授权目标返回 404，避免泄漏其存在。
- 每个 Agent 有独立的 Opaque 凭证，数据库只保存摘要。MCP HTTP 在每次请求上解析凭证并注入 Agent ID，工具 Schema 不包含 `agent_id`；`stdio` 在启动及每次工具调用前完成同样的凭证解析。
- 停用用户会删除其 Web Session、Native Session 和未兑换 Native 授权码。停用 Agent 会吊销该 Agent 的所有活动 Lease；移除单个 Agent 桌面分配会吊销对应桌面的活动 Lease。凭证轮换后旧凭证立即失效。

## Guest 本地权限

管理员为每台受管桌面选择固定的 `privilege_mode`：

- `standard`：Debian 从 `sudo`/`admin` 组移除 `vdi`，Windows 从 SID `S-1-5-32-544` 对应的本地 Administrators 组移除 `vdi`。它阻止系统级软件安装和系统配置变更，但不承诺阻止用户目录内的便携软件。
- `local_admin`：Debian 把 `vdi` 加入 `sudo` 并安装一条受控的 `NOPASSWD` 规则，Windows 把 `vdi` 加入本地 Administrators。Windows 仍保留 UAC；Debian 无需向用户泄露连接时自动轮换的桌面密码。切回 `standard` 会删除该规则并用 `sudo -n` 反向验证提权已失效。

控制面在 PostgreSQL 保存 desired/applied revision 和 `pending`、`applied`、`failed` 状态。运行中的 VM 立即通过 PVE QEMU Guest Agent 执行仓库内固定命令；关机 VM 保存为 `pending`，在原生客户端建联前应用。Guest Agent 不可用、OS 不支持或验证失败时不得签发新的 RDP 描述符。

只有组成员或受控 sudoers 规则实际改变时才结束 `vdi` 的现有会话，使新的权限立即生效；相同策略的连接前重验不注销用户。Web API 只接受两个枚举值，不接受用户名、脚本或任意命令。策略更新和连接前执行失败均写入审计。
