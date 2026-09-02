# Kubernetes 部署

当前状态：容器镜像和 Kustomize 基线已经在本机构建/渲染验证，但尚未部署到用户的真实 Kubernetes 集群。目标集群所需的 Registry、域名/TLS、StorageClass、Namespace、Secret 和备份方案由 [DEPLOY-01](../plan/status.md#todo) 跟踪；本页描述部署边界，不能把客户端 dry-run 当作 rollout 完成。

## 部署边界

Kubernetes 承载 Web、Go 控制面、远程 MCP 和 PostgreSQL。公开入口只暴露一个 HTTPS 主机；Web 容器在同源下把 `/api/` 转发给控制面、把 `/mcp` 转发给 MCP。控制面和数据库不直接暴露到集群外。

`deploy/kubernetes/base/` 是可直接渲染的 Kustomize 基线：Web 与 MCP 各两个副本，控制面一个副本，PostgreSQL 一个带 PVC 的 StatefulSet。所有应用 Pod 禁用 Service Account Token、禁止提权、丢弃 Linux capabilities 并使用只读根文件系统；NetworkPolicy 默认拒绝后按组件开放。

控制面当前保持单副本：数据库迁移可以重复执行，但 PVE Job 收敛仍由进程内 watcher 承担，还没有 leader election。Web 和无状态 MCP 可以横向扩展。仓库内 PostgreSQL 适合 Bootstrap；生产环境应替换为已备份的高可用 PostgreSQL，并从清单移除 StatefulSet。

## 构建与应用

从仓库根目录构建三个镜像：

```bash
docker build -f deploy/container/control-plane.Dockerfile -t <registry>/vc-workspace-control-plane:<tag> .
docker build -f deploy/container/mcp.Dockerfile -t <registry>/vc-workspace-mcp:<tag> .
docker build -f deploy/container/web.Dockerfile -t <registry>/vc-workspace-web:<tag> .
```

部署前必须完成以下替换：

1. 把 `deploy/kubernetes/secret.example.yaml` 复制到仓库外的受保护位置，生成 PostgreSQL、Setup、内部服务和 PVE Token；数据库密码与两个服务 Secret 中的内部服务 Token 必须分别保持一致。Agent 凭证由 Web“访问控制”逐个签发，不写入 MCP Deployment Secret。
2. 修改 `config.yaml` 的公开地址与 PVE Endpoint；公开地址必须和 Ingress Host、TLS 证书及 OIDC Redirect URI 一致。
3. 用 Kustomize 的 `images` 字段改成已经推送的镜像仓库与不可变 Tag 或 Digest。
4. 按集群设置 `ingressClassName`、证书 Secret 和 StorageClass；集群没有支持 NetworkPolicy 的 CNI 时，清单存在但不会产生隔离效果。

应用顺序：

```bash
kubectl apply -f deploy/kubernetes/base/namespace.yaml
kubectl apply -f /secure/path/vc-workspace-secrets.yaml
kubectl apply -k deploy/kubernetes/base
kubectl -n vc-workspace rollout status deployment/vc-workspace-control-plane
kubectl -n vc-workspace rollout status deployment/vc-workspace-mcp
kubectl -n vc-workspace rollout status deployment/vc-workspace-web
```

`make k8s-check` 会用本机 `kubectl` 渲染 Kustomize，并对基线和 Secret 模板执行不连接集群的客户端 dry-run。

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
| macOS 客户端 → Desktop Guest | RDP `3389/TCP` | 当前数据面必需 | 不经过 Kubernetes；客户端网络必须能直达 Guest，公网场景先使用 VPN |
| Control Plane → QEMU Guest Agent | 无 Guest 网络端口 | 必需 | 通过 PVE API 和 VM 的 virtio-serial 通道执行状态读取、密码轮换与权限收敛 |
| Intel GVT-g / PCI passthrough | 无新增网络端口 | 可选 | 设备发现和分配都通过 PVE API 完成 |
| 镜像 Builder → Debian Guest | SSH `22/TCP` | 构建 Debian 模板时 | 只在 Packer Provision 阶段开放 |
| 镜像 Builder → Windows Guest | WinRM `5985/TCP` | 构建 Windows 模板时 | 只在受控构建网络开放；模板完成后移除构建规则 |
| Installer Guest → Packer HTTP Server | 动态 HTTP 端口 | 构建 Debian 模板时 | 当前 Packer 配方尚未固定可路由端口，因此 Kubernetes 基线默认禁用内嵌 Image Builder |
| Control Plane/Builder → 镜像源 | HTTP/HTTPS `80,443/TCP` | 构建时 | 包括内网镜像站；生产 NetworkPolicy 应收窄到实际 CIDR |

`vc-vdi://` 是 macOS 自定义 URL Scheme，不是网络协议端口。PVE VNC/SPICE 只用于运维，不进入最终用户数据面，也不应通过本清单公开。

## Image Builder 边界

基线设置 `VC_VDI_IMAGE_BUILDER_ENABLED=false`。原因不是 UI 限制，而是当前 Packer Debian 安装流程会在 Builder 上启动动态 HTTP 服务，PVE 中的安装虚拟机必须反向访问它；普通 ClusterIP 不能直接解决集群外 Guest 到动态 Pod 端口的路由。同时 Builder 还需要 Guest Agent、Cloudbase-Init 等构建产物和 PVE 专用 Token。

生产实现应把镜像构建拆成专用 Job/Worker，固定 Packer HTTP 端口范围，为 PVE 构建网络提供明确的 LoadBalancer、NodePort 或受控路由，并单独允许 `22/TCP`、`5985/TCP` 和该 HTTP 范围。完成这一闭环前，不应给控制面 Pod 增加 `hostNetwork` 或放宽全部入站端口。

## 探针与网络策略

- 控制面 `/api/v1/health` 只判断进程存活；`/api/v1/ready` 在两秒上限内 Ping PostgreSQL，数据库不可达时返回 503，但不会把临时依赖错误误判为进程死锁。
- MCP `/health` 判断进程存活；`/ready` 跟随控制面的数据库就绪状态。HTTP 请求中的 Agent 凭证由控制面解析为固定身份；Pod 只持有内部服务 Token，工具参数不能覆盖 Agent ID。
- Web `/health` 是静态最小响应。Ingress 只指向 Web Service。
- 默认 NetworkPolicy 的外部 `80/443/8006` 规则是可运行基线。生产 Overlay 应替换为 PVE、OIDC 与镜像站的实际 CIDR；若 CNI 不实施 NetworkPolicy，需要在集群防火墙复现相同边界。

协议依据：[Kubernetes 探针](https://kubernetes.io/docs/concepts/workloads/pods/probes/)、[Kubernetes NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/)、[PVE Administration Guide](https://pve.proxmox.com/pve-docs/pve-admin-guide.pdf)、[MCP Streamable HTTP](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)、[Microsoft RDP 端口](https://learn.microsoft.com/en-us/troubleshoot/windows-server/remote/ports-used-by-rds)。
