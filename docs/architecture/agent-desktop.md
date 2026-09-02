# AI Agent 桌面

AI 能力建模为 `Desktop Lease`，不是把 PVE 或通用远程桌面密码交给 Agent。

## MVP 能力

- 管理员为每个 Agent 建立独立主体、签发只显示一次的 Opaque 凭证，并显式分配可访问桌面。数据库只保存凭证摘要。
- 远程 Agent 用自己的 Bearer 凭证访问无状态 Streamable HTTP `/mcp`；本机 `stdio` 在启动及每次工具调用前验证同类凭证。MCP 再使用内部服务令牌调用控制面，两种令牌都与 PVE 凭证分离。
- Agent ID 来自认证上下文，不在 MCP 工具输入中出现；调用者不能借由提交其他 `agent_id` 访问未授权桌面。
- `desktop_acquire/lease_get/release` 为显式生命周期，包含 TTL、Agent、Desktop 与 `control_epoch`。
- 每台桌面只有一个活动租约；过期租约在下一次领取时失效。
- 停用 Agent 会吊销其全部活动租约；移除一个桌面分配会吊销该 Agent 对应桌面的活动租约；轮换凭证后旧值立即不能认证。
- 当前可变更能力仅为租约内 `start/stop`，每次动作写入 Job 与审计事件。
- MCP Server 只调用控制面，不读取 PVE 凭证，不承担连续视频串流。
- 浏览器 Origin 请求被 MCP 端点拒绝；面向人类的 Web Session 不能替代 Agent Token。Kubernetes 入口只代理 `/mcp`，MCP Service 不直接暴露为 LoadBalancer 或 NodePort。

当前边界必须明确：这些工具只提供桌面清单、Lease 和电源控制，尚未提供截图、可访问性树、键盘、鼠标或文件操作，因此不能把当前 MCP 描述成已经可以“操作远程桌面”。

## 后续能力

下一条纵向切片由 [AI-01](../plan/status.md#todo) 跟踪。Guest Agent 或独立的 Computer Use 数据面将提供截图、可访问性树、受控输入、Artifact 和审批，并继续复用现有 Lease、Agent 分配、`control_epoch` 和审计边界。人工接管会递增 `control_epoch` 并拒绝旧 Epoch；语义动作优先于可访问性操作，坐标操作只作为最后兜底。任何输入工具都不得直接接收 PVE 凭证或绕过桌面分配。
