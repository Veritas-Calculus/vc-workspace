# ADR-0004：IaC 控制面边界

| 字段 | 值 |
|---|---|
| 状态 | Accepted |
| 实现 | Initial slice（M25） |
| 日期 | 2026-09-05 |

## 决定

VC Workspace 同时支持两层 IaC，但不把它们混成一套状态：

1. 部署层 IaC 管理 Kubernetes Namespace、Workload、Service、Ingress、NetworkPolicy、PostgreSQL 和 Secret 引用。当前事实源是 `deploy/kubernetes/base/` 的 Kustomize 清单。
2. 平台资源 IaC 管理 VC Workspace 自己拥有的桌面、授权、镜像、GPU 和策略对象。官方 Provider 必须调用与 Web 相同的公开控制面 API，复用权限、校验、幂等和审计，不得直连数据库或持有 PVE 凭证。

首个 Provider 位于 `tools/terraform-provider-vcworkspace/`，使用 Terraform Plugin Framework。它当前提供：

- `vcworkspace_desktop_assignment`：创建、读取、删除和导入用户、组或 Agent 的显式桌面授权。
- `vcworkspace_desktops`：读取受管桌面注册表，供配置组合和计划阶段使用。

Provider 通过 `VC_WORKSPACE_ENDPOINT` 和 `VC_WORKSPACE_API_TOKEN` 配置。管理员只能从 Web 会话创建最长 90 天的 API 凭证；明文只显示一次，数据库只保存摘要。API 凭证不能继续创建或撤销 API 凭证。Bearer 请求使用服务端同一管理员身份和审计路径，支持的 IaC 写操作不使用浏览器 CSRF Token；鉴权包装器只挂在 OpenAPI 明确声明支持 `apiTokenBearer` 的路由上，不能因为某个 Web Handler 也要求管理员角色而自动扩大访问面。

Provider 资源必须使用稳定 ID，支持导入，并在 `Read` 中从服务端刷新状态，从而让计划识别外部漂移。API Secret 通过环境变量或外部 Secret Store 注入，不写入 HCL；示例不得包含可用凭证。

## 兼容性

Terraform Plugin Framework 是 HashiCorp 当前推荐的新 Provider 开发框架，并支持 Provider Protocol 5/6；OpenTofu 公开承诺兼容 Provider wire protocol 和标准安装方式。因此同一 Provider 二进制面向 Terraform 与 OpenTofu，不维护两套实现。

发布前 Provider 仍是源码内开发工具，不宣称已经进入 Terraform Registry 或 OpenTofu Registry。首次正式发布必须固定 Provider 版本、生成校验和、签名产物并在示例中约束版本。

参考：

- [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework)
- [Terraform Resource Import](https://developer.hashicorp.com/terraform/plugin/framework/resources/import)
- [OpenTofu Providers](https://opentofu.org/docs/language/providers/)
- [OpenTofu v1.x Compatibility Promises](https://opentofu.org/docs/language/v1-compatibility-promises/)

## 安全与所有权

- PVE 仍是外部虚拟化系统；需要管理宿主、存储或网络时应使用独立的 PVE Provider，不让 VC Workspace Provider 绕过 Broker。
- API Token 当前在 IaC 路由允许列表内继承创建者的平台管理员身份。按 Token 的细粒度 Scope、审计中的 Credential ID 和紧急全局吊销属于生产加固项。
- 密码、OIDC Client Secret、域加入 Secret、一次性 RDP 凭证和构建密码不成为 Provider 资源，也不进入 Terraform State。
- 删除桌面、模板或 GPU 映射等破坏性资源在进入 Provider 前必须先具备服务端保护、可恢复语义和明确导入/漂移模型。

## 后续资源顺序

下一批按服务端 API 完整度推进：桌面生命周期与访问模式、会话策略、身份组与成员、Guest Identity Profile、镜像配置和 GPU Profile。每个资源必须同时具备 Create/Read/Update/Delete、Import、漂移测试、审计和权限测试，不能只包装现有写接口。

## Alternatives

- 让 Terraform 直接读写 PostgreSQL：会绕过业务约束、审计和撤销链路，拒绝。
- 把 PVE Token 交给 VC Workspace Provider：扩大 Secret 和权限边界，且重复 PVE Provider 的职责，拒绝。
- 只提供脚本或通用 HTTP Provider 示例：难以表达稳定状态、导入和漂移，不作为官方接口。
