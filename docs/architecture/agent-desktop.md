# AI Agent 桌面

AI 能力建模为 `Desktop Lease`，不是把 PVE 或通用远程桌面密码交给 Agent。

## MVP 能力

- 管理员为每个 Agent 建立独立主体、签发只显示一次的 Opaque 凭证，并显式分配可访问桌面。数据库只保存凭证摘要。
- 远程 Agent 用自己的 Bearer 凭证访问无状态 Streamable HTTP `/mcp`；本机 `stdio` 在启动及每次工具调用前验证同类凭证。MCP 再使用内部服务令牌调用控制面，两种令牌都与 PVE 凭证分离。
- Agent ID 来自认证上下文，不在 MCP 工具输入中出现；调用者不能借由提交其他 `agent_id` 访问未授权桌面。
- `desktop_acquire/lease_get/release` 为显式生命周期，包含 TTL、Agent、Desktop 与 VM 级持久化 `control_epoch`；新租约不重置为 1。
- 每台桌面只有一个活动租约；过期租约由维护循环及下一次控制请求收敛，并排队撤销对应 OS 会话。
- 管理后台的 Agent 列表只返回未过期的活动租约，并显示当前受控 VM 与到期时间；人工接管、释放或吊销后该状态随下一次读取消失。
- 停用 Agent 会吊销其全部活动租约；移除一个桌面分配会吊销该 Agent 对应桌面的活动租约；轮换凭证后旧值立即不能认证。
- 租约内可变更能力包括 `start/stop`，以及 AI-01 第一协议版本的截图、控件级可访问性快照、鼠标、非打印按键和文本输入。所有 Computer Use 动作必须同时匹配 Lease ID 与当前 `control_epoch`。
- MCP Server 只调用控制面，不读取 PVE 凭证，不承担连续视频串流。
- 浏览器 Origin 请求被 MCP 端点拒绝；面向人类的 Web Session 不能替代 Agent Token。Kubernetes 入口只代理 `/mcp`，MCP Service 不直接暴露为 LoadBalancer 或 NodePort。

Computer Use 数据面采用 [ADR 0003](../decisions/0003-computer-use-data-plane.md) 的 QGA 信令与交互用户 Helper。默认动作使用 Linux V2：首次建立独立 `vca…` 本地账号和 xrdp/Xorg 会话，请求绑定真实 UID、交互会话与 Helper 实例，root 与 Helper 通过 Unix Peer UID 互验。控制面调用固定 `computer-v2-*` 子命令，内容不进入 argv；每次动作重新同步 authority，不信任跨副本内存缓存。Debian 返回 `source=linux_atspi`，语义服务不可用时才降级为 `source=window_enumeration`；当前 Windows 每用户链路明确不可用，不回退到旧共享 Helper。文件操作、持续视频和无界 shell 均未开放。

主屏截图在 Guest 返回原始桌面输入边界与缩放后图片尺寸；MCP 验证 JPEG、摘要和有界元数据后以标准图片内容块返回，结构化输出不携带图片 Base64。截图像素需要按边界/尺寸映射为鼠标坐标；可访问性节点无需再次缩放，显示变化后必须重新观察。接口格式与验收入口见 [MCP Server](../../apps/mcp/README.md)。

## 控制权一致性

- 人工建联、Agent 动作、领取/释放租约使用同一 PostgreSQL 单桌面控制锁，跨服务副本串行化。锁使用独立有界连接池，最多占用 `min(业务池上限, 4)` 条连接，不能耗尽动作自身需要的授权查询连接。连接等待取消后关闭锁状态不确定的连接。
- 租约创建在短事务内再次检查 Agent/桌面/分配状态，活动或尚未清理的人工连接阻止 Agent 领取。动作在取得控制锁后、同步 Guest authority 后及返回观察数据前再次校验；等待期间撤权不能继续执行，执行期间撤权不再返回截图或控件数据。
- 账号身份与交互实例分开持久化：026 固定每 VM/Agent 的平台及 UID/完整 SID，释放与重新领取不清空或替换它；Helper/登录实例可以刷新。发现账号不匹配则拒绝下发 authority 并排队撤销。该模型已用于 Linux，也为 Windows 留出身份边界，但不提前开放 Windows 未完成的数据面；升级约束见 [Agent OS 账号生命周期](../decisions/0003-computer-use-data-plane.md#agent-os-账号生命周期)。
- 租约从 active 转为 released/revoked/expired 时，数据库触发器在同一事务写入 `desktop_computer_revocations`。停用 Agent、移除分配和清单移除都走此路径；QGA 暂时失败不丢失吊销任务。释放 API 成功只表示 Broker 已释放，Guest fencing 可能仍待后台执行。
- 后台及下一次动作/人工建联在单桌面锁内重读队列并同步 Guest tombstone；完成 revision 必须与请求 revision 一致，完成事件与确认原子提交。旧快照不能重复撤销新 authority，进程内缓存不能跳过跨副本重新同步。
- 控制面锁之外，Linux Guest 以独立 stdin 输入、持久排他锁和单调 epoch 比较提交授权；旧清理使用队列自己的 closing epoch，不借用新租约版本。授权结果不确定时关闭精确租约并排队撤销，不自动重发激活。025 迁移会撤销旧活动租约，必须协同升级并确认旧 Helper 退出；协议、故障测试和升级步骤统一见 [ADR 0003](../decisions/0003-computer-use-data-plane.md#linux-授权发布与-vm-版本)。
- 这些保证不等于网络分区下立即停止已发出的输入；Guest 本地期限、会话身份隔离和 Gateway 的剩余边界仍以状态页为准。

## 已验证边界与后续能力

2026-09-04 的 AI-01 曾在 Debian 13 与 Windows 11 的旧 `vdi` 交互会话完成端到端任务；该证据不代表当前每用户链路。现有默认动作已切换到独立 Agent OS 身份，REVIEW-04 的 Windows 与显式人工接管仍在修复；跨副本控制与重试已通过 PostgreSQL 和可控 PVE/Executor 回归。

Linux V2 的两个真实 UID 图形容器测试覆盖截图、控件树、输入、重放、卡死恢复和权限拒绝。Debian 13 已完成 SDK MCP HTTP→控制面→PVE→无人值守 xrdp→Guest 的真实图片、缩放坐标点击、AT-SPI、输入、令牌轮换、MCP 服务重建和保留 Home 的重新领取；释放、停用/撤分配后账号与进程撤销通过，到期清理由控制面实机脚本另行覆盖。测试使用独立 schema 和临时账号，未重启既有 xrdp。Windows、Native 观察/接管、新模板和异常网络不能据此算作通过，最新证据见唯一状态页。

目标是让每个 Agent 使用服务端派生、明确分配的独立 OS 身份，绑定实际交互会话及 Helper 实例；不默认为 Agent 开放任意当前登录者的桌面。Artifact、文件操作和高风险动作审批仍属于后续协议扩展。任何输入工具都不得直接接收 PVE 凭证或绕过桌面分配。
