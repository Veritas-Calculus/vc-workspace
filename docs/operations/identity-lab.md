# 身份集成实验室

`deploy/identity-lab` 提供一个本机、一次性、可重复的身份集成环境，用于在没有企业 AD/FreeIPA/Entra 租户时发现协议与 Guest 配置问题。它是测试设施，不是生产身份服务的部署模板。

## 运行

前置依赖是 Docker、Docker Compose、Go、OpenSSL 与 curl。本机 TCP `18080`、`18081`、`55436` 必须空闲。

```bash
make identity-lab-check
```

脚本同时兼容 `docker compose` v2 和 `docker-compose` v1。受限网络可以为两个 Debian 13 测试镜像指定内部软件源；没有目标架构 Go 工具链时，也可以传入由 `go test -c` 生成的同包测试二进制：

```bash
VC_WORKSPACE_LAB_DEBIAN_MIRROR=http://10.31.0.2/debian \
VC_WORKSPACE_LAB_DEBIAN_SECURITY_MIRROR=http://10.31.0.2/debian-security \
VC_WORKSPACE_IDENTITY_LAB_TEST_BINARY=/absolute/path/to/httpapi-live.test \
./deploy/identity-lab/check.sh
```

如果 PostgreSQL、Keycloak 和两个 `vc-workspace-identity-lab-*:local` 自建镜像已经按目标架构导入，可额外设置 `VC_WORKSPACE_IDENTITY_LAB_SKIP_BUILD=true` 做无构建复验。这个选项只复用镜像；容器、卷和每轮随机凭据仍会重新创建并在结束时清理。

脚本每次生成新的临时凭据，以权限 `0600` 写入系统临时目录，启动服务，执行测试，并在成功或失败后删除容器、网络、数据卷与临时凭据文件。构建出的本地测试镜像会保留，便于下一次复用缓存。

失败时脚本先打印容器状态和最近日志，再清理环境；日志不打印生成的用户密码、Client Secret 或 PostgreSQL 密码。

## 实际覆盖

实验室启动四个隔离组件：

- 官方 Keycloak `26.7.3` 镜像，启动时导入一次性 realm、机密 Client、用户和组；
- PostgreSQL 17，作为 VC Workspace 的真实状态库；
- 基于 Debian 13 的 OpenLDAP，使用两天有效的临时 CA 和 StartTLS；
- 基于 Debian 13 的 systemd/SSSD/PAM 客户端，模拟受管 Linux Guest。

OIDC 测试运行 VC Workspace 的真实 HTTP Handler 与数据库迁移，覆盖 Discovery、Authorization Code、PKCE、state、nonce、ID Token/JWKS 校验、首次用户绑定、Web Session、两个组的同步、IdP 侧移除组、重复登录，以及本地管理员与 OIDC 用户并存。它还覆盖 macOS 服务端流程的自定义 Scheme 回调、一次性 Code 兑换、重复兑换拒绝、Native Bearer Session 与注销；这验证服务端契约，不等于真实 `.app` 已完成界面和 Open URL 验收。

LDAP 测试直接调用控制面当前的 `linux_sssd_ldap` Guest 收敛计划，并把同一条命令放进 Debian 13 容器执行。验收内容包括：

- `sssd.conf` 通过 Debian 13 的 `sssctl config-check`；
- OpenLDAP StartTLS 证书通过系统 CA Bundle 校验；
- NSS 能解析授权用户、未授权用户和 POSIX 组；
- 授权用户通过 PAM 密码认证、账号检查并由 `oddjob-mkhomedir` 创建 Home；
- 密码正确但不满足 LDAP Access Filter 的用户在账号检查阶段被拒绝。

