# PVE 开发环境

## 安全边界

- PVE 地址与凭证通过环境变量或仓库外文件提供。
- 默认只读；写操作必须显式设置 `VC_WORKSPACE_PVE_MUTATIONS_ENABLED=true`。
- 控制面写入口限定为桌面 Clone/Start/Stop、PCI Resource Mapping、允许列表中的模板构建和固定 Guest 权限策略，不提供 Delete UI，也不接受任意 Packer 路径、Guest 命令或用户名。
- 测试资源名称必须使用 `vc-workspace-mvp-` 前缀，并记录内部 Job、VMID 和 PVE UPID。

## 已验证环境

2026-09-01 已使用用户提供的环境完成验证：PVE 9.2.10、集群 `infra`、6 个在线节点、共享 `ceph-pve` RBD，并发现多份 Cloud-Init 模板。随后从模板 901 幂等克隆 VM 147 `vc-vdi-mvp-debian-001`，通过 Web/API 与 MCP 路径完成启动、停止，验收后保持停止状态。VM 158 `vc-vdi-mvp-desktop-003` 是 Debian 13 XFCE/xrdp 数据面验收机；验收时它运行在 `infra-node6`，控制面会从 PVE 集群清单动态定位所在节点，不绑定这一记录。源模板 901 实际是 Debian 12，因此 v6 seed 显式禁用云镜像自带的 deb822 bookworm 源，改用内网 trixie 源执行受控升级，再安装桌面。VM 153、157 是停止状态的早期安装试验，不参与调度；2026-09-02 的客户端冷启动测试确认 VM 153 的 PVE Agent 通道配置存在但来宾 Agent 不响应，等待被正常取消并通过 ACPI 恢复停止。以上 PVE 名称是在品牌迁移前创建的历史资源，为避免影响运行状态不就地改名；所有新资源使用 `vc-workspace-`。凭证内容未写入仓库。

VM 158 的数据面验收结果：Debian `trixie/13`、QEMU Guest Agent 与 xrdp 均为 active，`desktop-ready` 就绪标记可读，3389/TCP 正常监听。控制面签发一次性 RDP 描述符后，macOS 上的 FreeRDP 3.31.0 完成认证、TLS/RDP 协商、Metal 窗口创建与桌面图像帧接收；VM 侧 xrdp-sesman 确认 `vdi` 登录、Xorg `:10`、XFCE 窗口管理器和后续重连成功。这台机器用于验证完整数据面，不等同于正式 Packer 模板产物。

