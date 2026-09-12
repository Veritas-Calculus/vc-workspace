# Kubernetes 部署

当前部署为 `kubernetes-admin@infra.homelab` 集群的独立 `vc-workspace` 命名空间，入口 [ws.infra.plz.ac](https://ws.infra.plz.ac)。`overlays/infra` 已真实 rollout，使用私有 Harbor 的 amd64 Digest、cert-manager 签发的公开证书及 Ceph PVC；不是生产就绪声明。验收证据和剩余条件只在 [DEPLOY-01 / NET-01](../plan/status.md#todo) 跟踪。

## 部署边界

Kubernetes 承载 Web、Go 控制面、远程 MCP、Session Gateway 和 PostgreSQL。公开入口只暴露一个 HTTPS 主机；Web 容器在同源下把 `/api/` 转发给控制面、把 `/mcp` 转发给 MCP。infra Overlay 的精确 `/gateway/v1/rdp` 路径直接进入网关，控制面、mTLS 控制接口和数据库不直接暴露到集群外。

`deploy/kubernetes/base/` 是可直接渲染的 Kustomize 基线：Web 与 MCP 各两个副本，控制面一个副本，PostgreSQL 一个带 PVC 的 StatefulSet。所有应用 Pod 禁用 Service Account Token、禁止提权、丢弃 Linux capabilities 并使用只读根文件系统；NetworkPolicy 默认拒绝后按组件开放。

控制面当前保持单副本：数据库迁移可以重复执行，但 PVE Job 收敛仍由进程内 watcher 承担，还没有 leader election。Web 和无状态 MCP 可以横向扩展。仓库内 PostgreSQL 适合 Bootstrap；生产环境应替换为已备份的高可用 PostgreSQL，并从清单移除 StatefulSet。

Native/Computer Use 已共用数据库级单桌面锁，租约吊销有事务性 Guest 重试队列；这只解决桌面控制一致性，不解除上述 Job/Builder 的单副本限制。控制面另有一个不保留最小空闲连接的锁连接池，上限为 `min(pool_max_conns, 4)`；数据库连接预算需在业务池之外为每个控制面副本预留这些连接，避免长时间的 Guest 操作饿死授权查询。

升级包含 025/026 迁移的版本时，不使用新旧控制面重叠运行的滚动升级。先暂停 Agent 入口、排空旧租约及 Helper、停止旧控制面并备份数据库，再按 [MCP 协同升级次序](../../apps/mcp/README.md) 更新 Guest 与控制面。026 拒绝旧程序清空已绑定 UID 的写法；直接回退旧镜像不构成可用回滚，恢复数据库也不能倒退 Guest 栅栏。目标环境的备份/恢复和升级回滚仍须单独验收。

## 构建与应用

从仓库根目录构建镜像。Apple Silicon 到 amd64 集群使用 `buildx --platform linux/amd64`；构建阶段运行于 `BUILDPLATFORM`，Go 显式交叉编译，最终镜像采用目标架构，不依赖本机 x86 模拟器：

```bash
docker build -f deploy/container/control-plane.Dockerfile -t <registry>/vc-workspace-control-plane:<tag> .
docker build -f deploy/container/mcp.Dockerfile -t <registry>/vc-workspace-mcp:<tag> .
docker build -f deploy/container/web.Dockerfile -t <registry>/vc-workspace-web:<tag> .
docker build -f deploy/container/session-gateway.Dockerfile -t <registry>/vc-workspace-session-gateway:<tag> .
```

部署前必须完成以下替换：

1. 把 `deploy/kubernetes/secret.example.yaml` 复制到仓库外的受保护位置，生成 PostgreSQL、Setup、内部服务和 PVE Token；数据库密码与两个服务 Secret 中的内部服务 Token 必须分别保持一致。Agent 凭证由 Web“访问控制”逐个签发，不写入 MCP Deployment Secret。
2. 修改 `config.yaml` 的公开地址与 PVE Endpoint；公开地址必须和 Ingress Host、TLS 证书及 OIDC Redirect URI 一致。若 IdP 的组 Claim 不是 `groups`，同时修改 `VC_WORKSPACE_OIDC_GROUPS_CLAIM`。
3. 用 Kustomize 的 `images` 字段改成已经推送的镜像仓库与不可变 Tag 或 Digest。
4. 按集群设置 `ingressClassName`、证书 Secret 和 StorageClass；集群没有支持 NetworkPolicy 的 CNI 时，清单存在但不会产生隔离效果。

SSSD OIDC 与 Windows Entra RDP 是默认关闭的实验能力。只有在对应 Guest 模板、外部目录/IdP 及客户端数据面完成真实验收后，才在环境 Overlay 中把 `VC_WORKSPACE_EXPERIMENTAL_SSSD_OIDC_ENABLED` 或 `VC_WORKSPACE_EXPERIMENTAL_WINDOWS_ENTRA_RDP_ENABLED` 设为 `true`；不要修改 base 默认值。域加入密码和 OIDC Client Secret 由管理员在单次收敛请求中输入，不应进入 Kubernetes Secret、ConfigMap、日志或数据库。

应用顺序：

```bash
kubectl --context <target-context> apply -f deploy/kubernetes/base/namespace.yaml
kubectl --context <target-context> apply -f /secure/path/vc-workspace-secrets.yaml
kubectl --context <target-context> apply -k deploy/kubernetes/base
kubectl --context <target-context> -n vc-workspace rollout status deployment/vc-workspace-control-plane
kubectl --context <target-context> -n vc-workspace rollout status deployment/vc-workspace-mcp
kubectl --context <target-context> -n vc-workspace rollout status deployment/vc-workspace-web
```

`make k8s-check` 会离线渲染 base/infra Overlay、Secret/恢复模板，并回归私有文件、网关身份、Digest、TLS 和隔离配置；需要 kubectl、Node.js 和 Ruby/YAML。它不执行真实 rollout 或网络验收。

已部署环境的无凭据、只读边缘检查使用 `node deploy/kubernetes/infra-smoke.mjs`：验证系统信任的 TLS 1.3、HTTP 跳转、API/MCP/内部路径状态以及 WSS 无效票据拒绝。它不生成桌面票据，不代表 OS 桌面验收。

真实 Mac 桌面验收的环境专用夹具为 `tools/test-infra-gateway.mjs`，必须显式设置 `VC_WORKSPACE_LIVE_INFRA_MAC_LAB=true`，目标固定为隔离 VM160；它不是通用安装器或可盲目重复执行的 smoke test。夹具通过普通 API 创建/分配测试主体，桌面登录和重连由真实 `.app` 完成；Guest 操作使用受限 QGA，单次 stdin 不超过 64 KiB，未知执行结果只查询、不重发。私有操作日志与状态保存在 `.cache/infra-gateway-lab/`（0700/0600），不能提交或公开。清理核验账号、进程、摘要和撤权回执后恢复 Guest，平台禁用身份与审计保留；再次验收前须检查历史 UID 绑定和已清理状态，不得通过删除身份栅栏来复用 UID。具体通过项与失败项只记录在状态页。

## infra 环境的部署与维护

`deploy/kubernetes/overlays/infra/` 固定本次授权的集群环境；命令始终显式传入 context，不修改默认 context。现有默认 context 可能属于其他项目，不能省略。首次安装流程为：

```bash
kubectl --context kubernetes-admin@infra.homelab create -f deploy/kubernetes/base/namespace.yaml
kubectl --context kubernetes-admin@infra.homelab -n vc-workspace apply -f deploy/kubernetes/overlays/infra/certificates.yaml
# 等待 cert-manager 的公开及内部 Certificate Ready，再准备秘密。
VC_WORKSPACE_DEPLOY_INFRA=true node deploy/kubernetes/infra-bootstrap.mjs
kubectl --context kubernetes-admin@infra.homelab apply -k deploy/kubernetes/overlays/infra
```

Bootstrap 是本次环境专用工具，不是跨环境自动安装器。它读取 `VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE` 指向的凭据文件和 Docker 中已有的 `harbor.infra.plz.ac` 登录，创建 90 天、仅本项目 pull 的 Harbor Robot，以及权限分离的 PVE Token。PVE Token 只允许基础设施元数据读取和 VM 160 的 Guest Agent/VM 审计；独立验证 VM 158 的配置读取返回 403，不授予启停、克隆、配置或宿主权限。PVE 原密码不会上传 Kubernetes。当前 Guest 地址经 QGA 与固定网卡身份核验为 `10.31.0.168/32`；变更 VM、IP 或权限必须重新审核，不能改成整个内网白名单。

Guest 使用 DHCP，2026-09-07 两次冷启动后地址依次由 `.166` 变为 `.167`、`.168`，均经固定网卡核验与精确路由维护。不要重跑 Bootstrap、保留旧 IP 或扩大白名单来处理漂移。隔离环境的维护入口如下；正式环境仍应配置地址保留和受控端点生命周期：

```bash
VC_WORKSPACE_DEPLOY_INFRA=true node deploy/kubernetes/infra-target.mjs 10.31.0.168
```

该操作要求没有活动隧道，核验 VM 160 的名称、内存、固定 MAC 和唯一 IPv4，再核验自有 ConfigMap/NetworkPolicy 的旧精确 `/32`。操作日志先落入私有目录，使用 resourceVersion 条件更新网络策略与路由，然后依次重启控制面和 Gateway。它不会修改 Guest 网络或自动跟随任意 DHCP 地址；维护期间可能无法新建连接，不是无中断切换。若中途失败，先检查 `target-*.json` 和两项资源/rollout 状态，不得盲目重跑或回滚到可能已被其他设备占用的旧地址。真实 Mac 实测夹具在发放新授权前也会验证此绑定；清理和只读检查不因地址漂移被禁用。

初始化管理员和恢复所需秘密只保留在被 Git/Docker 排除的 `.cache/infra-bootstrap/`（目录 0700、文件 0600）及专用 Kubernetes Secret；不能把这些文件提交仓库或贴进日志。Bootstrap 使用 server-side apply，不把秘密复制到 `last-applied-configuration`。已有不归本工具所有的 Secret/ConfigMap、缺失私有状态的同名 PVE Token、变更的证书指纹均拒绝盲目覆盖。首次管理员通过 `/api/v1/setup/admin` 的 Setup Token 初始化，之后入口拒绝再次创建初始管理员。

公网证书使用既有 `cloudflare` ClusterIssuer（Let's Encrypt DNS-01）。内部 CA、网关服务证书、控制服务证书与网关客户端证书都由该命名空间的 cert-manager 资源管理。Ingress 仅持有内部 CA 公钥，不取得网关的 mTLS 客户端私钥；上游强制 TLS 1.3、主机名校验和链验证。配置依据：[Ingress 上游证书验证](https://kubernetes.github.io/ingress-nginx/user-guide/nginx-configuration/annotations/#backend-certificate-authentication)、[cert-manager Certificate](https://cert-manager.io/docs/usage/certificate/)。不修改共享 Ingress Controller 的全局配置，也不在 Mac 上安装测试 CA。

### 证书轮换

infra 控制面/Gateway 已部署支持热重载的 Digest。2026-09-07 使用 cert-manager 实际续签客户端和两端服务叶证书，验证新服务叶证书、旧客户端 Keep-Alive 拒绝及两个 Pod 的 UID/重启计数不变；后续已在正式 Mac→Gateway→Debian 的同一条活动桌面连接中复验，轮换后实际输入和原 Guest 会话保留。根 CA、应急恢复和完整故障矩阵仍未验收；完整证据只在 [DEPLOY-01](../plan/status.md#todo) 维护。

新版本会从原挂载路径重载证书/密钥/CA，控制面按请求重新检查 CA 和登记指纹，Gateway 在自身客户端证书或控制 CA 变化时替换内部 HTTP 连接池，WSS 数据通道不因成功重载而主动重建。损坏或过期材料拒绝服务，不继续采用已撤回的旧配置。具体安全边界和回归入口见 [Gateway 证书重载](../architecture/session-gateway.md#证书与信任重载)。

客户端签发材料 `vc-workspace-gateway-client` 由 cert-manager 管理；Gateway 只挂载独立的 `vc-workspace-gateway-active-client`。签发不会直接改变活动身份。新安装 Bootstrap 同时创建活动 Secret；已有部署首次迁移时使用 `initialize`，只允许复制已登记的同一身份，再升级 Overlay，不能通过重跑整个 Bootstrap 来处理证书变化。

```bash
VC_WORKSPACE_DEPLOY_INFRA=true node deploy/kubernetes/infra-certificates.mjs status
# 仅首次接入活动 Secret 时需要；不触发签发或重启。
VC_WORKSPACE_DEPLOY_INFRA=true node deploy/kubernetes/infra-certificates.mjs initialize
# cert-manager 有新的 Ready 客户端证书后，显式推广。
VC_WORKSPACE_DEPLOY_INFRA=true node deploy/kubernetes/infra-certificates.mjs promote
# 只读验证实际运行进程呈现的服务证书与当前 Secret 一致。
VC_WORKSPACE_DEPLOY_INFRA=true node deploy/kubernetes/infra-certificates.mjs verify
```

推广限定一个控制面 Pod、一个 Gateway Pod、固定命名空间/身份/证书名称和相同 CA；核对证书签名、用途、私钥匹配、有效期、资源所有权与 UID。新候选至少还需有效一天。流程先发布双指纹，通过实际 mTLS 验证运行中控制面接纳新旧身份，再条件更新活动 Secret；确认 Gateway 文件投影和 readiness 后撤销旧指纹，并验证旧 Keep-Alive 返回 403、新身份及 Gateway 返回 204。每次写入包含 UID/resourceVersion 条件，不扩大权限或重启 Deployment。地址转发只绑定本机 `127.0.0.1`，TLS 仍验证真正的服务 DNS 和内部 CA，不修改 Mac 信任。

工具只执行显式推广，不负责后台定时调度。`status` 输出候选/活动指纹、到期日和 `promotionPending`，不输出私钥；已完成的推广再次执行为无操作。按需测试签发应使用官方 [cmctl renew](https://cert-manager.io/docs/reference/cmctl/#renew)，明确 `--context kubernetes-admin@infra.homelab --namespace vc-workspace` 和单个证书名，不使用 `--all`。正常自动签发后仍需运维推广与告警；当前不应作为无人维护的生产部署。

中断恢复以 `.cache/infra-bootstrap/certificate-rotation.json` 和实时资源为准。未完成记录含恢复所需旧/新秘密，必须保持 0600；完成后去除秘密，只保留身份、阶段和验收记录。重新执行 `promote` 根据实际单指纹/双指纹及活动身份继续，不盲目重发已成功的写入，也不自动恢复已撤销旧身份。候选再次变化、未知资源/CA、Pod 变更或观察超时会停止并保留进度；先检查再恢复。SIGINT/SIGTERM 尽可能关闭自身转发进程并释放本地锁；进程崩溃/SIGKILL 遗留锁时须独立核对 `certificate-rotation.lock/owner.json` 的进程及实时资源，不能仅按文件年龄删除锁或启动第二个操作者。根 CA、过期身份应急恢复、强制中断矩阵与后台推广告警仍待专项实现/验收。

CA 轮换还需同步 Ingress 的上游信任，不是只改某一 `ca.crt`；该叶证书推广工具明确拒绝跨 CA 切换。

Kubernetes Secret 文件更新是最终一致的；`subPath` 挂载不会自动更新。以文件投影实际可见为边界，不能以 Secret API 写入成功代替各 Pod 的接纳验证。[Kubernetes Secret 投影](https://kubernetes.io/docs/concepts/configuration/secret/#using-secrets-as-files-from-a-pod) 工具每阶段最多观察 180 秒，等待时不会重复触发签发、推广或重启；两次实际客户端推广分别约 141 秒、131 秒，不作为续期 SLA。内部 CA 的实机轮换、PVE/Harbor 的 90 天凭据轮换告警仍未验收。

### 数据备份与恢复检查

```bash
VC_WORKSPACE_DEPLOY_INFRA=true node deploy/kubernetes/infra-backup.mjs backup
kubectl --context kubernetes-admin@infra.homelab create -f deploy/kubernetes/restore-check.yaml
kubectl --context kubernetes-admin@infra.homelab -n vc-workspace wait --for=condition=Ready pod/vc-workspace-restore-check --timeout=45s
VC_WORKSPACE_DEPLOY_INFRA=true node deploy/kubernetes/infra-backup.mjs restore-check /absolute/path/to/private-backup.dump
```

备份是 0600 的 PostgreSQL custom archive，包含敏感身份数据；恢复命令只允许恢复到独立 `vc_workspace_restore` 数据库的空临时 Pod，拒绝 PVC/hostPath 和非空目标。该 Pod 只监听 Unix socket，无 Service，900 秒自动终止，不挂载在线数据库卷。验收后删除这个确切的临时 Pod，保留本地受保护备份。此检查不等同于持续备份：尚未配置远端加密存储、保留期、PITR、自动恢复演练或跨版本升级回滚。

## 协议与端口

| 路径 | 协议/端口 | 必需性 | 说明 |
|---|---:|---|---|
| 用户、macOS 客户端、Agent → Ingress | HTTPS `443/TCP` | 必需 | Landing、Web、控制 API、OIDC Callback 与 `/mcp` 共用；TLS 在 Ingress/Gateway 终止 |
| HTTP → HTTPS | HTTP `80/TCP` | 可选 | 只用于边缘重定向，应用 Pod 不监听 80 |
| Ingress → Web Service | HTTP `80/TCP` → Pod `8080/TCP` | 必需 | ClusterIP；Web 同时承担静态资源与同源反向代理 |
| Web → Control Plane | HTTP `8080/TCP` | 必需 | 集群内 JSON API，不对外创建 Service 类型 LoadBalancer/NodePort |
| Web → MCP | Streamable HTTP/JSON-RPC `8090/TCP` | 远程 MCP 必需 | 外部统一使用 `https://<host>/mcp`；每个 Agent 使用单独签发的 Bearer 凭证，当前为无状态请求，不依赖粘性会话 |
| Control Plane → PostgreSQL | PostgreSQL `5432/TCP` | 必需 | 仅控制面 NetworkPolicy 可访问 |
| Control Plane → PVE | HTTPS `8006/TCP` | 通常必需 | PVE `pveproxy` 默认端口；若前置反向代理则按实际 Endpoint 使用 `443/TCP` |
| Control Plane → OIDC Provider | HTTPS `443/TCP` | 启用 OIDC 时 | Discovery、Authorization 跳转后的 Token/UserInfo 调用；Callback 从公开 `443/TCP` 返回 |
| 应用 Pod → DNS | DNS `53/UDP,TCP` | 必需 | Service、PVE 和 OIDC 名称解析 |
| macOS 客户端 → Desktop Guest | RDP `3389/TCP` | 默认直连部署 | 未启用 Gateway 时使用；infra 的 Native 路由不回退直连 |
| Ingress → Session Gateway | TLS 1.3 `8443/TCP` | infra 网关 | 精确 WSS 路径、校验内部 CA/主机名，保留公开 Host |
| Session Gateway → Control Plane | mTLS 1.3 `8444/TCP` | infra 网关 | 只允许网关 Pod，另外校验已登记叶证书 SHA-256 |
| Session Gateway → Guest | RDP `3389/TCP` | infra 网关 | NetworkPolicy 与进程双重限制 VM 160 的 `/32`；Guest 防绕过规则还需单独验收 |
| Control Plane → QEMU Guest Agent | 无 Guest 网络端口 | 必需 | 通过 PVE API 和 VM 的 virtio-serial 通道执行状态读取、逐用户密码轮换、SSSD/AD Profile 及权限收敛；一次性 Secret 用 QGA stdin 传送 |
| Debian Guest → AD/FreeIPA/LDAP/OIDC | 依目录实现 | 使用企业身份时 | 常见为 DNS `53`、Kerberos `88`、LDAP(S) `389/636`、SMB `445`、Global Catalog `3268/3269` 与 IdP HTTPS `443`；应按实际目录文档和 CIDR 收窄，不由 VC Workspace Ingress 代理 |
| Windows Guest → AD/Entra | 依目录实现 | 使用企业身份时 | AD 需要 DNS/Kerberos/LDAP/SMB/RPC 等域服务端口；Entra Join/认证通常需要 Microsoft HTTPS `443`；由 Guest 网络策略负责，不能只开放 Kubernetes 出口 |
| Intel GVT-g / PCI passthrough | 无新增网络端口 | 可选 | 设备发现和分配都通过 PVE API 完成 |
| 镜像 Builder → Debian Guest | SSH `22/TCP` | 构建 Debian 模板时 | 只在 Packer Provision 阶段开放 |
| 镜像 Builder → Windows Guest | WinRM `5985/TCP` | 构建 Windows 模板时 | 只在受控构建网络开放；模板完成后移除构建规则 |
| Installer Guest → Packer HTTP Server | HTTP `8840–8847/TCP`（可配置） | 构建 Debian 模板时 | 安装机须能反向访问 Builder；有界端口不代表 Pod 已可路由，Kubernetes 基线仍禁用内嵌 Image Builder |
| Control Plane/Builder → 镜像源 | HTTP/HTTPS `80,443/TCP` | 构建时 | 包括内网镜像站；生产 NetworkPolicy 应收窄到实际 CIDR |

`vc-workspace://` 是 macOS 自定义 URL Scheme，不是网络协议端口。PVE VNC/SPICE 只用于运维，不进入最终用户数据面，也不应通过本清单公开。

[Session Gateway](../architecture/session-gateway.md) 的默认开关仍关闭，只有新建 infra 环境显式启用；原本机开发部署不变。infra 已配置重新加密上游，mTLS 控制接口不接受转发证书头代替认证。Windows 新网关路径继续关闭，Guest 3389 不创建公网入口；Kubernetes 出口限制不能代替 Guest 防绕过规则或完整桌面实测。

## Image Builder 边界

基线设置 `VC_WORKSPACE_IMAGE_BUILDER_ENABLED=false`。Debian 安装虚拟机需要反向访问 Packer 的安装 HTTP 服务，普通 ClusterIP 不能直接解决集群外 Guest 到 Pod 的路由。当前配方默认限制为 `8840–8847/TCP`；运维可用 `VC_WORKSPACE_IMAGE_HTTP_PORT_RANGE` 配置至多 32 个连续非特权端口（单端口写为 `18840-18840`）。指定 `VC_WORKSPACE_IMAGE_HTTP_BIND_ADDRESS` 或 `VC_WORKSPACE_IMAGE_HTTP_INTERFACE` 二者之一，不能同时设置；均未设置时绑定所有 IPv4 接口。多网卡机器必须明确验证实际安装机可达的地址。HTTP 内容含一次性构建凭据，只能用于可信、隔离的安装网络，不能公开到 Ingress。

Builder 还需要成套 Guest Agent/PAM、经过审核且校验和匹配的 Debian xrdp 包、Cloudbase-Init 等构建产物及 PVE 专用 Token。生产实现仍需拆成专用 Job/Worker，为 PVE 构建网络提供明确的 LoadBalancer、NodePort 或受控路由，并单独允许 `22/TCP`、`5985/TCP` 和安装 HTTP 范围。完成实机闭环前，不应给控制面 Pod 增加 `hostNetwork` 或放宽全部入站端口。`make images-packer-check` 验证真实 Packer/插件配置，不启动 VM，也不替代路由及 OS 安装验收。

## 探针与网络策略

- 控制面 `/api/v1/health` 只判断进程存活；`/api/v1/ready` 在两秒上限内 Ping PostgreSQL，数据库不可达时返回 503，但不会把临时依赖错误误判为进程死锁。
- MCP `/health` 判断进程存活；`/ready` 跟随控制面的数据库就绪状态。HTTP 请求中的 Agent 凭证由控制面解析为固定身份；Pod 只持有内部服务 Token，工具参数不能覆盖 Agent ID。
- Web `/health` 是静态最小响应。infra Ingress 另有网关精确路径，`/internal/` 与其余 `/gateway/` 不走 SPA fallback。
- base 的外部 `80/443/8006` 规则是通用基线。infra 已收窄控制面出口为数据库、DNS 和 PVE 的 Ingress HTTPS；此环境未启用 OIDC/Builder。启用其他依赖时按目标 CIDR 单独配置，不能重新放开任意出口。

协议依据：[Kubernetes 探针](https://kubernetes.io/docs/concepts/workloads/pods/probes/)、[Kubernetes NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/)、[PVE Administration Guide](https://pve.proxmox.com/pve-docs/pve-admin-guide.pdf)、[MCP Streamable HTTP](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)、[Microsoft RDP 端口](https://learn.microsoft.com/en-us/troubleshoot/windows-server/remote/ports-used-by-rds)。
