# ADR-0001：技术栈与模块边界

| 字段 | 值 |
|---|---|
| 状态 | Accepted |
| 实现 | Implemented for MVP |
| 日期 | 2026-09-01 |

## 决定

控制面和 MCP 使用 Go；Web 使用 TypeScript/React；Guest Agent 与跨平台 Session Core 使用 Rust；macOS UI 使用 SwiftUI/AppKit；持久化使用 PostgreSQL。

控制面首先是模块化单体，MCP 与 Session Gateway 因信任和流量边界独立进程化。仓库使用语言各自原生的工作区，而不是把所有模块塞进同一种构建系统。

## 原因

Go 适合 REST、并发任务、PVE Reconciler 和单文件部署。Rust 的收益集中在长期驻留 Agent、FFI、协议、输入和媒体路径；用于普通控制面 CRUD 会增加交付成本。React 适合复杂管理界面，SwiftUI/AppKit 能保留 macOS 键盘、窗口、Keychain 和渲染体验。

## Alternatives

- 全 Rust 服务端：内存安全一致，但 MVP 的认证、管理 API 和运维生态交付更慢。
- Electron 统一客户端：跨平台快，但 macOS 输入、窗口、能耗和远程呈现不是 Tier 1 体验。
- 从微服务起步：部署和一致性成本早于真实扩容需求。

## Not Decided

- 高性能串流最终采用 WebRTC、QUIC 或兼容现有协议。
- Rust Session Core 与 FreeRDP 的具体 FFI 边界。
- 项目最终许可证。
