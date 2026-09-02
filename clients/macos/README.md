# macOS Client

Tier‑1 SwiftUI/AppKit 客户端。当前支持本地账号和 OIDC 登录、按控制面地址绑定的 Keychain Session、授权桌面列表、`vc-vdi://connect?vmid=<VMID>` App Link、启动与就绪等待，以及应用内 RDP 会话。

## 原生数据面

客户端不启动 `sdl-freerdp` 或其他桌面应用。`EmbeddedRDP.swift` 从当前 `.app/Contents/Frameworks` 加载仓库内 C ABI Bridge；Bridge 使用 FreeRDP 原生 Mac Client 创建 `MRDPView`，SwiftUI 通过 `NSViewRepresentable` 把它放入当前窗口。连接、失败、断开、全屏与应用退出共享同一个生命周期。Bridge 在原生连接进入 Active 后补齐内嵌视图的 responder/剪贴板生命周期，并把实时容器尺寸同步给 FreeRDP 的 Smart Sizing 坐标换算，保证窗口缩放后键鼠仍与画面对齐。

密码直接写入当前进程的 FreeRDP settings，不进入进程参数、环境变量或文件。交互客户端固定使用自适应策略，由 FreeRDP 进行网络自动检测和画面缩放；带宽参数保留为受管策略与诊断能力，不在普通用户的桌面库中展示。

## 构建

构建机需要 Swift 6、CMake 和 Ninja。脚本会自行构建固定的 OpenSSL LTS；最终用户不需要这些工具，也不需要 Homebrew、OpenSSL 或 FreeRDP。

```bash
./scripts/build-app.sh release
```

脚本下载 FreeRDP 3.31.0 与 OpenSSL 3.5.8 LTS 源码并校验 SHA-256，先幂等应用仓库内经过审计的 Mac 剪贴板补丁，再面向 macOS 14 构建客户端所需的最小功能集，最后把 Bridge、FreeRDP、WinPR、OpenSSL、provider、证书对话框和第三方许可证写入并签名：

```text
.build/app/VC Workspace.app
```

开发时可用 `VC_WORKSPACE_FREERDP_SOURCE` 与 `VC_WORKSPACE_FREERDP_BUILD_DIR` 复用本地源码和构建目录；这两个变量只影响构建，不会成为运行时依赖。

## 验证

```bash
swift test --disable-sandbox
./scripts/verify-app.sh
```

验证脚本检查深度签名、所有 Mach-O 的 macOS 14 最低版本，并拒绝指向 Homebrew、MacPorts、`/usr/local`、用户目录或临时构建目录的动态库依赖和 RPATH。真实验收仍必须从 `.app` 启动，覆盖本地登录与 Keychain 恢复、桌面卡片与搜索、停止桌面的启动链路、运行中桌面的直接连接、Debian 与 Windows 图像/键鼠、自适应网络、剪贴板、最小/默认/宽窗口缩放、全屏往返、关闭/隐藏/最小化后由 Dock 恢复、主动断开和显式退出清理。单元测试或不可达端点冒烟测试不能替代真实桌面验收。
