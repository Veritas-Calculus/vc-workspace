# PVE 开发环境

## 安全边界

- PVE 地址与凭证通过环境变量或仓库外文件提供。
- 默认只读；写操作必须显式设置 `VC_VDI_PVE_MUTATIONS_ENABLED=true`。
- 控制面写入口限定为桌面 Clone/Start/Stop、PCI Resource Mapping、允许列表中的模板构建和固定 Guest 权限策略，不提供 Delete UI，也不接受任意 Packer 路径、Guest 命令或用户名。
- 测试资源名称必须使用 `vc-vdi-mvp-` 前缀，并记录内部 Job、VMID 和 PVE UPID。

## 已验证环境

2026-09-01 已使用用户提供的环境完成验证：PVE 9.2.10、集群 `infra`、6 个在线节点、共享 `ceph-pve` RBD，并发现多份 Cloud-Init 模板。随后从模板 901 幂等克隆 VM 147 `vc-vdi-mvp-debian-001`，通过 Web/API 与 MCP 路径完成启动、停止，验收后保持停止状态。VM 158 `vc-vdi-mvp-desktop-003` 是 Debian 13 XFCE/xrdp 数据面验收机；验收时它运行在 `infra-node6`，控制面会从 PVE 集群清单动态定位所在节点，不绑定这一记录。源模板 901 实际是 Debian 12，因此 v6 seed 显式禁用云镜像自带的 deb822 bookworm 源，改用内网 trixie 源执行受控升级，再安装桌面。VM 153、157 是停止状态的早期安装试验，不参与调度；2026-09-02 的客户端冷启动测试确认 VM 153 的 PVE Agent 通道配置存在但来宾 Agent 不响应，等待被正常取消并通过 ACPI 恢复停止。凭证内容未写入仓库。

VM 158 的数据面验收结果：Debian `trixie/13`、QEMU Guest Agent 与 xrdp 均为 active，`desktop-ready` 就绪标记可读，3389/TCP 正常监听。控制面签发一次性 RDP 描述符后，macOS 上的 FreeRDP 3.31.0 完成认证、TLS/RDP 协商、Metal 窗口创建与桌面图像帧接收；VM 侧 xrdp-sesman 确认 `vdi` 登录、Xorg `:10`、XFCE 窗口管理器和后续重连成功。这台机器用于验证完整数据面，不等同于正式 Packer 模板产物。

2026-09-02 使用正式 Packer 定义在 `infra-node6` 完成 VMID 9100 `vc-vdi-debian-13-xfce`，构建耗时 21 分 57 秒并成功转换为停止状态模板。完整克隆 VM 9101 `vc-vdi-mvp-debian13-template-check` 首启后获得 `10.31.0.177`，QEMU Guest Agent、`desktop-ready`、Debian 13 trixie、`startxfce4` 会话入口和 macOS FreeRDP 认证均通过；验收后保留为停止状态资源。

2026-09-02 使用正式 Windows Packer 定义在 `infra-node4` 完成 VMID 9110 `vc-vdi-windows-10-22h2`，构建耗时 29 分 21 秒并成功转换为停止状态模板。完整克隆 VM 9112 `vc-vdi-mvp-windows10-template-check` 首启后获得 `10.31.0.195`，确认为 Windows 10 Pro 22H2 build 19045；QEMU Guest Agent、Cloudbase 首启、VC Workspace Agent 启动任务、`desktop-ready`、TermService/3389、QGA 密码轮换和 macOS FreeRDP 认证均通过。验收后 VM 9112 正常关机并保留为停止状态资源。

2026-09-02 使用 Microsoft Windows 11 Enterprise 25H2 Evaluation 介质在 `infra-node4` 完成 VMID 9111 `vc-vdi-windows-11-25h2-eval`，构建耗时 1 小时 20 秒并成功转换为停止状态模板。完整克隆 VM 9113 `vc-vdi-mvp-windows11-template-check` 首启后获得 `10.31.0.199`，确认为 Windows 11 Enterprise Evaluation build 26200；Cloudbase-Init、QEMU Guest Agent、VC Workspace Agent、`desktop-ready`、TermService/3389、BitLocker 完全解密和 macOS FreeRDP 完整图形会话均通过。后续从签名 macOS 客户端做冷启动验收时，中国区介质在 `vdi` 首次登录仍出现一次隐私/跨境数据提示；完成提示后桌面、键鼠、双向剪贴板与全屏通过。构建配方已补设备级隐私体验禁用策略，下一版模板必须无人值守首登复验后才能替代当前技术模板。

