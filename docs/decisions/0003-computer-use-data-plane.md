# ADR 0003：Computer Use 使用 QGA 信令与交互会话 Helper

状态：已采用；Linux 默认链路已切换 V2，Windows 与显式人工接管待补齐  
日期：2026-09-05

## 背景

VC Workspace 需要让外部 AI Agent 通过 MCP 操作已分配的 Debian 13 或 Windows 11 桌面，同时保留现有 Agent 身份、独占 Lease、TTL、`control_epoch`、审计与人工接管边界。

[open-computer-use](https://github.com/e2b-dev/open-computer-use) 对观察—动作循环、暂停和人工介入有参考价值，但它的数据面依赖 E2B Desktop Sandbox，不能直接充当 PVE Guest 的桌面传输层。VC Workspace 复用它的工具粒度与人工介入思路，不引入 E2B 运行时依赖。

Windows 服务从 Vista 开始运行在非交互的 Session 0。微软要求需要界面交互的服务启动独立的用户会话进程，并通过受 ACL 保护的 IPC 通信；因此 SYSTEM Guest Agent 不能直接截图或向 RDP 桌面发输入。参见 [Microsoft Interactive Services](https://learn.microsoft.com/en-us/windows/win32/services/interactive-services) 与 [Window Station and Desktop Creation](https://learn.microsoft.com/en-us/windows/win32/winstation/window-station-and-desktop-creation)。

PVE 已经提供 QEMU Guest Agent 的受权限控制文件读写与命令执行端点，并将 `FileRead`、`FileWrite` 和 `Unrestricted` 拆分为独立权限；VC Workspace 可以继续使用已有的 PVE 信任关系，不需要给 Guest 新开管理端口。上游实现与 60 KiB 写入、16 MiB 读取边界见 [Proxmox QEMU Agent API](https://github.com/proxmox/qemu-server/blob/master/src/PVE/API2/Qemu/Agent.pm)。

## 第一协议版本与既有部署

以下记录历史协议，供理解旧部署和迁移使用；不是当前 HTTP/MCP 默认数据面：

1. MCP 只暴露 `desktop_screenshot`、`desktop_accessibility_snapshot`、`desktop_mouse`、`desktop_key` 和 `desktop_type_text`。Agent ID 始终来自认证上下文；每个工具必须提交 Lease 返回的当前 `control_epoch`。
2. 控制面再次检查桌面分配、Lease 状态、TTL 和 Epoch，为每次动作生成短期、单次 Request ID。超时范围固定为 250–15000 ms。
3. 请求正文通过 PVE QGA `file-write` 暂存到 Guest 的受限 spool，不放入进程参数。SYSTEM/root 只执行固定的 `computer-dispatch` 子命令：它原子发布暂存请求、等待响应、把响应写到标准输出并清理；不能接收 shell 片段。这样每个动作只需要一次文件写入和一次固定 Guest exec，重试沿用同一 Request ID 保持幂等。
4. `vdi` 用户登录后启动独立 `computer-helper`：Debian 由 XFCE autostart 启动，Windows 由隐藏、单实例的 Interactive Logon Scheduled Task 启动。截图和输入都发生在当前交互会话内；Helper 每 5 秒更新心跳，超过 15 秒的旧 `helper-ready` 文件不得视为在线会话。Host 与 Guest 必须保持时间同步，因为 Lease 和单次请求都使用绝对到期时间。
5. Guest 的 `authority.json` 是控制权的本地副本。Helper 在每次请求前验证 Lease ID、Epoch、状态和到期时间；长文本按小块重新验证，观察动作在返回数据前再次验证。人类原生客户端请求连接时，控制面先吊销活动 Agent Lease、递增 Epoch，再通过同桌面动作/接管互斥门等待在途动作退出，并无条件写入 revoked tombstone；即使上一次接管已改写数据库但 Guest 同步失败，重试也不能跳过该 tombstone。同步失败则拒绝签发人的连接凭据。
6. Helper 为每个动作启动独立 worker，最多运行至请求到期且不超过 15 秒；截图、系统可访问性 API 或输入库卡死时父 helper 强制终止 worker，后续请求仍可处理。动作到期时间不能晚于控制面的调用超时。
7. 截图按请求宽度下采样并编码为 JPEG，完整响应不得超过 QGA 16 MiB 读取上限。文本正文、截图数据和控件值不得进入审计；审计只保存动作类型、文本字节数、键鼠元数据、节点数、图像尺寸与 SHA-256 等元数据。Windows UIA 密码控件和 Linux AT-SPI 密码角色的名称也会被清空。
8. 第一协议版本的可访问性结果使用同一稳定节点结构。Debian 使用 AT-SPI 并返回 `source=linux_atspi`，Windows 使用 UI Automation 并返回 `source=windows_uia`；平台服务不可用时才降级为只包含顶层窗口的 `source=window_enumeration`。

## 被否决的替代方案

- 直接让 SYSTEM/root Guest Agent 操作桌面：Windows Session 0 无法正确访问用户的 RDP 桌面，也扩大高权限进程的攻击面。
- 只使用 PVE VNC/noVNC：它适合诊断，但在 GPU passthrough/mdev 场景可能没有目标显示输出，也不能提供 Guest 内可访问性语义。
- 让 MCP 控制 macOS 客户端内部的 FreeRDP 实例：这会把服务端 Agent 能力绑定到一台有人值守的 Mac，不能满足 Kubernetes 部署和专属 OS Agent。
- 在 Guest 开 HTTP/gRPC 管理端口：会增加证书分发、Guest 防火墙和跨网络暴露面；MVP 已有 QGA 信道足够承载低频 Computer Use 动作。

## 结果与限制

- 每个 Guest 模板必须同时包含系统级 readiness Agent 和用户会话 helper；旧模板不具备 Computer Use 能力，必须重建或升级。
- QGA 是低频控制信令，不是连续视频协议。实时观看仍由 RDP 数据面负责；弱网连续控制和跨网络 Gateway 继续由 NET-01 跟踪。
- 同桌面动作、租约和人工接管门使用 PostgreSQL advisory lock，并为 Guest authority 吊销增加事务性队列与 revision 确认。多个副本的正常请求在控制面串行；每次动作重新同步，不信任进程内缓存。双 Store/HTTP 副本的阻塞、取消、撤权后丢弃观察结果和失败重试已通过自动回归。锁释放不代表 QGA 结果丢失后的旧 Guest 进程已经结束：Linux 默认链路与 Windows 实验路径另有 Guest 单调发布栅栏，025 迁移提供 VM 级持久化 epoch，见下文。这不等于 Job/Builder/Kubernetes 全部具备多副本生产能力。
- 顶层窗口降级结果只能辅助定位，不能替代控件级语义。2026-09-04 的 AI-01 实机验收已从 Debian AT-SPI 与 Windows UI Automation 真实交互会话返回控件树，并在两端完成截图、输入、人工接管、旧 epoch 拒绝和脱敏审计任务。

## 每用户会话纠偏

上述第 4 项的 `vdi` 启动方式是旧协议的已知缺陷，不是新模板应继续采用的目标。后续实现必须同时满足以下设计约束；未通过前 REVIEW-04 不得标为完成：

- Agent 的 Guest 用户由控制面根据 Agent 主体派生并独立持久化，与人的 `vcw…` Home 和登录会话分开。MCP 输入不能自行提供 Guest 用户、系统 Session ID 或 Helper 路径。
- Helper 身份来自实际 UID/SID 和 OS 交互会话，请求同时绑定用户、会话和 Helper 实例；过期心跳、多会话歧义、旧实例或未匹配用户均拒绝，不回退到 `vdi` 或前台窗口。
- authority 和输入队列由 root/SYSTEM 管理，Helper 只读；响应/心跳按目标用户隔离。路径与权限校验须覆盖软链接、重解析点及伪造跨用户响应，不能只检查 JSON 中的用户名。
- 无人值守的 Linux Bootstrap 使用 xrdp 自带的 [xrdp-sesrun](https://manpages.debian.org/trixie/xrdp/xrdp-sesrun.8.en.html) 建立真正的 sesman/Xorg 会话，已在 Debian 13 验收机通过；密码走 stdin/文件描述符，登录成功后再次随机轮换，不进入 argv、数据库或审计。Windows 必须由服务端会话组件建立并保持真实交互登录，不能用 SYSTEM Session 0、全局自动登录或“先在某台 Mac 人工登录”代替最终 Bootstrap。
- 人工观察/接管 AI 会话应是显式操作，与进入自己的个人桌面分开；接管前吊销 Agent authority，不能靠换到另一个 OS 用户的空桌面冒充接管同一工作现场。

## Linux V2 会话传输

Rust Guest 的 `computer-v2-*` 与 Go `SessionExecutor` 已接入 HTTP/MCP 默认动作路径。当前仅支持已升级 Guest Agent、启用 managed-local Linux 身份配置的桌面；Windows V2 明确返回不可用，不回退到第一版共享 Helper。授权发布要求 Guest 和运行中的 Helper 均声明 `authority_transport=stdin_epoch_account_v2`，不支持旧新控制面混跑或仅替换二进制而保留旧 Helper 的无中断升级。

- `SessionTarget` 绑定受管用户名、内核 UID、实际 POSIX Session ID/本地 X11 Display，以及每次 Helper 启动新生成的 256 位实例 ID。只接受 `vca`/`vcw` 加 12 位小写十六进制的受管本地用户；不接受 `vdi`、系统用户、远程 Display 或客户端任意路径。
- root 在 `/var/lib/vc-workspace/computer-v2` 保存 authority 与私有输入。Helper 在每用户 `runtime` 内监听 Unix Socket；root 使用 `SO_PEERCRED` 验证对端 UID，Helper 只接受 UID 0 的调度请求。Helper 不产生供 root 读取的用户可写响应文件，从结构上避开旧响应目录的软链接/跨用户伪造问题。身份依据见 [Linux Unix Socket 文档](https://man7.org/linux/man-pages/man7/unix.7.html)。这不是对已取得 Guest root 权限者的隔离承诺。
- root 输入读取校验父目录归属与写权限，并使用 `O_NOFOLLOW` 拒绝文件链接。动作写入前由固定 `stage` 子命令新建 0600 暂存 inode，避免 QGA 默认新文件权限过宽及旧文件描述符改写新请求。请求原子发布后不可用同一 ID 改写；最终响应走有界 IPC/标准输出。Go 在完成或失败时清理精确请求文件，下一次控制区初始化清理到期/遗留输入；不承诺空闲期间定时清理的 SLA。共享 `authority-stage` 已明确拒绝，授权不再走 file-write。
- 动作 worker 通过 stdin 接收正文、独立进程组执行，250–15000 ms 到期后清理进程组。原请求期限另含最多 5 秒的 Go 传输宽限，总期限不超过 20 秒；重试保留同一不可变请求。Helper 的重放缓存只清理已到期条目，限制请求数及内存，结果丢失时拒绝重复执行输入。
- 同一账户第二个 Helper 被互斥锁拒绝。旧实例、旧 epoch 或目标不匹配不能执行，也不能重放已缓存截图；Helper 重启后必须重新发现目标并同步 authority，OS 应用状态可保留。

### Linux 授权发布与 VM 版本

- `computer-v2-authority` 从独立、有界的 QGA stdin 读取一份快照。Guest 使用持久 root-only `authority.lock` inode 的排他非阻塞锁，在同一临界区读取旧版本、比较、重新发现真实 Helper，再写私有随机暂存并原子发布为 `authority-account-fenced.json`。新版 Helper/worker 只读取这个文件，迟到的旧二进制写 `authority.json` 或 `authority-fenced.json` 不能覆盖新版授权。新文件首次创建时须核对两个历史文件，并先发布不低于任何历史版本的撤销，不删除历史来绕过升级。
- 旧 epoch 的 active/revoked 均拒绝；同 epoch 的 revoked 是终态。刷新 active 只允许相同 Lease/用户名/UID 且有效期不缩短，实例刷新仍需持锁重新认证。过期历史保留排序意义；损坏快照或不安全路径拒绝，不自动删除/reset。Lease ID 接受控制面 Base64URL 的 `-`/`_`，不接受路径或空白。
- 文件与发布目录执行 fsync；提交前重新检查有效期。进程中断释放锁，下次发布只回收 root 私有 inbox 内匹配自身随机暂存命名的安全文件，不删除锁 inode 或其他请求。控制面不自动重发授权：提交后返回值丢失仍属结果不确定，失败的 Bootstrap 关闭该精确 Lease，并把更高版本的撤销排队；不能继续用同一已关闭租约恢复激活。
- 025 增加 `desktop_computer_epochs`，按 VM 持续递增，不随历史 Lease 删除或桌面清单移除级联删除。数据库触发器统一分配新租约及失效版本，禁止关闭的租约重新 active。撤销队列记录自己的 closing epoch；旧清理即使晚于新租约领取，也不会使用新 active 的版本写 tombstone。数据库/快照回滚导致 Guest 版本更高时明确拒绝，尚无自动恢复或自动采信 Guest epoch 的生产机制。
- 迁移会撤销升级前的活动租约，排队清理旧 OS 会话。升级应先暂停 Agent 入口，释放旧租约并确认旧 Helper/交互进程退出，再停旧控制面、升级 Guest、启动新版控制面完成迁移及剩余清理。异常遗留存在时保持入口关闭，确认撤销队列与旧 Helper 清理完成后才重新领取；旧 Helper 不会因为新授权文件存在就自行升级。没有新栅栏而存在旧 V2 快照时，必须先发布不低于旧版本的 revoked，不能直接激活。

Linux 容器回归覆盖延迟旧 active/旧 revoke、发布者互斥、持锁进程被杀后的恢复、旧路径写入隔离、损坏旧记录/升级先撤销，以及原有双 UID 图形和 Helper 重启矩阵。PVE MCP 验收的临时数据库必须显式核对已撤销的 Guest epoch，测试起点只写入本次新 schema，不删除 Guest 栅栏；该测试操作不能作为业务数据库丢失后的恢复接口。

## Windows V2 身份、传输与执行基础

新增 `crates/windows-session` 收敛 Win32 FFI；Guest 主 crate 继续使用 `forbid(unsafe_code)`。该层已经实现与测试以下基础接口，但尚未接入 Windows 默认 MCP 动作：

- Windows 目标使用完整 SID、WTS Session ID、Token Authentication LUID 和 Helper 实例；不能把 SID 最后一段 RID 当作 Linux UID。协议用 `uid=0`、非 SYSTEM 的账号 SID 与 `windows:<session>:<16位登录LUID>`，拒绝 Session 0、系统/内置管理员 SID、混合 Linux/Windows 身份和不规范的别名。Go Linux Executor 继续拒绝 Windows 目标。
- SYSTEM 使用 OS 计算机名限定本地账号查找，再枚举真实 WTS 用户 Token；匹配完整 SID，多个匹配或查询失败均拒绝，不选择当前前台/控制台用户。`computer-v2-inspect --guest-user <受管用户名>` 仅提供 SYSTEM 只读诊断，不创建或接管账号；只有真实管道握手返回与当前 SID/WTS 一致的目标时才报告 Helper 就绪。[WTSQueryUserToken](https://learn.microsoft.com/en-us/windows/win32/api/wtsapi32/nf-wtsapi32-wtsqueryusertoken) 要求 LocalSystem/TCB 权限，并要求调用方关闭 Token 句柄。
- 本机 Named Pipe 以受管账号和会话编号命名，使用显式受保护 DACL、`FIRST_PIPE_INSTANCE` 和远端客户端拒绝。调用端不获得可创建其他管道实例的通用写权限；没有默认 Everyone/Anonymous ACE。微软说明了默认权限和 `FILE_GENERIC_WRITE` 隐含实例创建权限的风险，见 [Named Pipe Security](https://learn.microsoft.com/en-us/windows/win32/ipc/named-pipe-security-and-access-rights)。
- SYSTEM 客户端按内核报告的服务进程 Token 检查 SID/Session/LUID；Helper 仅通过 identification-only 客户端 Token 验证对端，并在解析业务数据前恢复线程身份，恢复失败立即终止。身份识别不向用户 Helper 授予 SYSTEM impersonation 权限，参见 [ImpersonateNamedPipeClient](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-impersonatenamedpipeclient)。
- 管道使用 overlapped I/O 和整次交换期限；超时通过 `CancelIoEx` 取消，并等待完成后才释放 Win32 正在使用的缓冲区/事件。不会调用可能无限等待对端读取的 `FlushFileBuffers`。
- 持久监听接口在请求间保留同一个管道实例，避免关闭/重建时被第二个 Helper 抢占；每次交换重新认证并设置 I/O 期限，认证失败、读写超时或回调展开都会断开该客户端。清理失败的监听器不再接受请求。业务回调仍须使用有期限的 worker；返回响应后必须有客户端读取完成确认，因为 [DisconnectNamedPipe](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-disconnectnamedpipe) 会丢弃未读数据。该接口尚未等同于已安装的每用户 V2 Helper。

Windows 授权快照使用 64 位机器注册表 `HKLM\SOFTWARE\VCWorkspace.ComputerV2\<受管用户名>`，不再计划复用共享文件 spool：

- 父键仅 SYSTEM 可访问；每用户键由 SYSTEM 拥有，在创建时即设置受保护 DACL，只有 SYSTEM 完全控制、目标完整 SID 只读。每次打开重新检查 owner、精确 ACE、SID 元数据及值类型，拒绝被放宽的既存权限，不自动“修复并信任”；打开链接本体并拒绝注册表链接。Helper 直接打开自己的叶键，不需要枚举父键。访问掩码依据 [Registry Key Security and Access Rights](https://learn.microsoft.com/en-us/windows/win32/sysinfo/registry-key-security-and-access-rights)。
- `Authority` 为单个非空、有界 64 KiB 的 `REG_BINARY` 值，整值替换，不分段修改 JSON。读写竞争有界重试，超大值和错误类型拒绝。初始化只建立 SID 绑定，缺失授权始终拒绝，不把已撤销的授权重新激活。写入/枚举要求 LocalSystem Session 0，且拒绝正在模拟任何身份的线程。这仍以 Guest SYSTEM/管理员可信为前提，不承诺机器断电时未刷新写入的持久性。
- 固定 Guest 命令 `computer-v2-init --guest-user` 核对本地账号 SID；`computer-v2-authority` 仅从有界 QGA stdin 接收严格版本化的绑定快照，不从 argv 或可写目录接收内容。活动授权核对 SID、实际 WTS 登录实例、Lease/Epoch 和有效期格式，并通过认证管道再次发现 Helper，拒绝旧实例；撤销对已登记目标写 tombstone。该记录不是 OS 账号归属账本，不允许根据它接管已有同名账号；独立账号账本见下文，默认 HTTP 接线仍未完成。
- 原生回归使用随机测试键，并使用受限令牌验证内核确实允许目标 SID 只读、拒绝其他 SID 和写入；这不等同于两个真实登录用户的桌面隔离验收。普通 Windows CI 账号验证非 SYSTEM 拒绝分支，完整机器注册表分支由显式的 PVE SYSTEM 验收覆盖。测试清理按 `OPEN_LINK` 取得精确键的句柄后调用 [NtDeleteKey](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/wdm/nf-wdm-zwdeletekey)，不递归删除、不跟随链接目标。

- 同一用户/桌面下的 worker 先以 suspended 状态创建，成功加入私有 Job Object 后才恢复执行；加入失败直接终止，不降级成无约束进程。Job 最多 16 个活动进程，不允许 breakaway，启用 `KILL_ON_JOB_CLOSE`。正常退出也清理派生进程；执行期限为 1–15000 ms，另有最多 2 秒的进程树收敛检查。这是受信任动作 worker 的生命周期边界，不是抵御 Guest 管理员、任意代码或经 WMI 等外部服务启动进程的沙箱；也不会结束桌面上原本独立的应用。相关边界见 [Windows Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects)。
- worker 只继承显式列出的 stdin/stdout/stderr，不继承 Job、Token 或其他偶然设为可继承的句柄。使用明确本地 `.exe` 路径及 Windows 参数转义，不查 PATH、不调用 shell 或批处理回退。stdin 最多 64 KiB，必须采用长度定界协议而不等待 EOF；父端保持打开直到 worker 退出，避免 Named Pipe 提前关闭丢弃未读数据。stdout 最多 16 MiB，stderr 丢弃以免动作正文进入 QGA/日志；可取消的标准流与执行共享期限。句柄列表契约见 [UpdateProcThreadAttribute](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute)。

Job 执行器已替换 Windows 旧 `computer-helper` 只杀直接子进程的逻辑；这没有修复旧共享授权文件的安全边界，也不是重新开放旧数据面的理由。新 V2 Guest 实验路径已接入 `computer-v2-helper/session/dispatch/worker`：

- Helper 必须以目标受管本地用户的真实交互 Token 运行，启动时检查注册表 SID/ACL 并生成新实例 ID。它可以在 revoked 状态等待授权，但不能执行动作。SYSTEM Session 0 不能运行图形 Helper/worker；Helper 只接受完整匹配 LocalSystem SID、Session 0 和固定 LSA LUID `0x3e7` 的调度端，该 LUID 见 [LsaGetLogonSessionData](https://learn.microsoft.com/en-us/windows/win32/api/ntsecapi/nf-ntsecapi-lsagetlogonsessiondata)。
- 空闲等待与认证后的请求接收、动作响应使用不同的有界 I/O 阶段，避免空闲计时耗尽新请求预算。每次交换仅处理一个长度定界消息，返回后等待单字节接收确认；动作正文直接由 QGA stdin 经认证管道进入 Helper，不落入共享文件目录。
- JSON stdin 使用 ASCII 安全编码：非 ASCII 字符转换为等价 `\u` 转义，非 BMP 字符使用 UTF-16 代理对，按转义后的 64 KiB 传输长度限流。真实 PVE 在直接接收中文/emoji 的 `input-data` 时出现 `Wide character in subroutine entry`；[PVE 上游输入通道实现](https://lists.proxmox.com/pipermail/pve-devel/2020-February/041977.html) 可见该文本在服务端被 Base64 编码。此处理保留 Guest 解码后的原文和重放字节一致性；通用原始 stdin API 不隐式修改内容。
- Helper 在执行前预留 Request ID 和内容摘要，重试不重复键鼠输入；缓存结果在返回前也重新授权。worker 在同一用户的私有 Job 中运行，从长度定界 stdin 读一条请求，执行中和返回前检查完整 authority；正文、额外尾随数据、错误 Request ID 和过大输出不能作为成功结果接受。执行期间撤权会丢弃结果且保留重放预留，SYSTEM 调度端返回 QGA 前再检查一次授权。
- 登录 Token 不等于桌面已经可操作。实例发现和图形 worker 还要求当前进程位于 `WinSta0\\Default`，且当前线程桌面的 `UOI_IO` 表示正在接收输入；尚未就绪或安全桌面期间拒绝动作，不切换桌面、不获取安全 UI 权限。此检查依据 [GetUserObjectInformationW](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-getuserobjectinformationw)，完整锁屏/UAC/断线矩阵仍须实测。
- Windows UIA 对单个元素批量读取有限属性，而非逐属性跨进程调用或请求无界子树；待访问队列也受节点上限约束，重复 Runtime ID 截断。每次动作获取新快照，继续隐藏密码控件名称、不读取 ValuePattern。批量读取契约见 [UI Automation caching](https://learn.microsoft.com/en-us/windows/win32/winauto/uiauto-cachingforclients)。
- 鼠标位置使用 `SetPhysicalCursorPos`，与 UIA 矩形和截图的原生桌面坐标一致，不把物理像素交给受线程 DPI 虚拟化影响的 `SetCursorPos`。这不修改用户缩放设置；实际高 DPI、多显示器和混合缩放仍需要行为验收。坐标约定见 [UIA screen scaling](https://learn.microsoft.com/en-us/windows/win32/winauto/uiauto-screenscaling)。
- 文本按最多 32 个 Unicode 字符分块，每块重新核验 authority 和正常输入桌面，再用 `KEYEVENTF_UNICODE` 一次提交成对的按下/释放事件；代理对不跨调用。不解析快捷键语法、不使用剪贴板或 UIA ValuePattern，修饰键仍被按住时拒绝，不擅自释放用户按键。部分插入返回失败且保留原动作重放记录，不重新输入。[SendInput](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendinput) 只确认事件插入输入流，不证明应用已处理完文本；夹具通过最多四次新只读 UIA 快照、连续两次精确内容确认应用结果，错误后缀/重复文本立即失败，并保留失败截图。各读动作仍受原期限约束；此观察不执行第二次输入，也不将窗口枚举兜底当作完整 UIA。
- 快捷键与文本编码分离：只允许协议中的虚拟键和最多四个唯一修饰键，以一个有界批次发送按下、主键按下/释放及逆序修饰键释放，不再通过工作线程的 `VkKeyScanW` 或字符串快捷键解析。发送前检查按键状态；部分插入只补本次已插入前缀中尚未释放的按键，不重发主键动作，仍返回失败。[KEYBDINPUT](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-keybdinput) 的虚拟键与 Unicode 标志不可混用；方向/导航键保留扩展键标志。夹具额外只读报告实际选区和键盘事件，`Ctrl+A` 必须确认完整 UTF-16 选区后才发送替换文本。
- Linux/Windows V2 撤权快照统一使用 `expires_unix_ms=0`，不把控制面当前时钟当作“已经过期”的证明；否则 Guest 时钟略慢就可能拒绝撤权。`control_epoch` 和无目标 tombstone 语义不变，仍拒绝旧代撤权覆盖较新授权；活动授权继续检查真实截止时间，没有放宽时钟或期限。
- Windows PowerShell 的 WinForms 夹具显式使用多行 TextBox 并关闭旧的 `DoNotSupportSelectAllShortcutInMultilineTextBox` 兼容开关，让框架内置处理 `Ctrl+A`。这是[微软记录的应用兼容行为](https://learn.microsoft.com/en-us/dotnet/core/compatibility/fx-core#donotsupportselectallshortcutinmultilinetextbox-compatibility-switch-not-supported)，不是 Guest 注入策略：不自定义全选事件、不直接设置被测文本或选区。实机非交互控件对照仅证明测试控件支持该命令，不替代随后真实用户桌面的输入、选区和替换断言。
- V2 的 QGA stdout 使用 ASCII-only JSON，把中文和非 BMP 字符编码为标准 JSON Unicode 转义；不对接收字符串做猜测式乱码修复。内部管道和防重放摘要保持原协议，转义后的响应仍受尺寸上限约束。

Guest 接线包含 026 的持久 SID、独立账号生命周期及已有 WTS 登录的 Helper 启动/停止，但不等于安装/服务恢复、无人值守登录或默认控制面接线。`computer-v2-capabilities` 报告 `experimental_session_transport=named_pipe_sid`、`authority_store=protected_registry`，默认能力仍是 `computer_actions=false`、`unattended_login=false`、`session_transport=unavailable`；控制面继续拒绝 Windows，每用户 Linux 链路不受影响。逐版本实机证据与剩余工作只在 [状态页 REVIEW-04](../plan/status.md) 维护。

Windows 实验路径的授权发布现在使用 VM 级单调栅栏，而非逐用户独立写值：

- `AuthorityUpdate` 把 SYSTEM-only 根键中的 `Fence` 和所有已登记用户的 `Authority` 置于同一个注册表事务。活动目标得到完整授权，其他用户得到同 epoch 的 revoked；VM 撤销对全部用户发布 revoked。先以 `UpdateLock` 值取得事务写意图，再读取旧 Fence，避免两个发布者依据同一旧值提交。每个键都先按不跟随链接的方式打开并验证 ACL/SID，再用空子键将同一个已验证对象加入事务；不会重新按可能变动的字符串路径寻找对象。API 约定见 [RegOpenKeyTransactedW](https://learn.microsoft.com/en-us/windows/win32/api/winreg/nf-winreg-regopenkeytransactedw)。
- 旧 epoch 的 active/revoked 均拒绝；同 epoch 的 revoked 是终态，不能再次 active。同 epoch 的 active 只允许相同 Lease、用户名和 SID 且有效期不缩短；Helper 实例变化必须在持有写意图期间重新通过真实登录身份和管道认证。过期的旧授权仍保留排序意义，不能作为空记录重置栅栏。提交前再次检查活动有效期。
- 若已有旧版逐用户授权但没有全局 Fence，必须先发布不早于每个旧记录的撤销才能迁入新模型；存在畸形记录时拒绝，不盲目重置或信任。只有没有旧授权的新登记区可以直接首次激活。Fence 不向普通用户开放读取权限；用户叶键权限保持不变。
- 事务只在全部写入完成后提交，重复或失败的 stage 不可提交；冲突、未提交的进程退出与 5 秒准备超时均回滚，不降级成无栅栏写入。创建和提交都核对 SYSTEM 且无线程模拟。超时和最后一个未提交事务句柄关闭的行为见 [CreateTransaction](https://learn.microsoft.com/en-us/windows/win32/api/ktmw32/nf-ktmw32-createtransaction)。这是 Guest 授权发布边界，不替代键鼠防重放，也不承诺 VM 快照回滚或断电后的未刷新数据持久性。

该栅栏的协议排序单测与 Windows 原生冲突/读可见性/回滚测试已通过，但默认 Windows MCP 仍关闭。完整控制面接线时必须复用 025 的 VM 持久化 epoch 与撤销队列 closing epoch，不能下发固定版本或把新租约版本用于旧清理。QGA 丢失返回值仍是结果不确定；未完成默认链路与真实用户验收前，不增加授权命令自动重发，也不把这批基础测试当作 Windows MCP 闭环。

`deploy/container/windows-session-test.Dockerfile` 提供一次性 FreeRDP/Xvfb 验收客户端，允许分别指定 Debian 普通和安全镜像源。实际 RDP 验收调用应通过 stdin 参数传递凭据、以 QGA 读取的 RDP 证书指纹固定服务端身份，不把密码放进 argv/环境变量。`make windows-session-client-check` 仅验证无外部网络、无宿主挂载下的启动和参数通道，使用公开的假测试密码；它不是 Windows 登录验收、生产无人值守登录组件或人的 macOS 客户端。内网构建参数为 `COMPUTER_TEST_DEBIAN_MIRROR` 和 `WINDOWS_SESSION_TEST_DEBIAN_SECURITY_MIRROR`。

`make pve-live-windows-helper-check` 是独立的真实交互验收入口，要求明确的 PVE endpoint/credential file/VMID、Windows amd64 `VC_WORKSPACE_LIVE_WINDOWS_HELPER_BINARY` 与本机私网 `VC_WORKSPACE_LIVE_WINDOWS_HELPER_ARTIFACT_BIND=<IP>:0`。指定 `VC_WORKSPACE_LIVE_WINDOWS_SESSION_WORKER=<绝对路径>` 时使用下述原生后台组件，在任何 VM 写操作前将可执行文件冻结到私有临时目录并记录 SHA-256，两个用户不会受本地重编译影响；未指定时保留已构建的一次性 Docker/Xvfb 客户端作为历史对照。验收 VM 必须最初停止且没有 V2 注册或 Agent 账号账本；两个限时标准用户使用真实 Agent provision/enable/disable，测试仅通过 stdin 生成并轮换临时 RDP 密码。私有目录保存不可变 SID 回执，只有测试输入框应用使用登录任务；Helper 由 SYSTEM 调用实际 `helper-start/stop`，不再使用代启动 Helper 的计划任务，也不替换安装中的 Guest。RDP 验证实际服务证书 SHA-256；下载源限制精确路径和 Guest 源地址，并核对文件长度/摘要。测试覆盖没有真实登录时拒绝、重复启动、等待期间实例不抖动、截图/UIA/输入/防重放、两个顺序用户、旧停止请求拒绝和撤权注销；入口存在不代表整套断言已经通过，结果以状态页为准。

### 后台 RDP 传输组件

`internal/computer.WindowsSessionExecutor` 是独立的 Windows V2 数据面实现，不负责创建账号或 RDP 登录。初始化先读取 SYSTEM 账号账本证明完整 SID；发现要求可交互的 SID/WTS/LUID/Helper 实例，授权始终绑定完整目标。成功动作验证操作对应的互斥响应类型，保留协议明示的 `window_enumeration` 降级来源；探测、派发和最多三次原字节重取共用原动作截止时间，不因丢失 QGA 回执更换动作目标或延长有效期。只有 capabilities/account-inspect/session 三个固定只读命令可在 QGA 回执丢失时限次重读；初始化、账号/授权写入不重放。错误提供固定阶段和退出码，不包含 Guest 原始输出。可执行路径由部署代码固定，不是 HTTP/MCP 参数。实机夹具仅将它改到本轮冻结的私有 Agent 产物，正常截图/UIA/输入与授权经过同一实现；错误身份、冲突重放和撤权后的负向请求仍直接到达 Guest，以免只验证 Go 拦截。默认 Linux 执行器及 HTTP 的 Windows 拒绝门保持不变，直到账号/Lease/进程所有权与安装恢复完成整体验收。

固定只读命令和具有防重放协议的动作，将单次 QGA 回执观察限制为最多 5 秒，仍服从调用方更短的期限。仅在实际传输不确定或该次观察超时、原请求仍有效时，才能进行下一次只读获取或原字节重取；总计最多三次。原 Guest 动作、Worker、授权和请求 ID 的期限不改变，调用方取消后不再派发，明确非零回执不重试。单次等待超时不代表 Guest 未执行输入，不能另起请求来补发。

[QGA 的 `guest-exec-status` 实现](https://raw.githubusercontent.com/qemu/qemu/master/qga/commands.c)在返回完成结果时移除进程记录，不能将其当作永久可重复读取的结果存储。`PID … does not exist` 或传输超时无法区分未执行、已执行但回执丢失等情况；当前实机未确认这些传输故障的根因，不能仅据该错误声称 QGA 服务崩溃。结果重取依赖 Helper 自己的身份绑定与防重放缓存，不是对任意 Guest 命令的幂等承诺。

`make pve-live-windows-expiry-check` 复用同一隔离 fixture 和显式参数，要求原生后台 RDP Worker；仅执行两个真实 SID/WTS 登录、第一用户显式停用和第二用户保持 RDP 时本地到期回收，并独立检查 SAM 禁用及 WTS 缺失。它不执行输入/截图/UIA/人工辅助，不是 MCP、正常桌面首登或全套交互验收的替代；账号、任务、账本和暂存资源仍须逐一确认清理并恢复关机。

`apps/session-worker` 的职责仅是保留真实的 Windows 交互登录连接，图形观察和动作仍由绑定 SID/WTS/LUID/实例的 Guest Helper 执行。它不是人的 macOS 客户端，也不替代身份、租约和撤销机制。

- 固定的 `VCW1` 长度定界 stdin 帧，正文最多 1024 字节，只接收一份配置：RFC1918 IPv4、端口、精确 `vca` 受管账号、机器域、临时密码、证书 SHA-256、有限分辨率与绝对到期时间。无命令行选项、交互认证或环境凭据，不接受任意 Shell/路径/续期指令。
- NLA 认证、固定证书摘要；摘要不匹配时即使系统 CA 信任也拒绝，不使用 TOFU 或证书忽略回退。不接受服务端重定向或 Gateway 地址。剪贴板、设备、文件、音频和动态显示通道关闭；不加载客户端插件或动作脚本。Linux 镜像不构建可见客户端、X11/Wayland、Server 或虚拟设备通道。
- `internal/rdpsession` 仅在协商和首个非空解码帧的两个回执都到达后返回就绪；这仍不等于输入桌面/Helper 已就绪，后者必须另行发现。错误只回传固定类型和数字阶段码，不转发服务端文案、凭据或库诊断。
- Go context、父进程管道、墙上时钟和单调时钟共同约束生命周期，最长 8 小时；管道 EOF、额外字节、到期均终止，不自动重连或续权。Go 等待子进程退出，超时定点终止并回收；取消在连接初始化之前发生时，原生监管仍持续发出中止，避免内部重置丢掉取消信号。断开 RDP 不等于 WTS 注销，账号撤权与本地到期回收仍由 Guest 承担。
- WinPR 初始化需要实际用户 Home 路径，因此只原样传递该路径，不传任意调用方环境；XDG 和 FreeRDP 缓存/配置/Home 设置隔离到每连接 `0700` 临时目录。正常结束清理目录，Broker 被强制终止时可能留下目录，应由部署的临时卷生命周期处理；不把文件系统隔离描述为抵御同 UID 或管理员的沙箱。
- 本地 `make session-worker-check` 需要 CMake、pkg-config 和 FreeRDP/WinPR ≥3.31.0。`VC_WORKSPACE_TEST_SESSION_WORKER=<绝对路径> VC_WORKSPACE_TEST_SESSION_WORKER_BIND=<本机私网 IPv4> go test -race ./internal/rdpsession` 额外运行真实原生证书拒绝、握手中断和期限测试；测试端只监听本机地址，不登录任何真实 OS。`auto` 绑定仅用于隔离容器内选择本机私网接口。
- `make session-worker-container-check` 构建 `deploy/container/session-worker.Dockerfile` 并运行内部专用 Docker 网络、非 root、只读根文件系统、无 capability/端口暴露的回归；也可用 `bash apps/session-worker/test-container.sh <已构建本地镜像>` 单独测试。软件源通过 Docker build 的 `DEBIAN_MIRROR`/`DEBIAN_SECURITY_MIRROR` 传入，FreeRDP 源码固定版本与摘要。现有控制面镜像和 Kubernetes 清单没有默认启用它；多副本所有权、账号/Lease 期限关联、恢复与默认 MCP 接线仍须整体验收，状态只在 REVIEW-04 维护。

验收脚本使用 SYSTEM-only 命名互斥量串行化同一标记的操作。QGA 结果丢失不能证明命令没有执行，账号创建、授权写入和清理不自动重发。已取得 PID 后，QGA 暂时不可用时只在原调用期限内继续 GET 查询同一 PID，不重新 POST 启动命令。仅已绑定 SID/会话/Helper 实例、具有防重放预留的 V2 动作允许在原截止时间内最多三次原字节重取，不生成新 ID、不延长期限、不绑定新实例；明确的退出码拒绝不重试。清理验证标记、ACL、账号归属和任务定义后，只注销对应 SID 并移除本次账号/Profile/任务/注册表/暂存资源，再恢复 VM 停止状态。若结果不确定，保留日志中的精确标记供 `TestLiveWindowsHelperFixtureRecovery` 显式恢复；恢复先在同一互斥量下只读核对剩余资源，已经消失则确认没有同标记账号/任务，不重复清理，不能递归清理其他验收或业务资源。

新账号的 Windows 首次登录体验可能遮挡测试应用，即使 `WinSta0\\Default` 已接收输入，也不能把应用的 Shown 事件或控件树中出现输入框等同于前台可操作。验收必须确认输入框真正获得前台焦点，失败时只保留这个隔离测试桌面的当时截图用于诊断。微软的用户级 [DisablePrivacyExperience](https://learn.microsoft.com/en-us/windows/client-management/mdm/policy-csp-privacy#disableprivacyexperience) 策略已在临时新建 Profile 中尝试，但未消除当前镜像的跨境数据传输确认页；未奏效的实验代码和 Profile 已清理，不采用为部署方案。

部署方明确授权后，可设置仅测试使用的 `VC_WORKSPACE_LIVE_WINDOWS_HELPER_DESKTOP_ASSIST=true` 辅助完成本轮临时用户的初始化：在日志中的私有目录查看当前截图，再提交绑定该帧 SHA-256 的一次性 `click`、`refresh` 或 `continue` 指令。点击限制在图像边界、60 秒内的新帧和当前 SID/会话/Helper 授权内，每段辅助最多 5 分钟/20 帧；元数据提供采集和点击截止时间，过期帧必须 refresh 后重新查看，无任意键盘或 shell 接口。辅助发生在测试窗口 Shown 就绪检查之前，以便发现阻挡首次登录的界面；Shell 也可能弹出开始菜单或应用。辅助结束后仍须通过 Shown、真实前台焦点和输入断言，不因点击成功就视为整套验收通过。仅私有验收 Lease 给每用户 12 分钟，整轮两用户含清理最多 30 分钟；单次动作/Worker/重放期限不变。此开关默认关闭，不是生产无人值守初始化方案，也不伪造 Consent 状态或改变系统地区/全局隐私策略。

基础库在 Windows 上运行 `cargo test --locked -p vc-workspace-windows-session -- --test-threads=1`；Mac 可用 `--target x86_64-pc-windows-gnu --release --no-run` 构建测试可执行文件。标为 ignored 的 fixture 仅由正式测试显式选择，不应统一执行 `--include-ignored`，其中有会主动退出进程的专用夹具。设置明确的 PVE endpoint/credential file/VMID 和 `VC_WORKSPACE_LIVE_WINDOWS_SESSION_TEST_BINARY` 后，`make pve-live-windows-session-check` 将它放入 SYSTEM/Administrators 私有临时目录，校验摘要再执行；结束后删除精确测试文件，并把原本停止的 VM 正常关机。上传用固定偏移、有界 QGA 重试、逐块长度/摘要检查和最终全文件校验，不重复追加文件；测试日志只记录摘要、长度与断言，不输出二进制内容。

`make pve-live-windows-accounts-check` 使用相同参数，额外 opt-in 执行 SYSTEM 本地账号生命周期测试。它只创建已证明不存在的随机 `vca…` 账号和随机注册表测试键，不登录、不生成 Profile、不替换现有 Agent。普通测试不触发账号写入；该入口同时比对测试前后的 SAM 用户与归属键清单。失败清理拒绝未知 SID、归属变化或已有交互会话，保留精确名称供独立核验，不能按 `vca` 或注册表前缀批量删除。

同一 PVE 节点的 Windows 验收必须串行。启动前读取节点资源，扣除 Guest 配置内存后至少保留节点内存的 10% 或 2 GiB（取较大者）；数据缺失或余量不足则拒绝开机。该快照不是并发资源预留，外部启动仍可能使其失效。原生回执读取单次最多 30 秒，观察超时仅在原两分钟预算内重读同一份回执，不重启套件；权限、格式或明确退出错误不作为网络重试处理。

底层原生实机测试可选 `VC_WORKSPACE_LIVE_WINDOWS_SESSION_ARTIFACT_BIND=<本机私网IPv4>:0`，自动提供仅限当前 Guest 地址访问的精确 `session-tests.exe` 下载路径，并在测试清理时关闭服务；Guest 地址每轮从 QGA 读取，不复用旧 DHCP 地址。也可改用 `VC_WORKSPACE_LIVE_WINDOWS_SESSION_INSTALL_URL=http://<私有IP>:<端口>/<临时目录>/` 的外部临时源，两者不能同时指定，本地测试二进制参数始终必需。此模式只减少构建产物传输的 QGA 往返，不绕过身份或摘要验证；禁止重定向/代理、未知或超限长度，有单次读取与总下载期限。外部临时服务须自行关闭；这不是生产模板更新或 Guest 软件分发协议。QGA 丢失 PID 结果仅对固定偏移的幂等测试块重试，不能据此重发无会话绑定及防重放预留的真实输入动作。

原生套件仅启动一次，在本次私有目录保存输出和完成回执；QGA 返回值不确定时只在两分钟内读取同一份回执，不重新运行测试。日志读取受 512 KiB 上限、明确退出码、UTF-8 和 Base64 校验约束。清理再次检查 SYSTEM/Administrators ACL、精确文件白名单及可执行文件是否仍在使用；不递归删除、不终止未知进程。失败遗留可用 `TestLiveWindowsNativeFixtureRecovery` 恢复：显式提供 `VC_WORKSPACE_LIVE_WINDOWS_SESSION_CLEANUP_MARKER`、原产物的 `VC_WORKSPACE_LIVE_WINDOWS_SESSION_CLEANUP_SHA256` 和原 VM 参数，要求 VM 原本停止，验证摘要与所有权后只清理该目录，随后恢复停止。它只报告遗留注册表测试键名称，不按前缀删除；未知键需另行核验归属。

控制面的通用 PVE Guest 执行器必须取得明确的正常 `exitcode`，不能将缺失值当成 0。[QGA 的 GuestExecStatus](https://www.qemu.org/docs/master/interop/qemu-ga-ref.html#object-guestexecstatus) 在 Linux 信号或 Windows 异常退出时使用 `signal`；捕获输出也可能带截断标记。这些结果现在均作为失败返回，不自动重新执行命令，不把残缺 stdout 交给上层当作完成回执。PVE 的布尔标记兼容 JSON 布尔或 0/1，其余形式拒绝。Windows 原生验收额外使用两个只返回固定退出值的进程，检查普通非零退出及异常式退出；不制造内存故障或弹出 WER。交互夹具清理还必须返回明确完成标记，并另起只读检查确认精确账号、登记键及暂存资源不存在。

Linux 的等价探针为 `TestLiveLinuxGuestExecCompletion`，需设置 `VC_WORKSPACE_LIVE_GUEST_EXEC_STATUS_AUDIT=true`、`VC_WORKSPACE_LIVE_DESKTOP_VMID`、PVE 连接参数及 `VC_WORKSPACE_LIVE_PVE_MUTATIONS_ENABLED=true`（guest-exec 属于 POST）。它只运行固定输出、非零退出和仅终止自己的子进程，不写文件/账号/配置、不改变 VM 电源或已有桌面进程。交互夹具恢复若已经丢失 manifest，必须另给 `VC_WORKSPACE_LIVE_WINDOWS_HELPER_CLEANUP_USERS=<用户名1>,<用户名2>`；此时只检查这两个精确身份已经不存在，不按 Agent 名称前缀猜测归属或删除资源。

## Agent OS 账号生命周期

### Windows 已登录会话的 Helper 启动

`computer-v2-helper-start --guest-user <vca用户名>` 仅允许非模拟状态的 SYSTEM 调用。它持有账号生命周期门，核对独立账本、完整 SID 和启用状态，再解析唯一实际 WTS 登录实例；无登录、Session 0、身份变化或并发停用不使用前台用户兜底。以当前 Agent 的固定绝对路径、固定子命令和 `WinSta0\Default` 启动，不接受任意可执行文件或 shell 参数。使用 [CreateProcessAsUserW](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-createprocessasuserw) 创建挂起进程，不继承父进程句柄；用户环境来自 [CreateEnvironmentBlock](https://learn.microsoft.com/en-us/windows/win32/api/userenv/nf-userenv-createenvironmentblock) 且不继承 SYSTEM 进程环境。这里复用 RDP 已加载的 Profile/桌面，不修改桌面 ACL，不把新建 Token 或后台进程当作已登录桌面。

挂起进程先进入私有 kill-on-close Job，复核其 SID/WTS/LUID 后才运行；握手还必须匹配被持有的实际进程句柄对应 PID，不能接受同一用户下的其他管道服务。失败/放弃时仅终止本次进程树，先回滚再释放账号门；验证应用声明后才解除启动回滚，使 Helper 在短命 QGA 命令结束后保留。已有 Helper 通过认证可重复取得同一实例；常规动作继续使用独立有界 Worker Job。

声明中的 `input_ready` 与 Helper 存活分开：首次登录尚未完成或锁屏时可保留同一个 Helper，但 `computer-v2-session`、授权发现和实际动作仍拒绝不可交互桌面。`computer-v2-helper-stop` 接受 stdin 完整目标，只允许 SYSTEM 通过认证管道停止匹配实例；旧请求不能关闭重启后的新实例。停止不自动给替代实例续权，仍须显式发布授权。此能力不负责无人值守 RDP 登录、自动解锁、安装器/服务恢复或默认 Windows MCP 接线；整体退出条件见唯一状态页。

### 持久账号与租约

- 024 迁移记录每 VM/Agent 的稳定 `vca…` 用户、Lease、generation、UID 和交互实例；026 增加不可变平台与完整 SID，并将账号身份与临时会话分开。Linux 使用 UID，Windows 使用完整 SID、`uid=0`，不将 RID 转换为 UID。[Microsoft SID 说明](https://learn.microsoft.com/en-us/windows-server/identity/ad-ds/manage/understand-security-identifiers)明确区分账号名称与不可复用的 SID。首次已认证发现绑定账号；重新领取、Helper 重启和 WTS 登录实例变化可更新 Session/Instance，但不能替换或清空既有 UID/SID。数据库检查、唯一索引及触发器阻止两个 Agent 共用同一 VM 的账号，也阻止同名重建账号或平台变化被自动接管。未知/被旧版本清空的 UID 不凭用户名猜测，只能由后续已认证发现首次补录。
- 数据库登记发生在 Guest 操作之前；释放、撤分配、停用、到期以及中途失败/取消都会保留撤销任务，队列派发与完成确认核对当前 generation，拒绝旧快照。这是数据库边界，不能取消已经发出而仍在 Guest 执行的旧账号命令。发现身份与固定账号不符时，不发布 authority，关闭当前 Lease 并排队清理。撤权成功只清空临时 Session/Instance，保留账号身份和 Home；此持久化不是 Windows 账号创建、无人值守登录或 Windows 默认 MCP 接线完成的证据。
- 026 保留原 Linux 活跃记录及 epoch，不修改已发布迁移；迁移发现重复 UID/SID 等冲突时失败，不自动挑选归属。升级前应排空旧控制面副本再执行迁移并启动新版：旧版清理逻辑会尝试清空 UID，被新保护拒绝；当前不宣称混合版本滚动升级已验收。Guest OS 重装或账号被外部删除后，不自动重绑到新身份，应先停用桌面并按显式恢复流程处理。
- root 使用私有账号归属记录核对 UID。首次 `useradd` 前保存随机归属标记以恢复中断创建；已有同名、同形状但没有归属记录的本地用户必须拒绝，不能改密或接管。账号 Home 的初始化写入降权到对应 UID 执行。
- Windows 账号使用独立的 `HKLM\SOFTWARE\VCWorkspace.AgentAccountsV2\<vca用户名>`，SYSTEM 独占写入/读取，账号本人也不可读，不复用授权快照推断归属。固定账号命令拒绝域控制器及带模拟令牌的调用；以仅 SYSTEM 可访问的首实例 Named Pipe 串行化同一用户的生命周期，已有实例导致拒绝而非绕过。创建前刷新随机 Pending 归属记录，使用 [NetUserAdd](https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netuseradd) 生成随机密码的禁用本地标准账号，创建中断仅在受保护随机标记和禁用状态匹配时补录完整 SID。SAM 查询使用 [USER_INFO_4](https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/ns-lmaccess-user_info_4)，在同一快照取得完整 SID、状态和 Unix 秒到期值，不读取密码字段；已绑定后删除或同名重建均不得自动接管。Users/Remote Desktop Users 按内置组 SID 解析本地化名称，只加入标准桌面组，不赋予管理员权限。
- Windows `computer-v2-account-provision/enable/disable/inspect` 已接入 Guest；enable 的有界 JSON stdin 仅接受 `expires_unix_seconds`，要求未来不超过 8 小时，凭据轮换由后续控制面登录流程负责。provision 不重新启用既有账号，disable 保留 SID/资料；创建前失败可幂等清理而不创建账号，已删除 SAM 用户仍按记录 SID 清理遗留登录。WTS 观察/注销只排除 Session 0 和监听端点，对其他状态读取实际令牌，复核 SID/WTS/LUID 后异步注销；[WTS 状态定义](https://learn.microsoft.com/en-us/windows/win32/api/wtsapi32/ne-wtsapi32-wts_connectstate_class)中的正在重置、连接中或 shadow 状态不能单独证明用户已退出。同一完整登录身份只成功派发一次，后续只读观察，登录实例改变则重新验证后派发，最多记录 1024 个实例。持账号门最多等待 30 秒，超时保留禁用状态并返回错误，交由后续本地巡检重试；不重置动作期限、不强杀其他用户，不将访问拒绝当作会话不存在。[WTSLogoffSession](https://learn.microsoft.com/en-us/windows/win32/api/wtsapi32/nf-wtsapi32-wtslogoffsession) 的异步返回只确认请求受理，不代替后续收敛检查。交互验收的账号命令等待 45 秒，覆盖这段注销期限和进程启动/收尾；该夹具期限不适用于输入动作。
- `account-inspect` 的结构化回执使用 `schema_version=1` / `observation_version=1`，携带已登记的 `username`、完整 `sid`、`exists`、`disabled`、`expires_unix_seconds` 和 `sessions`。SAM 已删除时仍保留账本 SID，状态/到期字段明确为 `null`；WTS 列表仍可能非空，每项为 `windows:<非零会话编号>:<16 位小写十六进制 LUID>`。未知/未绑定、同名换 SID、不安全账本、读取中 SAM 变化和权限错误均拒绝，不创建、修复或收养账号。控制面只接受预先已绑定的 SID，拒绝旧协议、缺失/null 会话列表、不一致字段和重复 WTS 编号；只有明确禁用或已删除、且实际会话列表为空的有效回执，才能报告本次观察没有可登录账号/保留会话。零值和读取失败不算注销成功，也不靠英文 stderr 推断。
- 上述观察只是一个时间点的 SAM/WTS 状态，不是完整 Lease 撤销确认。Windows 新增下节的持久账号 epoch，`account-inspect` 在 `lifecycle` 中返回其完整非秘密记录；无版本的历史记录为 `null`。控制面拒绝损坏/错配的生命周期记录；版本化账号只有 `revoked` 且 SAM 禁用/删除、WTS 无登录时，才能报告当次关闭观察。Linux 默认 Agent 登录/停用已接入下节的版本化协议；数据库锁仍不能取消已发出的 QGA 进程。跨端还须关联受监管 RDP 进程实际退出、持久 Broker 所有权和升级时旧进程排空，不以一次观察替代整个撤权链。此项跨平台缺口由状态页 REVIEW-12 跟踪，Windows Broker 整体由 REVIEW-04 跟踪；默认 Windows MCP 不开放。
- Windows SAM 到期本身不终止现有桌面。新版 SYSTEM Guest 主循环在每次心跳前运行本地到期检查（默认 15 秒间隔），`computer-v2-accounts-reconcile` 提供同一检查的显式入口。只枚举受保护账号账本，再持单账号门重读 SID/状态/到期值；到期、已禁用或不再有有界期限的已绑定账号才执行停用/WTS 收敛，不删除账号或 Home，不启用、不改密、不创建登录。丢失 SAM 身份仅清理账本旧 SID；同名重建、未绑定或不安全记录拒绝，不按前缀接管。单记录错误不阻止其他已证明身份的账号检查；失败下轮重试。Helper 启动同样拒绝到期或无限期账号。此机制依赖新版 SYSTEM 进程持续运行，不承诺在 Guest 挂起、时钟异常或 WTS 长时间故障时即时退出；安装/服务恢复、真实到期注销与默认控制面接线的验收状态见状态页。
- Linux 新账号先锁定并过期，应用 Guest 权限策略后才通过一次性随机密码启用并启动 XFCE Helper。已就绪的动作不重复配置 xrdp 或上传背景；策略修改与动作共用桌面锁。API 总期限为 65 秒，MCP 内部客户端为 70 秒，worker 仍独立受 250–15000 ms 限制；Computer Use 响应最多 16 MiB，普通 API 保留 1 MiB 上限。
- 首次 Bootstrap 和撤权都会同时封锁 `/var/lib/vc-vdi/computer` 与 `/var/lib/vc-workspace/computer` 的旧授权。升级使用逐级 `O_NOFOLLOW` 目录描述符验证，收回旧用户所有的控制目录再原子写入拒绝文件，不能依据新可执行文件存在就跳过旧目录。Debian 新模板不再安装共享 `vdi` Helper。
- 后台先写 V2 拒绝授权，再锁定/过期账号并终止该 UID 的交互进程，最后以 generation/revision 确认并记录系统审计。Home 保留用于后续领取；网络不可达时任务保留重试，不能宣称 OS 已退出。此边界不隔离已获 Guest root/local_admin 权限的主体。
- 当前 Native 连接仍进入人的个人账号并撤销 Agent，不等于“观察或接管 Agent 同一工作现场”；该交互与 Windows 无人值守会话由状态页继续跟踪。

### 版本化账号登录

`crates/guest-lifecycle` 定义不含 OS 调用的严格协议与状态机。Linux/Windows 固定命令 `computer-v2-account-lease --guest-user <vca用户名>` 只接受最多 4096 字节的 JSON stdin：`schema_version=1`、已绑定的 `identity { username, uid, sid }`、`lease_id`、正数 `control_epoch` 与 `login_generation`、`expires_unix_seconds`、`operation`，以及仅 `open` 必需的临时 `password`。Linux 使用固定本地 UID 和空 SID，Windows 使用 UID 0 和完整本地 SID。接口不创建/重新绑定身份，不接受任意程序、路径或域。能力声明报告实验 `lease_epoch_login_generation_v1`；缺少 generation 的早期实验请求/记录拒绝，不默认为新登录。默认 Linux Agent 路径要求该协议；Windows 自动选择和无人值守能力仍关闭。

- `open` 要求更高 epoch；或在同一仍存活的 `open/sealed` Lease 内使用严格更高的登录 generation，且到期时间完全不变。时间必须为未来且不超过 8 小时；过期、`opening/sealing` 或 `revoked` 不能靠增加 generation 复活。账号门内先持久化 `opening`，停用并清除旧会话/进程，再安装 stdin 凭据和启用，核验后提交 `open`。相同 epoch/generation 不重放，不重复安装旧密码。Windows 使用本地 [NetUserSetInfo 1003](https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netusersetinfo)。
- `seal` 仅接受同一身份、Lease、epoch、generation 和不变到期时间的 `open`：先持久化 `sealing`，把引导密码换成 Guest 本地产生且不返回的随机值，保留已登录桌面，最后提交 `sealed`。同一已 sealed 请求只核验并返回现有状态，不重复改密；不能以 seal 续期或重新启用。
- `revoke` 到期字段必须为 0，先持久化终态再停用和回收。低 epoch、同 epoch 的低 generation 或不同 Lease 拒绝；重复同一撤销在账号门内重新检查收敛。撤销对整个 epoch 终结，增加登录 generation 也不能重新打开，新租约必须使用更高 epoch。本地巡检把遗留 `opening/sealing`、到期或 OS 状态不一致先转为同版本 `revoked`，再关闭账号；时钟推进不清除历史。

Linux 默认路径使用同一协议的 `computer-v2-account-login`，仅接受新的 `open` 意图：在同一账号门内完成凭据安装、固定参数的 `xrdp-sesrun` Xorg 登录和密码退役，最后只返回精确 `sealed` 回执。密码仅走 stdin；账号写入子进程和 sesrun 客户端继承门，登录客户端最多等待 20 秒且受原租约期限约束。固定 exec 包装先设置内核父进程死亡信号，再核对原父 PID，随后只执行无 setuid/文件 capabilities 的系统 `xrdp-sesrun`；调度进程被杀不能留下无限持锁的登录客户端。客户端退出仍不代表服务端没有接受登录，不能重发凭据或回退用户名式启停。

新版 Linux 要求 `login_birth_fence=pam_logind_jobs_v3`：root 所有的 xrdp PAM 配置必须包含完整 v2 前置策略，且固定路径的原生模块必须为安全的 root 文件；默认 Go 执行端拒绝旧/未安装能力。`pam_vcworkspace.so` 运行于真实 `xrdp-sesexec` PAM 进程，通过固定子命令 `computer-v2-pam-domain` 与 Rust Guest 通信，不读取或传递密码。Rust 核对 root 父进程、可执行文件、SO_PEERCRED 与账号版本，在创建者门内持久登记 boot/PID/启动时刻；C 桥接随后以父进程自己的 pidfd 调用 logind `CreateSessionWithPIDFD`，不回退数字 PID。Rust 独立核对 systemd `GetUnitByPIDFD`、logind 用户/Leader/Service、内核 cgroup 和 scope InvocationID，再持久提交 scope 的设备/inode 身份并确认。`open_session` 必须复核同一登录版本和 scope，不能经无密码 UDS 绕过登记。非 Agent 用户继续执行原 PAM 栈。登录流程参考 [xrdp 会话架构](https://github.com/neutrinolabs/xrdp/wiki/SessionManagementArchitecture)、[xrdp PAM 适配器](https://github.com/neutrinolabs/xrdp/blob/v0.10.1/sesman/libsesman/verify_user_pam.c) 与 [systemd 257 logind 接口](https://github.com/systemd/systemd/blob/v257/src/login/logind-dbus.c)。

账号写入按“账号门→创建者门”排序；等待认证时只释放后者，封存密码前重新取得并确认唯一的当前会话创建者。撤权先锁定 shadow，再逐级 NOFOLLOW 打开已绑定的 cgroup v2 目录，核对同一 boot、设备/inode，通过固定目录 FD 打开 `cgroup.kill` 清除整个域并等待 `populated=0`，然后核验 root pidfd 退出，最后清除 UID 进程。不会发送可能迟到并解析为新会话的命名 KillUnit/TerminateUser 任务；同名目录被替换时拒绝，不杀替代对象。root 可执行文件只在登记时验证，回收器不读取其他 root 进程的 `/proc/PID/exe`，也不新增 `CAP_SYS_PTRACE`。`login_writers_absent` 同时考虑创建者与其 scope，即使父进程已经退出仍保留未清空的 scope。控制面须同时确认 UID 与登录域均为空才确认关闭。PAM 记录使用独立临时命名空间，避免账号门恢复误删在途登记。

v3 还要求当前 boot 的登录记录对应用户已从 logind 消失，并且 `user-UID.slice`、`user@UID.service`、`user-runtime-dir@UID.service` 均无待执行 Job 且为 inactive/failed（未加载也可）。只接受精确的 `NoSuchUser`/`NoSuchUnit`，D-Bus 超时、拒绝或未知错误不能解释为不存在；检查单元后再次读取 logind。原因是 [CreateSession 的回执会等待用户启动任务](https://github.com/systemd/systemd/blob/v257/src/login/logind-session-dbus.c)，父 PAM 进程退出不等于 root 用户管理器启动任务已经终止。撤权最多等待 15 秒让正常用户停止延迟收敛；未收敛则保留终态意图、拒绝确认关闭/重新登录，由正式 timer 重试。这是只读核验，不新增可能迟到的命名停止任务；v3 仍使用原生 v2 PAM ABI/策略及 schema 2 记录。

这项保护限定于新版受管 xrdp Agent 路径，不是任意 SSH/其他登录服务、Guest 管理员或旧版本进程的全局隔离。模板通过 `deploy/guest/linux/install-login-fence.py` 安装固定策略并保留原 PAM 栈；已有 Guest 必须先进入维护状态、排空旧客户端/创建者再升级。真实延迟认证、调度进程死亡、新旧版本切换及其他登录入口的覆盖状态只在 REVIEW-12 跟踪；上述组件不能把认证、OS 进程创建与输入注入变成原子事务。

不能把 sesexec 本体退出等同整个登录进程树已经关闭。[xrdp 的 `fork_child` 和会话启动实现](https://github.com/neutrinolabs/xrdp/blob/v0.10.1/sesman/sesexec/session.c) 先 fork，再在子进程中设置用户环境。真实 root fork/setuid 夹具已在隔离 Guest 复现：父 pidfd 退出且 UID 暂时无进程后，孤儿 root 子进程仍能迟到进入该 UID。这个旧版 v1 关闭判据的反例驱动上述 v2 接线；夹具并非修改 xrdp 的 fork 指令，当前端到端验收范围以状态页为准。

独立 scope 机制试验另使用随机且本轮不复用的 scope 名执行 `KillUnit(all, SIGKILL)`，核对 `cgroup.events` 与 systemd 的内核关闭日志；对照账号和相同 UID 的新 scope 均保留。依据 [systemd 257 scope 接口](https://github.com/systemd/systemd/blob/v257/src/core/dbus-scope.c)、[内核 cgroup.kill](https://docs.kernel.org/admin-guide/cgroup-v2.html#core-interface-files) 和 [systemd 的 SIGKILL 路径](https://github.com/systemd/systemd/blob/v257/src/core/unit.c)，内核关闭覆盖域内的并发 fork。该独立夹具仅证明机制；正式实现使用上面的固定目录 FD，不依赖 scope 名永久不复用。

原生模块以 `pam_set_data` 将 logind FIFO 引用保存在真实 PAM 调用进程中，在 close/end 时关闭，并设置 XDG_SESSION_ID/RUNTIME_DIR/TYPE/CLASS/DESKTOP；不禁用正常 `pam_systemd`。这是因为其 [SessionBusy 路径](https://github.com/systemd/systemd/blob/v257/src/login/pam_systemd.c) 不重新填充环境，而 [FIFO 最后一个写端关闭](https://github.com/systemd/systemd/blob/v257/src/login/logind-session.c) 会启动会话结束。一次性 Rust helper 不持有会话引用，并受内核父进程死亡信号约束。新版 journal schema 为 2：未提交 scope 的记录仅表示 Rust 未收到并确认登录域，不能据此推断 logind 尚未创建用户或排入启动任务；旧 schema 1 缺少登录域信息，拒绝静默读取/升级。现有安装须先用原版本收敛并排空旧进程，保留账本证据，再显式维护 PAM/产物；安装器不会自动迁移旧策略。丢失回执且用户管理器任务仍在运行的隔离实机反例已由 v3 修复并验证。另已实际暂停 logind，监视真实 PAM 创建请求排队；原调用者超时退出后，恢复 daemon 并捕获到对应消息序号的错误回复，而非由控制面补发创建请求。已有桌面也已通过 logind SIGKILL/自动恢复后的同 UID/Session/Helper 与 sealed 版本检查。上游 [pidfd 身份解析](https://github.com/systemd/systemd/blob/v257/src/basic/pidref.c) 与 [创建入口](https://github.com/systemd/systemd/blob/v257/src/login/logind-dbus.c) 供核对；在途 logind 重启、PID 1/systemd 异常和旧进程升级仍须分别验证；正常电源周期的窄范围证据见下一段，不能以正常桌面或单个认证暂停切片关闭 REVIEW-12。

登录账本的 boot ID 还限定旧 PID/scope 的适用范围：内核已换 boot 时，不按旧 PID 或同名 cgroup 操作新进程；旧创建者不存在不代表账号已撤销，仍须核对精确生命周期、shadow 与当前 UID 进程。隔离 Debian 已通过正常关机期间租约到期、正式开机 timer 自动回收、原 UID/Home 保留和新租约恢复实测，无人工 reconcile。该证据不涵盖断电未刷新数据、快照回滚、未到期租约、旧二进制排空或升级；运维入口见 [冷启动验收](../operations/pve-development.md#关机期间到期与开机自动回收)。

027 迁移新增数据库 `login_generation`，零表示尚未预留版本化登录。升级关闭旧活动租约并排队回收，保留原 UID/SID 和 epoch；数据库迁移本身不等于旧 Guest 进程已经排空。控制面先独立观察 Guest 固定账号并入库，再在短事务内以原 Lease/账号 generation/login generation/UID/SID 做 CAS，递增一次登录版本并清空旧会话目标，然后才能派发凭据。`ready` 必须匹配预绑定身份和已预留版本，不能从成功 Helper 回执首次推断身份。健康会话直接复用；发现会话失败且独立 UID 已无进程时，控制面可在 15 秒及原请求/租约期限内只读等待旧登录域和用户后台任务收敛；每次观察仍须匹配原 sealed 身份/epoch/generation/期限，未知或变化即拒绝。确认全部为空后才允许在同一未过期租约内预留下一次登录，租约和期限不变。Bootstrap 回执丢失关闭精确版本并排队清理，旧版本确认/清理不能影响新版本；未确认关闭的账号不重新领取。

Windows 记录位于现有 SYSTEM-only 账号键的单个 `Lifecycle` 值中，不包含密码，意图与完成分别 `RegFlushKey`；注册表和 SAM 不是一个事务。失败时尽力立即锁定登录，并保留可恢复状态，不回退 epoch；不会在一次失败中额外重复一轮 30 秒 WTS 注销。Windows Helper 启动拒绝未提交/过期/与 SAM 不符的生命周期。两端新版旧式 enable/disable 对任何已存在生命周期记录的账号都拒绝，provision 不重新启用；损坏记录不能触发旧协议回退。

Linux 使用 root 私有 0600 的 `users/<vca>/account.json` 固定 UID，`account-lifecycle.json` 保存非秘密意图/完成状态。生命周期记录现以 root 所有、单链接、0644 发布，允许无特权 Helper 读取同一份原子状态；其中只有固定身份、Lease 标识、版本、期限和阶段，没有密码、动作或观察内容，Lease 标识本身不是认证凭据。身份账本、账号门和 provision marker 仍为 0600。所有记录拒绝符号链接，随机临时文件 fsync→rename→目录 fsync；不是维护第二份可能失步的授权副本。每账号 `account.lock` 的 inode 不删除或替换，所有新版账号写入在同一排他 flock 内。实际 useradd/usermod/chpasswd/pkill 写入子进程继承锁的同一 open-file description，防止调度进程死亡后留下失去锁保护的旧写入；不会把锁传给不可信用户程序或 Helper。新版回收不派发可能晚于下一次登录执行的异步 logind terminate-user 任务，而是在门内同步关闭 shadow 登录、按已核验唯一 UID 终止进程并复查。删除账号后仍保留 UID，UID 被其他用户复用或共享时拒绝操作，不自动重新绑定。`account-inspect` 返回固定身份、实际登录开关/到期日、进程缺席观察和完整生命周期。

向 `vca` 发布输入授权时，root 还须持账号门核验固定 UID、实际 shadow 状态、精确 Lease/epoch/期限和 `sealed`；从账本取得 `login_generation` 写入授权快照，调用方不能自行提供该字段。Helper/worker 在动作、缓存重放、平台分段检查和响应返回前，读取同一生命周期记录并核对授权中的版本。未 sealed、缺失/损坏、错身份/租约/版本或到期一律拒绝；账号写入 pending/revoked 后即使进程尚未退出，后续校验也拒绝旧输入和缓存观察。旧 0600 生命周期可供 root 回收，但不能静默获得新输入授权，须先撤销再重新登录。检查与 OS 注入不是不可分割的事务，不宣称已经注入的事件可撤回或具有零延迟的撤权 SLA。

Linux shadow 到期字段只有“天”精度，设置为窗口终点向上取整的天；不能将其宣称为秒级 OS 登录期限。正式 Debian 配方使用 [`deploy/guest/linux`](../../deploy/guest/linux/) 的三个唯一来源单元：只写状态目录的 readiness 服务显式传入 `--readiness-only`；独立 root oneshot 仅运行固定 `computer-v2-accounts-reconcile`，由 timer 离线巡检。timer 在启动 5 秒后、上次执行结束 10 秒后触发，精度窗口设为 1 秒；失败后继续调度，不因启动频率上限永久停用。单轮启动上限 45 秒、终止宽限 5 秒，`KillMode=control-group` 约束工作进程及其账号写入子进程。readiness 自身崩溃由 `Restart=always` 恢复，不阻塞独立回收。定时器并非实时调度，Guest 挂起、时钟异常和 OS 故障仍影响收敛，不能承诺即时撤权。参见 [systemd.timer](https://manpages.debian.org/trixie/systemd/systemd.timer.5.en.html)。

账号工具需要创建相邻锁/备份并原子替换 shadow，单独把 `/etc/shadow` 绑定成可写不足以支持该协议。固定离线 worker 获得 `/etc`、私有控制目录、`/sys/fs/cgroup/user.slice` 及共享 `/tmp` 写权限，保留 `ProtectSystem=strict`、`ProtectControlGroups=true`、只读 Home、私有设备与 `NoNewPrivileges`；其他 cgroup 路径和系统服务、SSH、PAM、sudo、平台配置仍只读。共享 `/tmp` 是清理已关闭 UID 的 Xorg 节点所需，不能再设置 `PrivateTmp=true`；readiness 服务仍保留私有临时目录。cgroup 的创建/删除仍由 systemd 管理，代码只写经过身份复核的 `cgroup.kill`。能力集仍仅 CHOWN/DAC_OVERRIDE/FOWNER/KILL，不提供 SYS_ADMIN/SYS_PTRACE，地址族仅 AF_UNIX。该单元不是“只能写单个 shadow/scope/socket”的文件系统沙箱，也不防御 Guest root 管理员。权限语义参见 [systemd.exec](https://manpages.debian.org/trixie/systemd/systemd.exec.5.en.html)。

撤权先锁定账号，持 account/birth 门复核所有 scope，再向精确 pidfd 发送 TERM；所有创建者合计最多获得两秒正常退出时间。[xrdp 0.10.1 的会话退出逻辑](https://github.com/neutrinolabs/xrdp/blob/v0.10.1/sesman/sesexec/session.c#L782)会在窗口管理器结束后通知 Xorg/chansrv 退出。该机会结束后仍无条件检查并强制排空原 cgroup，父进程死亡不等于孤儿已清理。UID、登录域及用户管理器任务全部关闭后，才清理该固定 UID 拥有的规范 X11 锁/socket 名称；持有 root/sticky 目录 FD、限制枚举和节点数，拒绝混合归属、链接、异常类型和仍存活的锁 PID，删除前核对设备/inode/属性。socket-only 中断残留也可在该 UID 已关闭时恢复。未知对象保留并返回失败，不用全局 `rm` 或用户名/PID 模糊匹配；清理失败不得继续签发。实机证据与仍未闭环的生命周期矩阵见状态页。

直接运行新版 Agent 而不指定 `--readiness-only` 时，仍保留每轮 heartbeat 调用同一本地回收器的兼容路径（默认 15 秒），但不具备上述独立服务的超时与权限边界。损坏账本保留现场，仅在 UID 可安全验证时尽力关闭，返回失败而非清理成功。已有安装不因替换 V2 文件就获得新服务；必须显式安装单元并处理旧进程。隔离 PVE 服务验收入口见 [运维说明](../operations/pve-development.md#linux-guest-服务验收)，逐轮结果只记在状态页；新模板实建、升级排空与异步 sesman 登录恢复仍分别验收。

Go Linux 登录/撤销和 `ChangeWindowsAccountLease` 均只发送一次精确绑定写入，最大写入等待 45 秒且服从更短的调用方期限；回执必须匹配所请求身份、Lease、epoch、generation、到期和完成阶段。失败、空/旧/错配/未完成或带额外秘密字段的回执均不确认成功，也不重放写入。Linux 撤销另外独立观察精确 `revoked`、账号禁用、UID 无进程及已登记 root 创建者均退出后才确认；未知身份或缺失字段不能冒充完成。凭据只走 stdin；协议对象不实现凭据序列化/Debug，Rust 原始输入/UTF-16 缓冲与密码对象退出时清零，Go 清理其编码缓冲。这是尽力减少秘密驻留，不承诺清除解析器、运行时或 OS 内部所有副本。

原生账号套件在真实 SAM 上验证新/退役密码，并在实际意图写入和 SAM 修改后的检查点直接结束专用子进程，验证无 Rust 析构时的门释放、未提交状态和本地恢复。`make pve-live-windows-lease-check` 使用与 expiry-check 相同的显式 PVE/Guest/Worker 参数，但通过新接口打开固定五分钟账号窗口，实测 RDP/Helper、seal 后保留原桌面、显式 revoke 和不修改原到期时间的本地回收。它不包括默认 MCP、输入矩阵或可视 Mac 客户端。清理只接受 manifest 内的固定 SID、`lease_<本轮标记>` 与对应的 1/2 epoch；不按前缀删除，也不猜测其他租约。

版本化协议不能约束仍在执行的旧 Guest 二进制或任意 root/SYSTEM 管理员。部署升级必须排空旧控制面/旧账号 QGA 命令再切换版本。Linux Agent 已接入登录前身份绑定、持久登录版本、登录/撤销和 Helper 输入检查；Linux Native 默认代码也已改为下面的独立凭据协议，Windows 未绑定 Native 仍采用既有路径。同租约重连与全链路故障证据以状态页为准，不能用 Guest 局部通过代替端到端验收。安装恢复、旧进程排空、跨主体适配和默认 Windows Broker 接线未完成时，不宣称 REVIEW-12 已关闭。

Native 人工桌面不能直接复用 Agent 的“登录前清空旧桌面”语义：普通断开必须保留用户应用。028 已为其新增不可改绑身份、签发/凭据退役/账号撤销及单调版本的持久层；独立 Rust Native 协议、Linux shadow 执行端和单独的 Native PAM 模块已接入默认 Linux HTTP/QGA，详见 [Native 账号版本](../architecture/identity.md#native-账号写入版本)。签发确认与 Connection Session 同事务提交，撤权队列和后台恢复使用精确版本；两个独立 Store/HTTP 副本已通过真库并发与回执故障测试。另有隔离 Debian 真实 RDP/PAM、保留桌面重连、撤权和正式 timer 到期证据，但不等于默认 HTTP→macOS 全链路或 Windows 执行端已验收。强制回收后的 Xorg 文件节点使用上文同一固定 UID 清理规则，不在普通退役/有效重连时执行。未升级 Guest 和未知旧账号拒绝新签发，不能静默降级；默认服务未重新部署，生产升级仍须单独验收。

`make computer-session-check` 分别在两个无网络、无宿主挂载、带 init 回收孤儿进程的一次性容器运行 Linux 账号与图形套件，避免 UID 复用测试保留的故障账本污染后续 GUI 账号。账号套件检查真实 shadow、旧版本、UID 复用、门冲突、实际进程死亡/遗留写入和原期限后台回收；图形套件包含通用双 UID 协议矩阵，以及两个版本化 Agent 用户的真实 GTK 输入、缓存截图、密码退役和恢复。新增故障场景在实际 pending/revoked 写入后直接结束专用子进程，保留仍活着的 Helper/应用，验证不改全局授权也拒绝缓存观察与新输入，再通过本地回收与新租约恢复。检查点只在独立 Rust 测试二进制中，发布 CLI/导出产物不包含测试入口；容器结果不计 PVE xrdp、默认 MCP 或服务安装验收。

可重复的 Linux 图形隔离测试：

```sh
make computer-session-check
# 使用内网镜像；Mac 须先拉取与本机一致架构的 Rust 镜像。
make computer-session-check \
  COMPUTER_TEST_RUST_IMAGE=hub.infra.plz.ac/library/rust:1.88-bookworm \
  COMPUTER_TEST_DEBIAN_MIRROR=http://10.31.0.2/debian
```

该目标在一次性、无网络且不挂载宿主文件的容器中启动两个不同 UID 的 X11/DBus/GTK 会话，执行真实截图、AT-SPI、鼠标、文本输入、重放、实例重启、卡死恢复和身份/权限拒绝测试。CI 已加入独立 Job；本地证据与未完成的 PVE/Windows/人工接管边界统一记录在状态页。
