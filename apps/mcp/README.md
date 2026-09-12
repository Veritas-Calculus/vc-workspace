# MCP Server

独立 Go 进程，通过控制面的内部 Agent API 管理专属桌面，不直接持有 PVE 凭证。当前提供：

- `desktop_list`：列出由 VC Workspace 管理的桌面；
- `desktop_acquire` / `desktop_lease_get` / `desktop_release`：独占且自动过期的 Agent 租约；
- `desktop_power`：仅对租约覆盖的桌面执行启动或停止；
- `desktop_screenshot`：返回一次有界 JPEG MCP 图片，以及不含图片字节的尺寸、坐标和摘要元数据；
- `desktop_accessibility_snapshot`：读取当前 Agent Linux 会话的 AT-SPI 控件树（Windows 每用户会话待补齐）；
- `desktop_mouse` / `desktop_key` / `desktop_type_text`：执行受限鼠标、按键和文本输入。

本地 `stdio` 启动：

```bash
VC_WORKSPACE_API_URL=http://127.0.0.1:8080 \
VC_WORKSPACE_INTERNAL_API_TOKEN=<same-token-as-control-plane> \
VC_WORKSPACE_MCP_ACCESS_TOKEN=<credential-issued-for-this-agent> \
go run ./apps/mcp
```

进程启动及每次工具调用前都使用 Agent 凭证重新解析身份，因此停用或轮换无需重启进程即可生效。标准输出只承载 MCP stdio 协议，Agent 身份不会出现在工具参数中。

Kubernetes 或独立服务使用无状态 Streamable HTTP：

```bash
VC_WORKSPACE_API_URL=http://vc-workspace-control-plane:8080 \
VC_WORKSPACE_INTERNAL_API_TOKEN=<same-token-as-control-plane> \
VC_WORKSPACE_MCP_TRANSPORT=http \
VC_WORKSPACE_MCP_HTTP_ADDR=0.0.0.0:8090 \
go run ./apps/mcp
```

管理员在 Web“访问控制”中为每个 Agent 单独创建并保存一次性显示的凭证，再显式分配桌面；同一列表会显示 Agent 当前控制的 VM 和租约到期时间。Agent 连接 `https://<public-host>/mcp`，发送 `Authorization: Bearer <agent-credential>`；MCP 服务向控制面校验凭证并从认证结果注入 Agent 身份，工具调用者不能提交或覆盖 `agent_id`。停用 Agent、轮换凭证或移除桌面分配会立即阻断后续调用；移除分配和停用还会吊销活动租约。

HTTP 模式拒绝带 `Origin` 的浏览器请求，提供 `/health` 进程探针和跟随控制面数据库状态的 `/ready` 探针。控制面内部服务 Token 不得交给 Agent。Kubernetes 部署见 [部署说明](../../docs/operations/kubernetes.md)。

Computer Use 工具必须提交活动租约返回的 `control_epoch`，不能假设新租约从 1 开始。Guest 动作有 250–15000 ms 的硬超时；只有保留同一不可变请求及防重放保护的动作，才可在有界 transport budget 内重取。授权发布没有自动重试，返回值丢失按结果不确定处理，关闭租约并排队清理；重新调用应先读取租约状态，失效后重新领取。人工原生连接会先吊销同桌面的 Agent Lease，使旧 epoch 失效。文本正文和截图字节不写入审计，响应使用 `Cache-Control: no-store`。不提供文件读写、持续视频、任意 shell 或 Guest Agent 命令。

当前动作默认使用 Linux V2：Guest 必须安装新版 Agent，桌面启用 managed-local Linux 身份配置。第一次动作自动建立专属 `vca…` 账号和 xrdp/Xorg 会话，不需要先打开 Mac 客户端；账号不是 MCP 参数，也不会向调用方返回 OS 密码。整次控制面请求最多 65 秒，动作 worker 超时独立计算，响应最多 16 MiB。Windows 专属会话尚未实现，当前明确拒绝，不回退到共享 `vdi`。人类连接会撤销 Agent 并进入自己的账号，尚不等于查看 Agent 同一现场。