2026-09-04 在 VM 158 对 macOS Retina 动态显示完成最终实机回归：先把现有 XFCE 会话重置为 96 DPI，再由新版 Native Connection 请求自动同步为 192 DPI，证明不是依赖人工残留状态。默认窗口协商为 1640×972，macOS“缩放”最大化为 5120×2670，原生全屏为 5120×2796，退出全屏回到 5120×2670；每一步均通过 QEMU Guest Agent 进入现有 `vdi` 会话并用 `xrandr` 和 `xfconf-query` 独立读取，不以客户端截图尺寸代替。最大化与全屏后远程终端输入分别执行成功。xrdp 的 RandR 输出仍为 `0mm x 0mm`，与其尚未把 RDP `DesktopScaleFactor` 应用到 X server 的[上游问题](https://github.com/neutrinolabs/xrdp/issues/3473)一致，因此 Linux DPI 由受限的控制面请求同步。可用下列命令复核当前会话；`VC_WORKSPACE_LIVE_DESKTOP_EXPECTED_RESOLUTION` 可选，设置后会要求至少一个 XFCE 会话精确匹配：

```bash
VC_WORKSPACE_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
VC_WORKSPACE_LIVE_DESKTOP_VMID=158 \
VC_WORKSPACE_LIVE_DESKTOP_EXPECTED_RESOLUTION=1640x972 \
make pve-live-desktop-resolution-check
```

需要单独复核两档 Linux DPI 时，设置 `VC_WORKSPACE_LIVE_DESKTOP_DPI=96` 或 `192` 后运行 `make pve-live-desktop-dpi-check`；该命令会修改当前 `vdi` 会话，仅用于明确的验收机。

2026-09-02 使用正式 Packer 定义在 `infra-node6` 完成 VMID 9100 `vc-vdi-debian-13-xfce`，构建耗时 21 分 57 秒并成功转换为停止状态模板。完整克隆 VM 9101 `vc-vdi-mvp-debian13-template-check` 首启后获得 `10.31.0.177`，QEMU Guest Agent、`desktop-ready`、Debian 13 trixie、`startxfce4` 会话入口和 macOS FreeRDP 认证均通过；验收后保留为停止状态资源。

2026-09-02 使用正式 Windows Packer 定义在 `infra-node4` 完成 VMID 9110 `vc-vdi-windows-10-22h2`，构建耗时 29 分 21 秒并成功转换为停止状态模板。完整克隆 VM 9112 `vc-vdi-mvp-windows10-template-check` 首启后获得 `10.31.0.195`，确认为 Windows 10 Pro 22H2 build 19045；QEMU Guest Agent、Cloudbase 首启、VC Workspace Agent 启动任务、`desktop-ready`、TermService/3389、QGA 密码轮换和 macOS FreeRDP 认证均通过。验收后 VM 9112 正常关机并保留为停止状态资源。

2026-09-02 使用 Microsoft Windows 11 Enterprise 25H2 Evaluation 介质在 `infra-node4` 完成 VMID 9111 `vc-vdi-windows-11-25h2-eval`，构建耗时 1 小时 20 秒并成功转换为停止状态模板。完整克隆 VM 9113 `vc-vdi-mvp-windows11-template-check` 首启后获得 `10.31.0.199`，确认为 Windows 11 Enterprise Evaluation build 26200；Cloudbase-Init、QEMU Guest Agent、VC Workspace Agent、`desktop-ready`、TermService/3389、BitLocker 完全解密和 macOS FreeRDP 完整图形会话均通过。后续从签名 macOS 客户端做冷启动验收时，中国区介质在 `vdi` 首次登录仍出现一次隐私/跨境数据提示；完成提示后桌面、键鼠、双向剪贴板与全屏通过。构建配方已补设备级隐私体验禁用策略，下一版模板必须无人值守首登复验后才能替代当前技术模板。

当前 MCP Computer Use 使用独立 Agent OS 账号，不要求先从 Mac 登录共享 `vdi`。真实验收入口为 `make pve-live-mcp-computer-check`，只接受明确选择的 Linux 验收机、新版 Agent 和一次性 PostgreSQL 测试库；先核对 Guest 新旧授权记录都已撤销，并提供其最大 `control_epoch`，不删除栅栏或自行恢复生产库版本。该测试创建两个临时账号，覆盖 SDK HTTP、无人值守 xrdp、图片/坐标、AT-SPI、输入、释放/重新领取和后台撤权，结束后清理自己的账号/Home/schema。完整参数、升级次序与安全边界统一维护在 [MCP Server 验收说明](../../apps/mcp/README.md)，最新结果见 [项目状态](../plan/status.md)。

升级验收机前需要从当前源码构建 Linux amd64 Guest Agent，并检查 AT-SPI/DBus、EGL/GBM、PipeWire、Wayland/XCB 运行库；仅有模板就绪标记或 ELF 架构正确不代表运行依赖齐全。`TestLiveInstallAgentSessionBinary` 在隔离暂存中验证 SHA-256 和 `authority_transport=stdin_epoch_account_v2`，不再接受仅同名或同摘要但协议过期的缓存；替换已有产物需要显式匹配旧 SHA-256，保留旧文件且不替换旧服务、不重启 xrdp。二进制更换不代表旧 Helper 进程已升级。正式模板依赖由 `deploy/images/debian-13-xfce/configure-desktop.sh` 维护，旧验收机需要显式补齐，连接过程不自动安装软件。

2026-09-04 的共享 `vdi` 验收仅为历史证据：Debian 13 VM 158 与 Windows 11 VM 9113 曾通过直接 Guest 图片/AT-SPI/UIA/输入，以及旧 MCP 链路、Native 接管和脱敏审计。它不能证明当前每 Agent 身份模型或 Windows V2 已闭环；默认 Windows MCP 仍拒绝，禁止沿用旧共享会话测试绕过该限制。历史 VM 9113 验收后正常关机，VM 158 保留原会话。

2026-09-02 权限策略实机验收：运行中的 Debian 13 VM 158 通过 QEMU Guest Agent 依次加入 sudo 并验证 `sudo -n` 可提权，再移除 sudo 和受控 sudoers 文件并反向验证提权失败，最终保持标准用户。中文 Windows 11 VM 9113 暴露了内置组名称本地化问题，改为通过 SID `S-1-5-32-544` 读取实际组名后完成加入/移除；Windows 10 VM 9112 使用同一最终脚本再次通过，并以正常 shutdown 恢复停止状态。PVE 偶发的 `guest-exec-status` timeout 只在 60 秒执行窗口内重试；Guest Agent 未运行时不得绕过策略签发连接。

2026-09-05 会话策略实机验收：VM 158 先使用可恢复测试依次应用“剪贴板/驱动器允许”和“全部禁止”，独立核对 xrdp `[Channels]`、sesman 双向剪贴板限制、FUSE 磁盘挂载和服务状态，并恢复原配置。首次执行发现既有 Debian 13 的 `sesman.ini` 没有 `EnableFuseMount`；最终命令改为在现有 `[Chansrv]` 中插入缺失键，section 缺失则失败关闭。随后从真实 Web 管理端把最终策略经控制面和 QGA 下发为文本剪贴板允许、驱动器重定向禁止、受管背景开启；数据库 `desired_revision=applied_revision=3`，Guest 侧策略、背景资源、登录 helper 和 xrdp 服务由只读测试再次确认。该证据覆盖 Debian Guest 收敛，不代表 Windows 机器策略或 macOS 用户行为已经实测。

可对明确的非生产验收机重复运行；测试会改变 `vdi` 组成员并可能注销其桌面会话，结束时恢复原权限。Windows 测试会在目标原本关机时自动开机，并使用正常 shutdown 恢复：

```bash
VC_WORKSPACE_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
VC_WORKSPACE_LIVE_PRIVILEGE_VMID=158 \
make pve-live-linux-privilege-check

VC_WORKSPACE_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
VC_WORKSPACE_LIVE_PRIVILEGE_VMID=9112 \
make pve-live-windows-privilege-check
```

Linux 会话策略提供两条显式 opt-in 命令。第一条会重启 xrdp、切换 allow/deny 并恢复逐字节备份，只能对没有重要活动会话的验收机运行；第二条只读验证由 Web/API 下发后的默认组合，不修改 Guest：

```bash
VC_WORKSPACE_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
VC_WORKSPACE_LIVE_SESSION_POLICY_VMID=158 \
make pve-live-linux-session-policy-check

VC_WORKSPACE_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
VC_WORKSPACE_LIVE_SESSION_POLICY_VMID=158 \
make pve-live-linux-session-policy-verify
```

每用户 Guest 身份还有一条独立的破坏性 opt-in 验收。它只接受名称以 `vc_workspace_identity_live_` 开头的一次性数据库和明确指定的运行中 Linux 验收机；测试创建唯一 `vcw…` 账号，连续签发两个 Native Connection 并验证稳定映射、密码轮换、`ssl-cert` 组、旧连接自动撤销与重复 DELETE 幂等，最后删除临时 Guest 账号。不要对生产桌面运行：

```bash
VC_WORKSPACE_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
VC_WORKSPACE_LIVE_DATABASE_URL=postgres://user:password@127.0.0.1:5432/vc_workspace_identity_live_linux \
VC_WORKSPACE_LIVE_NATIVE_IDENTITY_VMID=158 \
make pve-live-native-identity-check
```

2026-09-05 在 VM 158 完成该链路：真实 QGA 创建稳定的每用户账号，两次描述符使用同一 Guest 用户但不同 Connection ID/密码，第二次签发先撤销第一次，首次与重复主动释放都返回 204，数据库进入 `revoked`，临时 Guest 用户随后删除。VM 保持原运行状态；一次性数据库在测试后销毁。该测试验证 Broker→PVE→Guest 的账号与凭据生命周期，不代替 macOS 可视 RDP 回归；macOS 数据面实测仍由上文既有 Debian/Windows 记录覆盖。

Guest 保留会话撤权有独立入口，使用 PostgreSQL 测试库中的随机 schema，不修改公共 schema。它只在指定 Linux 验收机创建两个临时账号和测试进程，不重启 xrdp、不修改已有账号；验证普通断开保留进程、撤分配/Native 注销后的账号锁定、xrdp PAM 拒绝、目标进程清理、对照账号与进程保持，以及重新启用。结束时删除测试账号与进程：

```bash
VC_WORKSPACE_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
VC_WORKSPACE_TEST_DATABASE_URL=postgres://user:password@127.0.0.1:5432/vcw_test \
VC_WORKSPACE_LIVE_GUEST_REVOCATION_VMID=158 \
make pve-live-guest-revocation-check
```

2026-09-05 在 Debian 13 VM 158 通过此项验收。PAM 检查直接调用 `xrdp-sesman` 服务的 account 栈；不能用 root 的 `runuser` 账号切换结果替代普通用户登录判断。该证据覆盖真实 API 释放/注销、撤销队列、QGA 与 Guest 进程/PAM，不等同 macOS 可视 RDP 或 Windows WTS 会话已验收。

## Linux Guest 服务验收

Debian 配方从 `deploy/guest/linux` 安装 readiness、账号回收 oneshot 与 timer，并运行 `install-login-fence.py` 安装 Guest 输出的固定 PAM 前置策略，不在多个安装器中复制正文。服务职责、权限和巡检边界见 [ADR 0003](../decisions/0003-computer-use-data-plane.md#版本化账号登录)。更新配方不代表现有模板或已运行的 Guest 已升级。

```sh
VC_WORKSPACE_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID=160 \
make pve-live-guest-service-check
```

必须显式选定运行中的受管 Debian 13 隔离机，名称以 `-check` 结尾；入口拒绝 VM 158、有活动 Xorg/Agent 用户、已有同名单元或 drop-in 的机器。需先安装含 `--readiness-only`、`computer-v2-pam-policy/domain` 的新版 amd64 Agent，以及同一次构建的 `/usr/local/lib/security/pam_vcworkspace.so`（root:root、0644、安全的父目录）。Docker `artifact`/`amd64-artifact` 同时导出二者；Packer 的 `pam_module` 和 Builder 的 `VC_WORKSPACE_LINUX_PAM_MODULE` 指向该文件，后者未指定时默认在 Linux Agent 旁查找 `pam_vcworkspace.so`。夹具临时启动三个源码单元，并调用同一安装器设置 xrdp PAM；已有 sesexec/Xorg 或不明 PAM 变更拒绝，不自动结束旧用户。它不启用开机链接、不重启 xrdp、不改数据库或宿主驱动；首次写入前在 `/run/vc-workspace-svc<随机标记>/manifest.json` 保存 root 私有恢复记录和原 PAM 栈。

测试先建立两个真实 xrdp/Helper 会话，分别验证 readiness SIGKILL 后重启及原始期限的自动回收、只读 `/etc` 故障后 timer 自行重试恢复。另用三个独立账号，在真实 PAM 登记后、`common-auth` 密码验证后及父进程已经退出三个场景暂停认证；PAM 故障脚本真实 fork 一个停在 setuid 前的 root 子进程，并核对所有进程属于已提交的登录 scope。只杀 Guest 调度进程时 SCP 子进程随之退出；第三个场景还精确终止 sesexec，确认 root 孤儿仍存活、登录域不能报告关闭。恢复 timer 后自动清除整个域，暂停进程不能迟到执行 setuid；每个新租约仍可复用原 UID。临时脚本只存在于测试恢复目录，不进入 Guest 发布 CLI/正式配方；信号绑定本轮 boot/启动时刻和 pidfd，清理与验收分开。这是实际 xrdp/PAM 路径内的 root 派生故障，不是修改 xrdp 的 fork 指令；未覆盖丢失 logind 回执、用户管理器启动任务和其他登录入口。

不会缩短账本期限或手动执行回收来代替定时器。结束时停止临时服务，按本轮固定 UID 清理账号/Home/控制记录和已登记故障进程，恢复原 readiness/PAM 和单元缺席状态，核对 xrdp/PAM 配置摘要。清理不确定或资源被外部修改时保留恢复记录并失败，先核对原标记和完整身份，再恢复，不按用户名或目录前缀批量删除。

其他 MCP 验收入口不会自动改 PAM。仅替换二进制或缺少原生模块时，`login_birth_fence` 为 `unavailable`，控制面拒绝新登录；必须显式维护并核对 `pam_logind_jobs_v3`。v3 保持原生 v2 PAM 策略/ABI 及 schema 2 记录，但新增 logind 用户与 systemd 用户启动任务的关闭核验；旧 `pam_logind_pidfd_v2` 不再满足默认控制面的能力要求。旧 `pam_pidfd_v1`、`computer-v2-pam-register` 和 schema 1 创建者记录不会静默升级：须先以旧版本撤销并排空，保留证据后维护策略/记录。备份 `/etc/pam.d/xrdp-sesman.vc-workspace-before-birth` 用于核验和恢复，不能在仍有新版本 Agent 会话时直接回退；完整升级/回退链路仍需验收。

### 关机期间到期与开机自动回收

使用同一组隔离机参数运行 `make pve-live-guest-cold-boot-check`。此入口会对指定 Debian 13 验收机执行一次正常关机和一次开机；要求最初运行且没有任何登录会话，拒绝业务 VM 158。启动前重新核对节点、VM 身份、配置内存与宿主余量；无法确认时保留恢复记录，不尝试强制关机或重复启停。结束后保留入口要求的运行状态，执行者另外恢复整轮工作开始前的电源和内存配置。

与运行时服务夹具不同，冷启动模式暂时启用正式 readiness 服务和回收 timer 的两个开机链接。首次写入前在 `/var/lib/vc-workspace/acceptance-svc<随机标记>/manifest.json` 保存 root 私有、已 fsync 的恢复记录和原 PAM/状态文件；已有单元、drop-in 或开机链接不归本测试所有，直接拒绝覆盖。`owned-login.json` 和 `owned-login-next.json` 分别在原登录与开机后新登录派发前持久记录精确 UID、Lease、epoch 和 generation，不保存密码、不覆盖前次意图。

验收先创建真实 XFCE/Helper 并写入本轮 Home 标记，正常关机后等待原始 90 秒租约期限，既不修改时钟也不修改账本。开机后必须观察到新 kernel boot ID；不手动启动 Guest 服务或执行 reconcile，由正式开机 timer 自动达到精确 `revoked`、禁用账号、UID 进程和旧登录写入者均缺席。随后使用新租约确认原 UID/Home 保留，并建立新的真实 Helper。

电源动作只提交一次，之后读取同一 UPID；任务 `OK` 后另从所属节点的 `/qemu/<VMID>/status/current` 确认状态。集群资源清单可能滞后，不能用它把已完成关机误判为仍在运行，也不能把读取失败视为停止。进程意外退出或 VM 留在关机状态时，先核对原 UPID、节点实时状态、持久恢复目录和精确账号版本，再决定是否恢复开机；不得盲目重跑测试。清理先按版本关闭测试账号，再核对并移除仅本轮拥有的开机链接、单元、账号/Home/控制记录，恢复 PAM/状态文件；外部变更或不确定结果保留现场并失败。

该入口覆盖正常电源周期和到期回收，不证明断电持久性、快照回滚、未到期租约恢复、旧版本进程排空/升级、新模板或 Native/Windows 登录已验收。最新结果统一记在 [状态页](../plan/status.md)。

### 丢失登录创建回执验收

使用上面的隔离机参数运行 `make pve-live-login-receipt-check`。夹具安装同一正式 PAM/服务，为新建且固定的 UID 临时添加 `user@UID.service` 的独占 drop-in；不改全局 `user@.service` 模板。root `ExecStartPre` 真实暂停，使 logind 创建请求等待用户管理器 Job，直到原生 PAM 的 8 秒调用超时。测试要求此时登录失败且 `login_writers_absent=false`，不能确认回收完成；随后恢复被 pidfd 绑定的启动进程，由正式 timer 自动收敛，并以新租约在原 UID 建立真实桌面。

该测试复现的是请求已进入 logind 后丢失回执，不是网络模拟或伪造 Guest 返回值，也不覆盖尚未处理的 D-Bus 排队请求、logind/systemd 重启。恢复目录内先保存精确 UID、drop-in 内容、程序摘要与进程身份；清理只处理本轮单元/进程，恢复原 PAM/服务，删除本轮账号/Home/记录。启动与结束时须另行核对隔离机电源和内存，不能将测试成功当作完整升级或其他登录入口验收。

### 登录请求排队与 logind 崩溃恢复

相同隔离机参数下运行 `make pve-live-login-queue-check`。该入口会暂停并实际结束隔离机的 `systemd-logind` 进程，要求没有任何现存登录会话，并确认系统单元为 active/running 且 Restart=always；不能对业务 Guest 或共享验收机执行。仍安装同一正式 PAM/服务，不修改全局 D-Bus 策略、logind 配置或 Guest 发布 CLI。

夹具以 boot/PID/启动时刻、root 可执行文件和 unit InvocationID 绑定原登录服务，再使用 pidfd 暂停它。私有 D-Bus monitor 核对真实 `CreateSessionWithPIDFD` 的 UID、发送者 PID 与已登记的 PAM 创建者，磁盘只保存发送者、消息序号和进程身份，不保存密码。客户端超时并确实退出后，Guest 观察必须拒绝把登录服务超时当作关闭；恢复原 daemon 后，监视器须收到对应序号的真实错误回复，不能只以等待若干秒后未见进程代替请求被拒绝的证据。

正式 timer 收敛并以原 UID 建立新桌面后，测试再向精确绑定的 logind 发送 SIGKILL，核对系统自动启动了新 InvocationID，同时原 Helper/会话身份及 sealed 登录版本保持一致。该检查不是 PID 1/systemd 重启、创建请求执行到一半时崩溃、Guest 冷启动升级或 Mac 可视输入验收。

`/run/vc-workspace-svc<标记>/queue` 在发出信号前保存恢复记录；独立 guardian 持有原 daemon/observer 的 pidfd，observer 退出或 40 秒上限时恢复原 daemon，不按可复用的数字 PID 发送信号。结束时先恢复故障并观察原登录派发结果，再按精确版本回收账号，避免恢复动作释放在途登录后立刻误删账号。监视器错误不算通过；清理有疑问时保留账号/服务恢复记录，独立检查后再处理。

### 登录派生进程的内核验收

相同隔离机参数下运行 `make pve-live-login-tree-check`。此入口要求空闲 Debian 13、cgroup v2、Python Gio/UnixFD 和内核 pidfd；不会调用生产登录接口或修改 PAM。它创建两个锁定、无 Home、nologin 的临时账号和独立 systemd slice，通过真实 root fork/setuid 复现“父进程已死、UID 暂时为空、子进程迟到降权”。随后用 PIDFD 绑定独立 scope，核对实际 cgroup 归属和内核 subtree kill 日志，验证另一账号及同 UID 的新 scope 不被旧 scope 操作影响。

这是 OS 隔离机制验收，不是已完成真实 xrdp/PAM 或 SDK MCP 的接线。首次写入前保存 `/run/vc-workspace-tree<标记>/manifest.json`，记录本轮账号、scope 与独立恢复目标；进程采用 boot/启动时刻与 pidfd 核验，清理只接受精确的测试解释器/脚本/标记。结束后清理本轮进程、账号、scope/slice 和恢复目录，并核对原 PAM/xrdp 摘要。VM 内存与启停仍由执行者根据宿主余量显式处理，不由此命令自动修改。

此入口不自动启动/关闭 VM；操作者负责恢复测试前电源状态。它不是冷启动模板、旧版本排空、默认 MCP 或 Windows 服务验收，最新实测结果统一记在 [状态页](../plan/status.md)。

## Windows 原生账号与 Profile 验收

只选择没有业务会话的隔离 Windows VM。设置 `VC_WORKSPACE_LIVE_PVE_ENDPOINT`、`VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE`、`VC_WORKSPACE_LIVE_COMPUTER_VMID` 和 `VC_WORKSPACE_LIVE_WINDOWS_SESSION_TEST_BINARY`（交叉编译所得 amd64 Rust 测试 EXE，不是 Guest Agent 主程序）。可用 `VC_WORKSPACE_LIVE_WINDOWS_SESSION_ARTIFACT_BIND=<Mac 私网 IPv4>:18764` 提供有界、校验 SHA-256 的一次性下载；不要将该端口公开到互联网。

- `make pve-live-windows-session-check`：SYSTEM 原生 IPC、进程、身份、注册表和密文封装原语。
- `make pve-live-windows-accounts-check`：额外创建随机 Agent/Native SAM 账号，验证密码和生命周期；不创建交互 Profile。
- `make pve-live-windows-profile-check`：额外创建/加载全新的固定 SID Profile，验证保护性拒绝、撤销/到期后的资料保留，并运行尚未解决的 DPAPI 改密实验。此入口当前可能因真实数据无法解密而失败，不能跳过失败项后宣称完整 Profile 验收；具体结果见[状态页](../plan/status.md)。

Profile 夹具在加载前核对 SID 尚无 ProfileList 记录且精确目录不存在，结束时只通过该 SID 和固定路径删除自身 Profile；失败则保留并禁用可归属的 SAM/账本，防止产生无主资料。登录权限候选只为新 SID 临时设置五类 LSA 拒绝登录权限，拒绝接管已有 LSA 账号；移除前复核固定 SAM/SID、仅包含本轮权限，并独立枚举所有 LSA 账号及这五类权限的成员，要求前后基线一致，不修改组或其他用户权限。Go 收尾另行比较完整 SAM、所有权根和 ProfileList 基线。新旧密码只走有界内存/子进程 stdin，不输出到日志或参数。测试不替换已安装 Guest，不创建可视 WTS 桌面，也不等同于 macOS 或凭据管理器、跨重启验收。

同一节点顺序运行 Windows 验收，测试会先核对内存余量；对由本轮启动的 VM，结束后正常关机恢复原停止状态。失败时先读取原执行回执并检查精确残留，不重发改密或删除其他 Profile 来制造成功。该入口不修改宿主驱动、VM 内存、模板或原开发数据库。

## 身份实验室验收机

2026-09-05 从 Debian 12 Cloud-Init 模板 901 完整克隆 VMID 9300 `vc-workspace-identity-lab-20260905` 到 `infra-node1`，使用 Ceph 30 GiB 系统盘、2 vCPU、4 GiB、DHCP、QEMU Guest Agent 和 `vc-workspace;identity-lab;temporary` 标签。当次地址为 `10.31.0.159`；DHCP 地址可能复用，后续应以 PVE Guest Agent 为准，不应依赖本机旧 `known_hosts` 条目。

该 VM 从 `10.31.0.2` 安装 Docker，并接受 Mac 侧显式拉取的 `linux/amd64` PostgreSQL、Keycloak、Debian 13 基础镜像。完整构建与无构建复验均通过，测试容器、网络、卷、随机凭据和监听端口已清理；磁盘中保留身份实验室源码、离线基础镜像包、amd64 测试二进制和 Docker 构建缓存。

同日 VMID 9300 被复用为临时 Samba 4.17 AD DC，并从模板 901 完整克隆独立 Debian 12 客户端 VMID 9301 `vc-workspace-ad-client-20260905`：2 vCPU、2 GiB、Ceph 20 GiB、QGA、DHCP 和 `vc-workspace;identity-client;temporary` 标签，当次地址 `10.31.0.160`。客户端通过 VC Workspace 生成的收敛计划直接加入 `ad.vcw.test`，完成 SSSD/NSS/PAM/Kerberos、自动 Home、允许组拒绝、在线撤权/恢复和幂等复验；过程中修复了 PVE REST `agent/exec` 原始 stdin 被控制面重复 Base64，以及客户端重启后 DHCP DNS 覆盖域 DNS并使 SSSD 离线的两个缺陷。重启后再次确认域 DNS、SSSD Online 和完整撤权链路。两台 VM 验收结束后均正常关机并以停止状态保留，域实验密码只保留在权限 `0600` 的域控实验文件中，本机临时副本已清理。完整步骤和验证边界见[身份集成实验室](identity-lab.md)。

## 镜像构建

- Debian 13 软件源：`http://10.31.0.2/debian`；安全更新：`http://10.31.0.2/debian-security`。已验证 `trixie`、`trixie-updates` 和 `trixie-security` Release 可访问。
- 镜像站是按请求缓存的软件包树，当前没有 `/debian-cd/` 安装 ISO 树。已将 [Debian 官方校验清单](https://cdimage.debian.org/debian-cd/current/amd64/iso-cd/SHA256SUMS) 对应的 `debian-13.6.0-amd64-netinst.iso` 导入 `infra-node6` 的 `local:iso`，SHA-256 为 `65273beed27b2df543b68b65630ba525cfbad8df2b12035732b2dff87d6664e7`；安装后的全部 APT 流量走内网源。
- `10.31.0.2` 的 `/local/windows-software/` 当前只提供应用软件，没有 Windows OS ISO。Windows 介质不从不明镜像代替：Windows 11 使用 [Microsoft Evaluation Center](https://www.microsoft.com/en-us/evalcenter/evaluate-windows-11-enterprise) 的简体中文 x64 Enterprise 25H2 Evaluation，导入为 `local:iso/Win11_25H2_Enterprise_Eval_zh-cn_x64.iso`（7,371,034,624 bytes）；PVE 下载任务按 [Microsoft 官方哈希清单](https://aka.ms/Win11-Hash-PDF) 校验 SHA-256 `7b4ac87391b659f7724229682b642256289a1c00504056249f0f12029157d3d2`。
- Packer 定义位于 `deploy/images/debian-13-xfce` 和 `deploy/images/windows-client`。示例变量文件只表达字段，不得写入真实 Token、构建密码或商业 ISO。
- Debian 模板包含 XFCE、xrdp、QEMU Guest Agent、Cloud-Init、SPICE vdagent 和 VC Workspace Agent；只有本机 3389/TCP 可接受连接时 Agent 才写 `desktop-ready`。
- 缩放重协商后的 xrdp 光标缓存修复使用独立、固定 Debian 安全版本的 [构建与隔离验收入口](../../deploy/images/debian-13-xfce/xrdp/README.md)。新 Debian 构建配方要求经过摘要校验的包，Guest 安装前再次校验且拒绝未知版本/降级；构建器需配置 `VC_WORKSPACE_LINUX_XRDP_BUNDLE`。它不会改动既有模板或运行中桌面；正式模板实建/推广状态仅以 IMAGE-01 / MAC-03 / NET-01 为准。
- Debian 13 正式模板为 VMID 9100，管理后台状态为 `ready`；VMID 9101 是该模板的独立完整克隆验收机，不参与生产调度。
- Windows 10/11 共用参数化构建器，使用 OVMF、Secure Boot 与 TPM 2.0，安装 VirtIO、QEMU Guest Agent、Cloudbase-Init、RDP 和 VC Workspace Agent，最后执行 Sysprep generalize。
- Windows 自动应答光盘由 Packer 的 `cd_content` / `cd_files` 在单次构建中生成，含一次性 Administrator 构建密码、配置脚本、Cloudbase-Init 和 VC Workspace Agent；安装时从只读光盘读取这些 payload，避免通过 WinRM 逐块上传二进制。临时光盘不作为仓库或 PVE 的长期 ISO 保存。Windows OS ISO 必须由部署方按授权提供。
- Windows 10/11 分别设置 PVE `ostype=win10` / `ostype=win11`。Sysprep 将 Cloudbase-Init 生成的 `conf\Unattend.xml` 复制到无空格临时路径，再以 `/generalize /oobe /quit /mode:vm` 完成泛化，保证克隆首启进入 Cloudbase unattended 阶段；Packer 等待 Sysprep 真实退出码，最终关机和模板转换由 PVE API 阶段负责。
- Windows 11 25H2 构建使用无人值守 OOBE，并按 [Microsoft Privacy CSP](https://learn.microsoft.com/en-us/windows/client-management/mdm/policy-csp-Privacy#disableprivacyexperience) 在镜像配置阶段写入 `HKLM\SOFTWARE\Policies\Microsoft\Windows\OOBE\DisablePrivacyExperience=1`，防止新建 `vdi` 用户首次 RDP 登录再次进入隐私选择页；当前 VM 9111 早于该修复，下一次构建需复验。配方同时使用 `powercfg /hibernate off` 关闭休眠和 Fast Startup，并在 Sysprep 前阻止自动设备加密、关闭 BitLocker、等待系统盘 `FullyDecrypted`。
- Windows 构建的 `product_key` 是仓库外敏感变量；可使用 Microsoft 公布的 GVLK 选择 ISO 版本，但模板激活和许可证合规仍由部署方单独负责。
- node4 现有 `Win10_22H2_Chinese_Simplified_x64v1.iso` 的 SHA-256 为 `d485d370406cbcb68959718817bd12ed87c537a14c885f84962e07136fc4a049`；`virtio-win-0.1.271.iso` 的 SHA-256 为 `0040e268e1095b080abfec74214d094bc7fe565568533505b99d70622061c187`。两者由临时 VM 9199 以只读 CD-ROM 块设备计算并用 hexdump 复核，随后 VM 9199 已删除。
- 2026-09-04 历史共享会话产物的 Linux/Windows SHA-256 分别为 `920921127257dd4282a751a33be61031f09566ef5eb40e808f649cec2c6f884e` / `75f51a64bc34e614002fab06e736fa0e4f38bb9c448ef1033884187be10f6237`，不能作为当前 V2 升级产物。当前源码和 Helper 协议需要重新验证；最新实测产物与仍缺的 Windows 每用户启动路径见 [项目状态](../plan/status.md)。CI 已配置 Windows Runner 构建，但远端执行尚待 RELEASE-01。
- Cloudbase-Init 固定为 1.1.8 x64，运行 `deploy/images/windows-client/fetch-cloudbase-init.sh dist/CloudbaseInitSetup.msi` 下载并校验；MSI SHA-256 为 `0e7fa42e0cbc0ce7657f85730b0c6cc7afc4087a3639df0ff51a721a0be19bd5`。
- 控制面签发连接前会依次检查 Linux `/var/lib/vc-workspace/desktop-ready` 与 Windows `C:\ProgramData\VC Workspace\Agent\desktop-ready`；为兼容既有镜像仍回退读取旧状态路径。只有内容为 `ready` 时，才为当前平台用户创建或复用稳定的 `vcw…` Guest 本地账号、应用权限并轮换该账号的短期密码后返回 RDP 描述符。模板中的 `vdi` 账号只保留作镜像引导与兼容检查，新的人类连接不再共享它。
- 管理后台将镜像切换为 `ready`、启用镜像或修改已就绪镜像的 VMID/源节点时，会实时读取 PVE 清单；VMID 不存在、不是 QEMU 模板或与源节点不一致都会被拒绝，避免数据库状态与集群漂移。
- 管理后台可以直接启动镜像 Bootstrap。控制面先校验构建节点在线、存储存在且支持 `images`、目标 VMID 未占用，再启动固定的 Debian 13 或 Windows 10/11 Packer 定义。同一镜像同一时间只允许一个构建 Job；Packer 输出脱敏后持久化为进度，成功后状态进入 `testing`，仍需克隆、QGA/Agent、RDP 和授权验收后才可人工设为 `ready`。
- 构建密码和 Windows 产品密钥只通过单次 HTTPS 请求进入构建进程环境，不写入镜像配置、Job request、审计详情或命令行。生产环境必须使用独立的最小权限 PVE API Token，并把构建执行器放在受控主机；当前 MVP 执行器随控制面进程运行，服务重启会中止当前 Packer 子进程，后续再拆分为可恢复 Worker。
- 可在不启动或修改虚拟机的情况下重复审计三套正式模板。该检查验证 VMID 9100/9110/9111 的模板/停止状态、QEMU Agent 标志、CPU/内存、磁盘、网络、当前 `vc-workspace` 标签或迁移期旧标签，并额外验证 Windows 的 OVMF、EFI 与 TPM 2.0；基础模板不得预先绑定 `hostpci`：

```bash
VC_WORKSPACE_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
make images-live-check
```

Windows 10 技术构建管线和 VMID 9110 已验收，管理后台状态为 `ready`，但普通 Home/Pro 22H2 已结束支持，因此配置继续保持停用；生产启用前必须换用满足 LTSC 或 ESU 生命周期且授权合规的介质。Windows 11 VMID 9111 同样已验收并在管理后台标记为 `ready`，但 Evaluation 介质只用于技术验证，配置保持停用；生产启用前必须替换为组织已授权介质并重建模板。

## 隔离的 RDP 弱网验收

`deploy/pve/rdp-network-lab.sh` 只用于专用 Debian QEMU/KVM 测试 Guest。禁止在 PVE 宿主、生产桌面或 Mac 执行；先确认该 VM 没有其他用户工作。以 root-owned、0700 文件安装到测试 Guest（可通过 QGA 的 file-write），再运行：

```bash
VC_WORKSPACE_NETWORK_LAB_ACK=isolated-guest \
  /bin/bash /root/vcw-rdp-network-lab.sh \
  '<测试 Guest 的实际 hostname>' ens18 '<Mac IPv4>' loss 45
```

脚本要求显式 hostname、IPv4、接口、档位和 5–120 秒持续时间，只接受初始默认 `mq + fq_codel`，拒绝自定义 qdisc。`latency` 注入 120 ms delay/20 ms jitter；`limited` 再限制 6 Mbit/s；`loss` 再加 1% 丢包；`outage` 为 100% 丢包。它仅匹配发往指定 Mac 的 TCP/3389 出站流量，不是双向 WAN 模拟，不能把配置值当作实测端到端 RTT。实际 seed 和包/丢弃计数输出在日志中。

结束或收到 TERM 时恢复默认队列；独立 systemd 定时器在测试期限 +10 秒执行兜底，SIGKILL/调用方失联也能恢复，只删除本工具自己的 root handle，不覆盖其他人的新配置。每轮完成后比对 `tc -j qdisc show dev ens18`，确认无 `7c01:`/`7c02:`，并检查无 `vcw-network-rollback-*` 剩余 timer。正常回滚及 SIGKILL 后兜底已在 VM 160 验证。QGA 控制通道不经过被限速的 TCP 流。

固定操作序列：真实 `.app` 登录并连接测试用户 → 终端输入 → 最大化/全屏/恢复 → 短时断网 → 自动恢复 → 再注入故障并取消。另用精确匹配测试 Mac/3389 的 `ss -K` 模拟 TCP reset；只能在隔离 Guest、核对唯一测试会话后执行。比较恢复前后的 XFCE PID、DISPLAY 与终端内容，不能把创建新 OS 会话当成保留原会话。撤销授权后不得因重试恢复访问。

首次连接需分开验收两种情形：①全新受管 OS 用户的 xrdp 冷登录，核对 Xorg 就绪前的显示请求是否被丢弃，并由 Guest `xrandr` 证明无需手动重试就达到当前目标尺寸；不能复用已存在的 XFCE 会话代替。②首帧前传输失败，用最长 15 秒的 transient systemd 故障单元在同一隔离目标范围注入 TCP reset，客户端日志应记录 `firstFrame=0`，随后重新授权并恢复；用单元退出、默认 qdisc 和相同 OS 会话核对清理及恢复。2026-09-06 两种情形均发现并验证了客户端修复，具体值以 [当前状态](../plan/status.md) 为准。RDP 首帧、OS 登录完成和最新目标分辨率实际绘制是三个不同事件。

当前仅完成故障恢复纵向验收；持续运动负载、帧率、输入延迟分布、CPU/带宽对照、双向 WAN 和 Gateway 仍按 [NET-01](../plan/status.md) 推进。不要用短时间终端输入通过替代性能基准。

## GPU 基线

2026-09-06 编码核查：VM 160 的 `xrdp --version` 为 0.10.1，构建未启用 `--enable-x264`/`--enable-openh264`；本轮连接日志明确为 `starting gfx rfx pro codec session`。Mac 固定构建关闭 OpenH264/FFmpeg，因此当前是 Intel 渲染 + CPU RFX 编解码，不是端到端 H.264/GPU 编码。上游 [H.264 说明](https://github.com/neutrinolabs/xrdp/wiki/H.264-encoding) 的当前实现是 x264/OpenH264 软件编码；同页说明 auto 网络类型按 LAN 处理。下一步应在独立镜像验证新版 xrdp/xorgxrdp + 自包含 H.264 解码依赖，保留 RFX 回退，并单独评估许可证、文本质量、CPU 与带宽。若要硬件编码，还需要独立编码后端/协议评估，不能仅给 VM 添加 GPU 或打开客户端参数就宣布完成。

2026-09-06 VM 158 的只读基线：没有 `hostpci`，Guest 无 `/dev/dri`，其独立用户的 Xorg 日志为 `GLX: Initialized DRISWRAST GL provider`。`xorg.conf` 已配置 `DRMDevice=/dev/dri/renderD128`、DRI3 和 `i915 radeon` allowlist，但设备不存在，不能把配置项当作 GPU 已启用。Guest 为 Debian 13、内核 `6.12.107+deb13-cloud-amd64`、Mesa `25.0.7-2+deb13u1`、xrdp `0.10.1-3.1+deb13u2`、xorgxrdp `0.10.2-1`。后续验证不修改这台运行中桌面或宿主 i915/VFIO。

2026-09-06 后续隔离实测：启动 VM 160（未改其 GVT-g 配置），同一测试账号首次 RDP 为 `llvmpipe / Accelerated: no`；Xorg 明确报告 `renderD128 open failed`。修复受管本地用户初始化后，新 OS 会话的 `glxinfo -B` 返回 `Mesa Intel(R) HD Graphics 530 (SKL GT2) / Accelerated: yes`，Xorg 为 `glamor X acceleration enabled`、`iris`。原生 Mac 客户端确认了实际图形帧、普通窗口 `2000×1260`、最大化 `5120×2678` 与全屏 `5120×2804`。这是同一台 GVT-g VM 上的软件回退/硬件渲染对照，不是无 GPU/GVT-g 性能基准，也不代表编码已经硬件加速。

GPU 验收需分清 Guest 渲染、RDP 编码和 Mac 画面布局。后续仍需在隔离机测相同分辨率和负载下的帧率、输入延迟、带宽及 CPU；1080p、1920×1200 和 5K Retina 分开记录。PVE 档位描述的分辨率不能代替 XRDP 离屏渲染实测，更不能据此承诺 5K 性能。当前 Mac 构建未启用 OpenH264/FFmpeg；上游 [xorgxrdp 0.10.3](https://github.com/neutrinolabs/xorgxrdp/releases/tag/v0.10.3) 才加入配合 xrdp 0.10.2 的 H.264 capture，现有 Guest 版本不能直接套用该能力。编码栈升级需在独立镜像完成兼容性与安全版本复核后再推广。当前工作排期见 [GPU-02](../plan/status.md)。

当前补齐的是控制面 `vcw…` 人工桌面用户的 `render` 组访问，不授予 `video`/sudo、不放宽设备权限，也不修改宿主驱动；已有图形进程不会自动获得新组，需保存工作并注销 OS 会话后重连。独立 `vca…` Agent 和目录用户仍需各自的 GPU 访问策略与验收，不能据此标为完成。上游也记录了 [Debian 13 render 权限导致软件回退](https://github.com/neutrinolabs/xorgxrdp/discussions/361) 的同类现象。

测试 Guest 安装 `mesa-utils` 后，可对当前用户的唯一 XFCE 会话执行只读渲染器门禁；它同时检查 renderer 和 `Accelerated`，不会把 `direct rendering: Yes` 当成硬件证据：

```bash
VC_WORKSPACE_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
VC_WORKSPACE_LIVE_DESKTOP_VMID=160 \
VC_WORKSPACE_LIVE_DESKTOP_USERNAME='<本次测试绑定的用户名>' \
VC_WORKSPACE_LIVE_DESKTOP_EXPECTED_RENDERER='Mesa Intel(R) HD Graphics 530' \
VC_WORKSPACE_LIVE_DESKTOP_EXPECTED_ACCELERATED=yes \
make pve-live-desktop-renderer-check
```

2026-09-02 对真实集群只读验收确认 PVE 为 `9.2.10`，6 个在线节点均检测到 Intel HD Graphics 530（`8086:1912`，`0000:00:02.0`）。完整直通侧仍报告 `iommugroup=0`，因此不能通过 VFIO 门禁；但每个节点的 `/hardware/pci/0000:00:02.0/mdev` 都实际返回 `i915-GVTg_V5_4`（1920×1200，当前可用 1 个）和 `i915-GVTg_V5_8`（1024×768，当前可用 2 个）。这两种能力必须分开判断，不能再用 IOMMU 结果否定 GVT-g。

控制面已经实现 PVE `/cluster/mapping/pci` 读取、创建、更新、逻辑映射配置和两类调度门禁。PVE 映射属性中 `path` 是 PCI BDF，`id` 是 `vendor:device` 硬件身份，`subsystem-id` 是 OEM 子系统身份；控制面分别解析并校验。完整直通映射要求有效 IOMMU group；mediated 映射要求 `mdev=1`，目标设备实时暴露档位指定的类型且剩余实例大于 0。真实集群已创建六节点 mediated 映射 `vc-vdi-intel-gvtg`（历史资源名）；两个 GVT-g 档位仍默认停用，由平台管理员选择映射并明确启用。

六个节点的只读 GPU 身份审计确认均为 `8086:1912`、OEM subsystem `103c:806a`，当前仍报告 `iommugroup=0` 且不可分配。维护前后可重复运行：

```bash
VC_WORKSPACE_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
make pve-live-gpu-check
```

GVT-g 优先路径：

- `intel-gvtg-v5-4` 和 `intel-gvtg-v5-8` 是两个独立档位，分别保存精确的 PVE `mdev_type`，不从显示名称推导行为。
- 管理后台创建 mediated 映射时自动收集所有支持该类型的在线节点，并向 PVE 写入 `mdev=1`；每个节点仍使用自己的 `00:02.0`，跨节点只保存逻辑映射 ID。
- 克隆完成后控制面写入 `hostpci0=mapping=<id>,mdev=<type>`。实机启动证明 GVT-g mdev 不支持 `x-vga`，因此不能沿用完整直通参数；同时不为现有 i440fx Linux 模板强制 PCIe。调度前重新读取 `available`，容量为 0 的节点不会进入目标节点列表。
- GVT-g 档位禁止在线迁移。PVE noVNC/SPICE 不能显示 vGPU 提供的画面，验收必须通过 Guest 驱动状态和 RDP 实际帧。
- Linux 首先确认 guest `i915`、`/dev/dri/renderD*` 与硬件渲染；Windows 必须安装兼容 HD 530/GVT-g 的 Intel Guest 驱动，并验证设备管理器、冷启动和 RDP 会话。未经这两类验收不得把档位设为默认。

2026-09-02 写入验收使用 Debian 13 模板 9100 链接克隆 VM 160 `vc-vdi-mvp-gvtg-check`。第一次沿用完整直通的 `x-vga=1` 时，PVE 明确拒绝启动并自动清理 mdev；去掉 `x-vga` 和强制 PCIe 后，`hostpci0=mapping=vc-vdi-intel-gvtg,mdev=i915-GVTg_V5_4` 冷启动成功。Guest Agent 返回 `desktop-ready`，RDP 3389 可达，来宾系统的 `card1` 和 `renderD128` 均由 `i915` 驱动，PCI 身份为 `8086:1912`；`card0` 仍是 Bochs 虚拟显示设备。验证机已关机保留，infra-node1 容量恢复为 V5_4 可用 1、V5_8 可用 2。Windows Guest 驱动和实际 RDP 图形帧仍是生产启用前的独立验收项。

完整直通备选路径仍保留，但它会从宿主机 `i915` 接管整块 iGPU，需要独立维护窗口。首个维护候选是 `infra-node4`：当前六台节点中它的 CPU/内存压力最低、不运行本地 `ceph-mon`，VM 150/201/406/500 使用共享 `ceph-pve` 且没有 PCI/USB 直通，可在维护前显式迁移。VM 149 使用 `local-lvm` 与本地 ISO，是唯一不能直接依赖共享存储迁移的阻点；必须先执行带本地磁盘的迁移或停机。node4 以 UEFI 启动，但 `/etc/kernel/proxmox-boot-uuids` 不存在，因此内核参数由 GRUB 管理。当前 HA 资源均为 `ignored`，不能假设 HA 会自动排空节点。节点是 HP/i3-6100 平台，BIOS 为 `N23 02.12`（2017-06-05），内核报告完全没有 DMAR 记录；维护窗口必须先在 HP F10 固件界面确认并启用 VT-d。

完整 PCI passthrough 约束：

- 节点 BIOS 打开 VT-d，内核启用 IOMMU，绑定设备到 VFIO 后才允许启用。
- 一块 iGPU 只分配给一台 VM；VM 固定在拥有该设备的节点，禁止在线迁移。
- 独立 GPU 后续沿用 `pci_passthrough` 档位和同样的 IOMMU/独占检查；Resource Mapping 解析保留同一节点的多条设备映射、分号分隔的多功能 PCI path，以及省略 function 的整设备 path，能够覆盖独显显示/音频功能。厂商 vGPU 作为单独能力，不与完整直通混用。

首台验收节点按 [Proxmox VE PCI(e) Passthrough](https://pve.proxmox.com/pve-docs/pve-admin-guide.html#qm_pci_passthrough) 的要求推进，不能在业务时段直接修改并重启全部集群。仓库工具均不自动迁移 VM 或重启节点：

1. 确认 node4 的 BIOS/UEFI 已打开 Intel VT-d，并确认有物理控制台或独立带外管理；`00:02.0` 是 boot VGA，交给 VFIO 后宿主机本地显示会消失。
2. 显式迁移 VM 150/201/406/500，并处理 VM 149 的本地盘；只有 `qm list` 和 `pct list` 都没有运行中 Guest 时才允许准备主机。
3. 先运行 `deploy/pve/gpu-passthrough-host-audit.sh 0000:00:02.0` 保存当前状态，再运行 `deploy/pve/gpu-passthrough-host-prepare.sh --device 0000:00:02.0` 查看 dry-run 计划。默认模式不会写文件。
4. 在排空节点并确认带外控制台后，维护人员才可执行 `sudo deploy/pve/gpu-passthrough-host-prepare.sh --device 0000:00:02.0 --apply --ack-headless`。脚本为 GRUB 增加 `intel_iommu=on iommu=pt initcall_blacklist=sysfb_init`，加载 VFIO、将 `8086:1912` 从 `i915` 交给 `vfio-pci`，备份原配置并更新 GRUB/initramfs；它不会重启。
5. 人工复核备份目录和网络/控制台回退条件后再重启。若主机启动异常，使用 `deploy/pve/gpu-passthrough-host-rollback.sh /root/vc-workspace-gpu-backup-TIMESTAMP --apply` 恢复配置并再次人工重启。
6. 重启成功后设置 `VC_WORKSPACE_OUT_OF_BAND_CONSOLE_CONFIRMED=true`，依次运行 `deploy/pve/gpu-passthrough-host-audit.sh` 与 `deploy/pve/gpu-passthrough-preflight.sh 0000:00:02.0`；只有显式 IOMMU、单设备 sysfs group、`vfio-pci`/未绑定、无运行 VM/CT 和带外控制台确认全部通过，才建立集群级 PCI Resource Mapping。
7. 在管理后台编辑 Intel iGPU 档位，展开“创建 PVE 映射”，从可直通设备清单选择 node4 的 `0000:00:02.0` 并创建 `vc-workspace-intel-igpu`；随后选择该映射并启用档位。控制面会使用实时清单写入正确的 `path=0000:00:02.0,id=8086:1912,subsystem-id=103c:806a,iommugroup=...`，调度器固定到 node4，克隆完成后自动配置 `hostpci0`。先用 Windows 11 验收克隆完成冷启动、Intel Guest 驱动、RDP 图形会话、正常关机与再次冷启动；PVE noVNC/SPICE 不作为 GPU 桌面的验收通道。

以上宿主机排空、VFIO 和重启步骤仅属于完整直通备选路径，目前尚未执行；GVT-g 路径不应运行这些脚本，否则会把 iGPU 从 `i915` 移走并破坏 mdev 能力。

## 本地配置

```dotenv
VC_WORKSPACE_PVE_ENDPOINT=https://pve.example.com
VC_WORKSPACE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential
VC_WORKSPACE_PVE_MUTATIONS_ENABLED=false

# Web 镜像 Bootstrap（需要上面的写入开关）
VC_WORKSPACE_IMAGE_BUILDER_ENABLED=true
VC_WORKSPACE_PACKER_PATH=packer
VC_WORKSPACE_IMAGE_ROOT=deploy/images
VC_WORKSPACE_IMAGE_BUILDER_PVE_TOKEN_ID=builder@pve!packer
VC_WORKSPACE_IMAGE_BUILDER_PVE_TOKEN_SECRET=replace-me
VC_WORKSPACE_LINUX_AGENT_BINARY=dist/vc-workspace-guest-agent-linux-amd64
VC_WORKSPACE_LINUX_XRDP_BUNDLE=dist/debian-13-xrdp
VC_WORKSPACE_WINDOWS_AGENT_BINARY=dist/vc-workspace-guest-agent-windows-amd64.exe
VC_WORKSPACE_CLOUDBASE_INIT_MSI=dist/CloudbaseInitSetup.msi
# 安装网络：绑定地址和网卡只能设置一个，默认端口范围为 8840-8847
VC_WORKSPACE_IMAGE_HTTP_BIND_ADDRESS=
VC_WORKSPACE_IMAGE_HTTP_INTERFACE=
VC_WORKSPACE_IMAGE_HTTP_PORT_RANGE=8840-8847
```

安装 Packer 和配方指定的 Proxmox 插件后，用 `make images-packer-check` 验证实际生成参数与 Debian/Windows 配方。它使用无秘密的临时产物，只执行 `packer validate`，不访问 PVE、不验证包安装，也不创建虚拟机；`make images-check` 另负责脚本、安装器边界和 HCL 语法检查。Debian 包真实安装验收见 [xrdp 构建说明](../../deploy/images/debian-13-xfce/xrdp/README.md)。

凭证文件开发格式为：

```text
username@realm / password
```

生产部署应改用最小权限、权限分离的 PVE API Token。