Keycloak 官方容器支持把 realm JSON 挂载到 `/opt/keycloak/data/import` 并用 `--import-realm` 导入；实验室按这一机制构建。SSSD 2.10 已移除旧的 `config_file_version`，因此 Debian 13 的生成配置不能继续携带该选项。参考 [Keycloak 容器文档](https://www.keycloak.org/server/containers)、[Keycloak Realm 导入文档](https://www.keycloak.org/server/importExport) 与 [SSSD 2.10 发布说明](https://sssd.io/release-notes/sssd-2.10.0.html)。

## PVE amd64 实测

2026-09-05 从集群现有 Debian 12 Cloud-Init 模板 VMID 901 完整克隆了临时验收机 VMID 9300 `vc-workspace-identity-lab-20260905`，位于 `infra-node1`。配置为 2 vCPU、4 GiB 内存、Ceph 30 GiB 系统盘、DHCP、QEMU Guest Agent，并带 `vc-workspace;identity-lab;temporary` 标签；当次地址为 `10.31.0.159`。Cloud-Init、公钥注入、磁盘自动扩容、SSH 和 Guest Agent 均实测通过。

VM 使用 `http://10.31.0.2/debian` 和 `http://10.31.0.2/debian-security` 安装 Docker Engine 20.10.24 与 Compose 1.29.2。提供的 Docker 安装脚本已经先做内容和 `--dry-run` 审阅；它仍会访问该网络无法连接的 `download.docker.com`，因此没有执行会留下半配置仓库的正式安装，而是从既有内部 Debian 源安装发行版软件包。

由于执行端 Mac 未启用 amd64 指令模拟，PostgreSQL 17、Keycloak 26.7.3 和 Debian 13 基础镜像均显式以 `linux/amd64` 拉取、校验架构并离线导入 VM；两个自建镜像随后在真实 amd64 VM 内从 `10.31.0.2` 构建。VC Workspace 测试包以 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c` 生成并在 VM 内运行。

完整构建运行和无构建复验各通过一次。两轮均通过 OIDC 首次/重复登录、组撤权收敛、Native Code 兑换/重放拒绝/注销，以及 LDAP StartTLS、SSSD、NSS、PAM、自动 Home 和 Access Filter 拒绝。每轮结束后均确认测试容器、网络、数据卷、随机凭据和 `18080`、`18081`、`55436` 监听已清理。该 VM 随后被复用为下面的临时 Samba AD 域控；它不是常驻或生产身份服务。

## PVE Guest 直接加入 Samba AD

同日把 VMID 9300 配置为 Samba 4.17 AD DC：域 `ad.vcw.test`、Realm `AD.VCW.TEST`，DNS/Kerberos/LDAP/SMB 分别在内网提供 `53`、`88`、`389`、`445` 等标准服务。Samba 只绑定 `lo` 与 `eth0`；provision 阶段误注册的 Docker 网桥地址 `172.17.0.1` 已删除，`dc1.ad.vcw.test` 最终只解析到当次地址 `10.31.0.159`。`workspace-users` 组包含 Alice，Bob 密码有效但不属于允许组。

另从模板 901 完整克隆独立客户端 VMID 9301 `vc-workspace-ad-client-20260905`，配置 2 vCPU、2 GiB、Ceph 20 GiB、QGA、DHCP 和 `vc-workspace;identity-client;temporary` 标签；当次地址为 `10.31.0.160`。客户端从 `10.31.0.2` 安装 Debian 12 的 `realmd`、`adcli`、SSSD AD Provider、Kerberos、PAM、`oddjob-mkhomedir` 与测试工具。域控和客户端相差 1 秒，SRV 发现从客户端直接通过。

`pve-samba-ad-server.sh` 与 `pve-samba-ad-client.sh` 用于重建实验配置；域加入和授权验收由控制面测试直接通过 PVE QEMU Guest Agent 执行：

```bash
export VC_WORKSPACE_LIVE_PVE_ENDPOINT=https://pve.example.com
export VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE=/absolute/path/to/pve-credential
export VC_WORKSPACE_LIVE_AD_SERVER_VMID=9300
export VC_WORKSPACE_LIVE_AD_CLIENT_VMID=9301
export VC_WORKSPACE_LIVE_AD_DOMAIN=ad.vcw.test
export VC_WORKSPACE_LIVE_AD_REALM=AD.VCW.TEST
export VC_WORKSPACE_LIVE_AD_ALLOWED_GROUP=workspace-users
export VC_WORKSPACE_LIVE_AD_JOIN_USERNAME=administrator
# 在当前安全 Shell 中另行导出 JOIN_PASSWORD、ALICE_PASSWORD 和 BOB_PASSWORD。
make pve-live-ad-identity-check
```

测试带 VMID、名称前缀和 `temporary` 标签三重保护。它使用产品生成的 `linux_sssd_ad` 收敛计划，覆盖首次加域、重复应用、`sssctl config-check`、NSS 用户/组解析、Alice PAM 登录与自动 Home、Bob 的允许组拒绝、Alice Kerberos 取票，以及从域控移除 Alice 后清理 SSSD 缓存并确认 PAM 拒绝，最后恢复组成员并再次确认登录。

首次运行发现并修复了 PVE 输入通道的真实缺陷：PVE REST `agent/exec` 接收原始 `input-data`，并由 PVE 负责对 QGA 再编码；客户端原先提前 Base64，导致 Guest 收到编码文本而不是域密码。重启复验又发现仅写入 `systemd-resolved` 的运行时文件会被 DHCP DNS 覆盖，使 SSSD 落入离线缓存；客户端准备脚本现会持久禁用该解析器并写入域 DNS。修复后首次运行、强制禁用 Go 缓存的幂等复验，以及客户端重启后 DNS/SSSD Online 和完整撤权复验均通过。测试密码没有进入 Guest 命令参数、数据库或测试输出。两台 VM 验收结束后均正常关机并以停止状态保留；DHCP 地址不是稳定配置，复验时应从 QGA 重新读取。

## 不覆盖的边界

一次本机实验不能代替生产身份环境验收：

- Keycloak 开发模式不覆盖生产 TLS、反向代理、密钥轮换、上游身份代理或组织 Claim 规则；
- Samba AD 实验覆盖 Debian 12 Guest 的直接入域和在线组撤权，不等于 Microsoft AD、Debian 13 正式桌面模板、FreeIPA Host Enrollment、离线缓存撤权时限或跨网延迟已验收；
- 不覆盖 Windows AD/Entra 加入和 Windows RDP 域凭据；
- 不覆盖 macOS 客户端中的目录密码、Kerberos 或设备码交互。

FreeIPA 官方提供容器镜像，但其自身说明该镜像运行 systemd 且需要额外的容器和网络处理。FreeIPA、Windows AD 与生产目录的可信验收仍需要稳定的 DNS、时间同步、Kerberos、多端口和可路由主机名，应在独立 PVE 测试网络中完成，而不是通过普通 Docker 端口映射给出“已通过”的错误结论。参考 [FreeIPA 官方容器说明](https://github.com/freeipa/freeipa-container/blob/master/README)。