025/027 迁移会关闭升级前相应的活动 Agent 租约并排队回收。须安排协同升级：暂停 Agent 入口，释放旧租约并确认旧 Helper/交互进程退出，停下旧控制面副本并升级 Guest，再启动新版控制面完成迁移和剩余清理，最后恢复入口并重新领取。异常遗留未清理前保持入口关闭。Guest 和运行中的 Helper 都必须声明 `authority_transport=stdin_epoch_account_v2`；替换二进制不等于已升级旧进程。026 保留固定 UID/完整 SID，027 要求登录前绑定身份并预留登录版本；账号阶段与版本还参与每次输入/缓存检查。旧控制面不能混跑或直接回退；不要删除 Guest 栅栏或改文件权限绕过拒绝。数据库丢失/回滚后的 epoch 恢复不自动执行，升级边界见 [ADR 0003](../../docs/decisions/0003-computer-use-data-plane.md)。

Linux 默认账号链路还要求 Guest 声明 `login_birth_fence=pam_pidfd_v1`。须在维护窗口排空旧 QGA 登录命令、`xrdp-sesrun`/`xrdp-sesexec` 和旧交互会话，再使用 [正式 PAM 安装器](../../deploy/guest/linux/install-login-fence.py) 安装固定登录保护；只复制二进制不会安装 PAM，此时能力为 `unavailable`，控制面拒绝登录而不回退。登录创建者在认证前登记，回收同时核对固定 UID 进程和 root 创建者均已退出；运维及验收条件见 [Linux Guest 服务](../../docs/operations/pve-development.md#linux-guest-服务验收)。该保护不覆盖其他 PAM 服务、SSH 或未排空的旧版本进程。

截图只捕获主显示器。`content` 包含一个 `image/jpeg` 图片块和一份简短 JSON 文本元数据；`structuredContent` 使用相同元数据，不重复放入 Base64。服务端检查 JPEG 尺寸、SHA-256、大小上限和 Guest 返回的 `desktop_bounds`；旧 Agent 不提供坐标信息时明确报错，需要升级 Guest。

`screenshot.width/height` 是返回图片的尺寸；`screenshot.desktop_bounds` 是该显示器在桌面输入坐标中的 `x/y/width/height`。若根据缩小的截图点击，先换算：`x = bounds.x + floor(u * bounds.width / image.width)`，`y = bounds.y + floor(v * bounds.height / image.height)`。例如 1600×900 桌面返回 640×360 图片，图片点 `(320,180)` 对应桌面点 `(800,450)`（原点为零时）。AT-SPI 节点已经使用桌面坐标，不要再缩放；改变分辨率后应重新观察。

显式选定安装新版 Agent 的 Linux 验收 VM，并设置 `VC_WORKSPACE_LIVE_PVE_ENDPOINT`、`VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE`、`VC_WORKSPACE_LIVE_COMPUTER_VMID` 和一次性 PostgreSQL 的 `VC_WORKSPACE_TEST_DATABASE_URL` 后，运行 `make pve-live-mcp-computer-check`。还需先只读核对该 VM 的新旧授权记录均为 revoked，设置 `VC_WORKSPACE_LIVE_COMPUTER_EPOCH_FLOOR` 为二者最大 epoch（均不存在时为 0）；可用 `TestLiveAgentSessionPreflight`，另设 `VC_WORKSPACE_LIVE_AGENT_SESSION_PREFLIGHT=true` 和同 VMID 的 `VC_WORKSPACE_LIVE_AGENT_SESSION_VMID`。测试再次核对权限、状态和版本，再仅向随机新 schema 写入测试起点，不删除或重置 Guest 栅栏；这不是生产库的自动恢复机制。

测试使用两个临时 Agent OS 账户和真正的 SDK HTTP 初始化/调用，结束或失败后关闭自己的租约，保留 Guest revoked 栅栏，再清理自己的账户/Home 与 schema；同时比较 xrdp 配置摘要和既有 Xorg PID，不重启服务或覆盖已有用户配置。不需要先登录共享 `vdi` 会话。每轮后的 epoch 会递增，下次需重新预检，不能重复使用旧 floor。

旧 Debian/Windows 共享会话验收仅是历史证据。Linux 新身份已通过真正 SDK MCP HTTP→控制面→PVE→Guest 的无人值守实测，包含图片/坐标、AT-SPI、输入、令牌轮换、MCP 服务重建、释放/重新领取和后台撤权。最新证据及 Windows、人工接管等未闭环边界统一见 [项目状态](../../docs/plan/status.md)。
