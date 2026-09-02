# MCP Server

独立 Go 进程，通过控制面的内部 Agent API 管理专属桌面，不直接持有 PVE 凭证。当前提供：

- `desktop_list`：列出由 VC Workspace 管理的桌面；
- `desktop_acquire` / `desktop_lease_get` / `desktop_release`：独占且自动过期的 Agent 租约；
- `desktop_power`：仅对租约覆盖的桌面执行启动或停止。

本地 `stdio` 启动：

```bash
VC_VDI_API_URL=http://127.0.0.1:8080 \
VC_VDI_INTERNAL_API_TOKEN=<same-token-as-control-plane> \
VC_VDI_MCP_ACCESS_TOKEN=<credential-issued-for-this-agent> \
go run ./apps/mcp
```

进程启动及每次工具调用前都使用 Agent 凭证重新解析身份，因此停用或轮换无需重启进程即可生效。标准输出只承载 MCP stdio 协议，Agent 身份不会出现在工具参数中。

Kubernetes 或独立服务使用无状态 Streamable HTTP：

```bash
VC_VDI_API_URL=http://vc-workspace-control-plane:8080 \
VC_VDI_INTERNAL_API_TOKEN=<same-token-as-control-plane> \
VC_VDI_MCP_TRANSPORT=http \
VC_VDI_MCP_HTTP_ADDR=0.0.0.0:8090 \
go run ./apps/mcp
```

管理员在 Web“访问控制”中为每个 Agent 单独创建并保存一次性显示的凭证，再显式分配桌面。Agent 连接 `https://<public-host>/mcp`，发送 `Authorization: Bearer <agent-credential>`；MCP 服务向控制面校验凭证并从认证结果注入 Agent 身份，工具调用者不能提交或覆盖 `agent_id`。停用 Agent、轮换凭证或移除桌面分配会立即阻断后续调用；移除分配和停用还会吊销活动租约。

HTTP 模式拒绝带 `Origin` 的浏览器请求，提供 `/health` 进程探针和跟随控制面数据库状态的 `/ready` 探针。控制面内部服务 Token 不得交给 Agent。Kubernetes 部署见 [部署说明](../../docs/operations/kubernetes.md)。

当前 MCP 尚未暴露屏幕、可访问性树、键鼠、文件或 Guest Agent 命令，不能用于完成 Computer Use 任务；这些能力由 [AI-01](../../docs/plan/status.md#todo) 跟踪，并且必须继续受同一租约、`control_epoch`、审批和审计边界约束。