MCP 验收同时验证：5 个工具可发现、活动租约唯一、错误 Agent 无法读取租约，以及租约内启动/停止后可释放。

2026-09-02 权限策略实机验收：运行中的 Debian 13 VM 158 通过 QEMU Guest Agent 依次加入 sudo 并验证 `sudo -n` 可提权，再移除 sudo 和受控 sudoers 文件并反向验证提权失败，最终保持标准用户。中文 Windows 11 VM 9113 暴露了内置组名称本地化问题，改为通过 SID `S-1-5-32-544` 读取实际组名后完成加入/移除；Windows 10 VM 9112 使用同一最终脚本再次通过，并以正常 shutdown 恢复停止状态。PVE 偶发的 `guest-exec-status` timeout 只在 60 秒执行窗口内重试；Guest Agent 未运行时不得绕过策略签发连接。

可对明确的非生产验收机重复运行；测试会改变 `vdi` 组成员并可能注销其桌面会话，结束时恢复原权限。Windows 测试会在目标原本关机时自动开机，并使用正常 shutdown 恢复：

```bash
VC_VDI_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_VDI_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
VC_VDI_LIVE_PRIVILEGE_VMID=158 \
make pve-live-linux-privilege-check

VC_VDI_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_VDI_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
VC_VDI_LIVE_PRIVILEGE_VMID=9112 \
make pve-live-windows-privilege-check
```

## 镜像构建

