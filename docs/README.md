# 文档索引

文档按“一个主题一个当前事实来源”维护：

- [项目状态与 TODO](plan/status.md)：当前阶段、能力矩阵、优先级、完成条件和外部决策。
- [MVP 开发计划](plan/mvp.md)：范围、里程碑退出条件、实施顺序和实机证据。
- [系统架构](architecture/overview.md)：组件边界、数据流和技术选型。
- [身份与访问](architecture/identity.md)：本地用户、OIDC、会话和初始化。
- [审计](architecture/audit.md)：事件模型、查询、脱敏与生产加固边界。
- [AI Agent 桌面](architecture/agent-desktop.md)：MCP、Lease、控制权和审计。
- [UI 设计语言](design/ui.md)：管理端与客户端共同遵守的视觉和交互规则。
- [PVE 开发环境](operations/pve-development.md)：安全接入、只读验证与受控写入。
- [Kubernetes 部署](operations/kubernetes.md)：容器、Kustomize、Secret、探针、网络策略和协议端口。
- [ADR-0001 技术栈与模块边界](decisions/0001-stack-and-boundaries.md)。
- [ADR-0002 会话策略与防泄漏边界](decisions/0002-session-policy-enforcement.md)：剪贴板、水印、桌面背景和策略强制执行。

旧的调研长文已经并入上述当前态文档，不继续维护多份路线图或实施顺序。任务状态只在 `plan/status.md` 更新；架构和运维文档不复制 TODO。
