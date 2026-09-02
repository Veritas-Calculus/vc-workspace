# Windows Client

状态：未开始，由 [CLIENT-01](../../docs/plan/status.md#todo) 跟踪。

计划使用 WinUI 实现原生 UI，复用 Rust Session Core 的协议和策略边界。最低闭环包括本地/OIDC 登录、Windows Credential Manager、桌面卡片与搜索、App Link、RDP 数据面、全屏/缩放、弱网恢复、会话策略和签名安装包；实现前不把当前目录描述为可用客户端。