- Debian 13 软件源：`http://10.31.0.2/debian`；安全更新：`http://10.31.0.2/debian-security`。已验证 `trixie`、`trixie-updates` 和 `trixie-security` Release 可访问。
- 镜像站是按请求缓存的软件包树，当前没有 `/debian-cd/` 安装 ISO 树。已将 [Debian 官方校验清单](https://cdimage.debian.org/debian-cd/current/amd64/iso-cd/SHA256SUMS) 对应的 `debian-13.6.0-amd64-netinst.iso` 导入 `infra-node6` 的 `local:iso`，SHA-256 为 `65273beed27b2df543b68b65630ba525cfbad8df2b12035732b2dff87d6664e7`；安装后的全部 APT 流量走内网源。
- `10.31.0.2` 的 `/local/windows-software/` 当前只提供应用软件，没有 Windows OS ISO。Windows 介质不从不明镜像代替：Windows 11 使用 [Microsoft Evaluation Center](https://www.microsoft.com/en-us/evalcenter/evaluate-windows-11-enterprise) 的简体中文 x64 Enterprise 25H2 Evaluation，导入为 `local:iso/Win11_25H2_Enterprise_Eval_zh-cn_x64.iso`（7,371,034,624 bytes）；PVE 下载任务按 [Microsoft 官方哈希清单](https://aka.ms/Win11-Hash-PDF) 校验 SHA-256 `7b4ac87391b659f7724229682b642256289a1c00504056249f0f12029157d3d2`。
- Packer 定义位于 `deploy/images/debian-13-xfce` 和 `deploy/images/windows-client`。示例变量文件只表达字段，不得写入真实 Token、构建密码或商业 ISO。
- Debian 模板包含 XFCE、xrdp、QEMU Guest Agent、Cloud-Init、SPICE vdagent 和 VC Workspace Agent；只有本机 3389/TCP 可接受连接时 Agent 才写 `desktop-ready`。
- Debian 13 正式模板为 VMID 9100，管理后台状态为 `ready`；VMID 9101 是该模板的独立完整克隆验收机，不参与生产调度。
- Windows 10/11 共用参数化构建器，使用 OVMF、Secure Boot 与 TPM 2.0，安装 VirtIO、QEMU Guest Agent、Cloudbase-Init、RDP 和 VC Workspace Agent，最后执行 Sysprep generalize。
- Windows 自动应答光盘由 Packer 的 `cd_content` / `cd_files` 在单次构建中生成，含一次性 Administrator 构建密码、配置脚本、Cloudbase-Init 和 VC Workspace Agent；安装时从只读光盘读取这些 payload，避免通过 WinRM 逐块上传二进制。临时光盘不作为仓库或 PVE 的长期 ISO 保存。Windows OS ISO 必须由部署方按授权提供。
- Windows 10/11 分别设置 PVE `ostype=win10` / `ostype=win11`。Sysprep 将 Cloudbase-Init 生成的 `conf\Unattend.xml` 复制到无空格临时路径，再以 `/generalize /oobe /quit /mode:vm` 完成泛化，保证克隆首启进入 Cloudbase unattended 阶段；Packer 等待 Sysprep 真实退出码，最终关机和模板转换由 PVE API 阶段负责。
- Windows 11 25H2 构建使用无人值守 OOBE，并按 [Microsoft Privacy CSP](https://learn.microsoft.com/en-us/windows/client-management/mdm/policy-csp-Privacy#disableprivacyexperience) 在镜像配置阶段写入 `HKLM\SOFTWARE\Policies\Microsoft\Windows\OOBE\DisablePrivacyExperience=1`，防止新建 `vdi` 用户首次 RDP 登录再次进入隐私选择页；当前 VM 9111 早于该修复，下一次构建需复验。配方同时使用 `powercfg /hibernate off` 关闭休眠和 Fast Startup，并在 Sysprep 前阻止自动设备加密、关闭 BitLocker、等待系统盘 `FullyDecrypted`。
- Windows 构建的 `product_key` 是仓库外敏感变量；可使用 Microsoft 公布的 GVLK 选择 ISO 版本，但模板激活和许可证合规仍由部署方单独负责。
- node4 现有 `Win10_22H2_Chinese_Simplified_x64v1.iso` 的 SHA-256 为 `d485d370406cbcb68959718817bd12ed87c537a14c885f84962e07136fc4a049`；`virtio-win-0.1.271.iso` 的 SHA-256 为 `0040e268e1095b080abfec74214d094bc7fe565568533505b99d70622061c187`。两者由临时 VM 9199 以只读 CD-ROM 块设备计算并用 hexdump 复核，随后 VM 9199 已删除。
- Windows amd64 Guest Agent 由 CI 的 Windows Runner 产出。本次实机构建产物 SHA-256 为 `b5d7e3b39e98a996a63393c5ff73ff00d15e43bf6203d17bddf8f29ae2f46a5d`。
- Cloudbase-Init 固定为 1.1.8 x64，运行 `deploy/images/windows-client/fetch-cloudbase-init.sh dist/CloudbaseInitSetup.msi` 下载并校验；MSI SHA-256 为 `0e7fa42e0cbc0ce7657f85730b0c6cc7afc4087a3639df0ff51a721a0be19bd5`。
- 控制面签发连接前会依次检查 Linux `/var/lib/vc-vdi/desktop-ready` 与 Windows `C:\ProgramData\VC Workspace\Agent\desktop-ready`；为兼容既有镜像仍回退读取 `C:\ProgramData\VC VDI\Agent\desktop-ready`。只有内容为 `ready` 时才轮换本地 `vdi` 密码并返回 RDP 描述符。
- 管理后台将镜像切换为 `ready`、启用镜像或修改已就绪镜像的 VMID/源节点时，会实时读取 PVE 清单；VMID 不存在、不是 QEMU 模板或与源节点不一致都会被拒绝，避免数据库状态与集群漂移。
- 管理后台可以直接启动镜像 Bootstrap。控制面先校验构建节点在线、存储存在且支持 `images`、目标 VMID 未占用，再启动固定的 Debian 13 或 Windows 10/11 Packer 定义。同一镜像同一时间只允许一个构建 Job；Packer 输出脱敏后持久化为进度，成功后状态进入 `testing`，仍需克隆、QGA/Agent、RDP 和授权验收后才可人工设为 `ready`。
- 构建密码和 Windows 产品密钥只通过单次 HTTPS 请求进入构建进程环境，不写入镜像配置、Job request、审计详情或命令行。生产环境必须使用独立的最小权限 PVE API Token，并把构建执行器放在受控主机；当前 MVP 执行器随控制面进程运行，服务重启会中止当前 Packer 子进程，后续再拆分为可恢复 Worker。
- 可在不启动或修改虚拟机的情况下重复审计三套正式模板。该检查验证 VMID 9100/9110/9111 的模板/停止状态、QEMU Agent 标志、CPU/内存、磁盘、网络、`vc-vdi` 兼容标签，并额外验证 Windows 的 OVMF、EFI 与 TPM 2.0；基础模板不得预先绑定 `hostpci`：

```bash
VC_VDI_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_VDI_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
make images-live-check
```

Windows 10 技术构建管线和 VMID 9110 已验收，管理后台状态为 `ready`，但普通 Home/Pro 22H2 已结束支持，因此配置继续保持停用；生产启用前必须换用满足 LTSC 或 ESU 生命周期且授权合规的介质。Windows 11 VMID 9111 同样已验收并在管理后台标记为 `ready`，但 Evaluation 介质只用于技术验证，配置保持停用；生产启用前必须替换为组织已授权介质并重建模板。

## GPU 基线

2026-09-02 对真实集群只读验收确认 PVE 为 `9.2.10`，6 个在线节点均检测到 Intel HD Graphics 530（`8086:1912`，`0000:00:02.0`）。完整直通侧仍报告 `iommugroup=0`，因此不能通过 VFIO 门禁；但每个节点的 `/hardware/pci/0000:00:02.0/mdev` 都实际返回 `i915-GVTg_V5_4`（1920×1200，当前可用 1 个）和 `i915-GVTg_V5_8`（1024×768，当前可用 2 个）。这两种能力必须分开判断，不能再用 IOMMU 结果否定 GVT-g。

控制面已经实现 PVE `/cluster/mapping/pci` 读取、创建、更新、逻辑映射配置和两类调度门禁。PVE 映射属性中 `path` 是 PCI BDF，`id` 是 `vendor:device` 硬件身份，`subsystem-id` 是 OEM 子系统身份；控制面分别解析并校验。完整直通映射要求有效 IOMMU group；mediated 映射要求 `mdev=1`，目标设备实时暴露档位指定的类型且剩余实例大于 0。真实集群已创建六节点 mediated 映射 `vc-vdi-intel-gvtg`；两个 GVT-g 档位仍默认停用，由平台管理员选择映射并明确启用。

六个节点的只读 GPU 身份审计确认均为 `8086:1912`、OEM subsystem `103c:806a`，当前仍报告 `iommugroup=0` 且不可分配。维护前后可重复运行：

```bash
VC_VDI_LIVE_PVE_ENDPOINT=https://pve.example.com \
VC_VDI_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/credential \
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
5. 人工复核备份目录和网络/控制台回退条件后再重启。若主机启动异常，使用 `deploy/pve/gpu-passthrough-host-rollback.sh /root/vc-vdi-gpu-backup-TIMESTAMP --apply` 恢复配置并再次人工重启。
6. 重启成功后设置 `VC_VDI_OUT_OF_BAND_CONSOLE_CONFIRMED=true`，依次运行 `deploy/pve/gpu-passthrough-host-audit.sh` 与 `deploy/pve/gpu-passthrough-preflight.sh 0000:00:02.0`；只有显式 IOMMU、单设备 sysfs group、`vfio-pci`/未绑定、无运行 VM/CT 和带外控制台确认全部通过，才建立集群级 PCI Resource Mapping。
7. 在管理后台编辑 Intel iGPU 档位，展开“创建 PVE 映射”，从可直通设备清单选择 node4 的 `0000:00:02.0` 并创建 `vc-vdi-intel-igpu`；随后选择该映射并启用档位。控制面会使用实时清单写入正确的 `path=0000:00:02.0,id=8086:1912,subsystem-id=103c:806a,iommugroup=...`，调度器固定到 node4，克隆完成后自动配置 `hostpci0`。先用 Windows 11 验收克隆完成冷启动、Intel Guest 驱动、RDP 图形会话、正常关机与再次冷启动；PVE noVNC/SPICE 不作为 GPU 桌面的验收通道。

以上宿主机排空、VFIO 和重启步骤仅属于完整直通备选路径，目前尚未执行；GVT-g 路径不应运行这些脚本，否则会把 iGPU 从 `i915` 移走并破坏 mdev 能力。

## 本地配置

```dotenv
VC_VDI_PVE_ENDPOINT=https://pve.example.com
VC_VDI_PVE_CREDENTIAL_FILE=/absolute/path/to/credential
VC_VDI_PVE_MUTATIONS_ENABLED=false

# Web 镜像 Bootstrap（需要上面的写入开关）
VC_VDI_IMAGE_BUILDER_ENABLED=true
VC_VDI_PACKER_PATH=packer
VC_VDI_IMAGE_ROOT=deploy/images
VC_VDI_IMAGE_BUILDER_PVE_TOKEN_ID=builder@pve!packer
VC_VDI_IMAGE_BUILDER_PVE_TOKEN_SECRET=replace-me
VC_VDI_LINUX_AGENT_BINARY=dist/vc-vdi-guest-agent-linux-amd64
VC_VDI_WINDOWS_AGENT_BINARY=dist/vc-vdi-guest-agent-windows-amd64.exe
VC_VDI_CLOUDBASE_INIT_MSI=dist/CloudbaseInitSetup.msi
```

凭证文件开发格式为：

```text
username@realm / password
```

生产部署应改用最小权限、权限分离的 PVE API Token。
