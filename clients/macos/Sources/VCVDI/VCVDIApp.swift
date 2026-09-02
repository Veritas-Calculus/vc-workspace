import SwiftUI
import AppKit

@MainActor
private final class AppDelegate: NSObject, NSApplicationDelegate {
    weak var model: AppModel?
    private var mainWindow: NSWindow?

    func registerMainWindow(_ window: NSWindow) {
        guard mainWindow !== window else { return }

        if let mainWindow {
            NotificationCenter.default.removeObserver(
                self,
                name: NSWindow.willCloseNotification,
                object: mainWindow
            )
        }

        mainWindow = window
        window.isReleasedWhenClosed = false
        NotificationCenter.default.addObserver(
            self,
            selector: #selector(mainWindowWillClose(_:)),
            name: NSWindow.willCloseNotification,
            object: window
        )
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        false
    }

    func applicationShouldHandleReopen(
        _ sender: NSApplication,
        hasVisibleWindows flag: Bool
    ) -> Bool {
        guard let window = mainWindow ?? sender.windows.first else { return false }

        sender.unhide(nil)
        if window.isMiniaturized {
            window.deminiaturize(nil)
        }
        window.makeKeyAndOrderFront(nil)
        sender.activate(ignoringOtherApps: true)
        return true
    }

    func applicationWillTerminate(_ notification: Notification) {
        model?.prepareForTermination()
    }

    @objc private func mainWindowWillClose(_ notification: Notification) {
        model?.prepareForWindowClose()
    }
}

@MainActor
private final class MainWindowReaderView: NSView {
    var onWindowAvailable: ((NSWindow) -> Void)?

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        if let window {
            onWindowAvailable?(window)
        }
    }
}

private struct MainWindowReader: NSViewRepresentable {
    let onWindowAvailable: (NSWindow) -> Void

    func makeNSView(context: Context) -> MainWindowReaderView {
        let view = MainWindowReaderView(frame: .zero)
        view.onWindowAvailable = onWindowAvailable
        return view
    }

    func updateNSView(_ view: MainWindowReaderView, context: Context) {
        view.onWindowAvailable = onWindowAvailable
        if let window = view.window {
            onWindowAvailable(window)
        }
    }
}

@main
struct VCVDIApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var model = AppModel()

    var body: some Scene {
        WindowGroup {
            Group {
                switch model.state {
                case .loading: ProgressView().controlSize(.small)
                case .signedOut: LoginView(model: model)
                case .signedIn:
                    if let session = model.activeSession {
                        DesktopSessionView(model: model, session: session)
                    } else {
                        DesktopListView(model: model)
                    }
                }
            }
            .frame(minWidth: 680, minHeight: 460)
            .background {
                MainWindowReader { appDelegate.registerMainWindow($0) }
                    .frame(width: 0, height: 0)
            }
            .task { await model.start() }
            .onAppear { appDelegate.model = model }
            .onOpenURL { model.handleIncomingURL($0) }
        }
        .windowStyle(.hiddenTitleBar)
        .defaultSize(width: 820, height: 560)
    }
}

private struct LoginView: View {
    @ObservedObject var model: AppModel
    @State private var serverSettingsOpen = false

    var body: some View {
        VStack(spacing: 24) {
            HStack(spacing: 10) {
                Image(nsImage: NSApplication.shared.applicationIconImage)
                    .resizable()
                    .interpolation(.high)
                    .frame(width: 44, height: 44)
                Text("VC Workspace").font(.title2.bold())
            }

            if let vmid = model.pendingConnectionVMID {
                Label("登录后将打开 VM \(vmid)", systemImage: "arrow.up.forward.app")
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }

            VStack(alignment: .leading, spacing: 16) {
                fieldLabel("用户名") {
                    TextField("用户名", text: $model.username)
                        .textContentType(.username)
                        .onSubmit { submitLogin() }
                }
                fieldLabel("密码") {
                    SecureField("密码", text: $model.password)
                        .textContentType(.password)
                        .onSubmit { submitLogin() }
                }

                Button("登录") { Task { await model.login() } }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .frame(maxWidth: .infinity)
                    .disabled(model.username.isEmpty || model.password.isEmpty)

                if model.system?.oidcConfigured == true {
                    HStack { Divider(); Text("或").font(.caption).foregroundStyle(.secondary); Divider() }
                    Button("使用 \(model.system?.oidcName ?? "SSO") 登录") { model.loginWithOIDC() }
                        .buttonStyle(.bordered)
                        .controlSize(.large)
                        .frame(maxWidth: .infinity)
                }

                DisclosureGroup("服务器设置", isExpanded: $serverSettingsOpen) {
                    fieldLabel("控制面地址") {
                        TextField("https://vdi.example.com", text: $model.server)
                            .textContentType(.URL)
                            .onSubmit { Task { await model.loadSystem() } }
                    }
                    .padding(.top, 8)
                    Button("检查连接") { Task { await model.loadSystem() } }
                        .buttonStyle(.borderless)
                }

                if !model.errorMessage.isEmpty {
                    Label(model.errorMessage, systemImage: "exclamationmark.triangle")
                        .font(.callout)
                        .foregroundStyle(.red)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .textFieldStyle(.roundedBorder)
        }
        .frame(width: 380)
        .padding(40)
    }

    private func submitLogin() {
        guard !model.username.isEmpty, !model.password.isEmpty else { return }
        Task { await model.login() }
    }

    private func fieldLabel<Content: View>(_ title: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title).font(.caption).foregroundStyle(.secondary)
            content()
        }
    }
}

private struct DesktopListView: View {
    @ObservedObject var model: AppModel
    @State private var searchText = ""

    private let columns = [
        GridItem(.adaptive(minimum: 280, maximum: 380), spacing: 14, alignment: .top),
    ]

    private var filteredDesktops: [Desktop] {
        DesktopLibrary.filtered(model.desktops, query: searchText)
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 12) {
                Text("桌面").font(.title2.bold())
                Spacer()

                TextField("搜索桌面", text: $searchText)
                    .textFieldStyle(.roundedBorder)
                    .frame(width: 220)
                    .accessibilityLabel("搜索桌面")

                Button { Task { await model.loadDesktops() } } label: {
                    if model.isRefreshing { ProgressView().controlSize(.small) } else { Image(systemName: "arrow.clockwise") }
                }
                .accessibilityLabel("刷新桌面")
                .help("刷新")
                .disabled(model.isRefreshing || model.connectingVMID != nil)
                Button("退出登录") { Task { await model.logout() } }
            }
            .padding(.horizontal, 20)
            .padding(.vertical, 14)
            Divider()

            if !model.errorMessage.isEmpty, !model.desktops.isEmpty {
                message(model.errorMessage, symbol: "exclamationmark.triangle", color: .red)
            }
            if !model.sessionMessage.isEmpty {
                message(model.sessionMessage, symbol: "info.circle", color: .secondary)
            }

            if model.desktops.isEmpty && model.isRefreshing {
                ProgressView("正在加载桌面").controlSize(.small).frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if model.desktops.isEmpty && !model.errorMessage.isEmpty {
                ContentUnavailableView {
                    Label("无法加载桌面", systemImage: "exclamationmark.triangle")
                } description: {
                    Text(model.errorMessage)
                } actions: {
                    Button("重试") { Task { await model.loadDesktops() } }
                        .buttonStyle(.borderedProminent)
                        .disabled(model.isRefreshing)
                }
            } else if model.desktops.isEmpty {
                ContentUnavailableView("没有可用桌面", systemImage: "display")
            } else if filteredDesktops.isEmpty {
                ContentUnavailableView {
                    Label("没有匹配的桌面", systemImage: "magnifyingglass")
                } description: {
                    Text("没有找到与“\(searchText)”匹配的桌面")
                } actions: {
                    Button("清除搜索") { searchText = "" }
                }
            } else {
                ScrollView {
                    LazyVGrid(columns: columns, alignment: .leading, spacing: 14) {
                        ForEach(filteredDesktops) { desktop in
                            DesktopCard(model: model, desktop: desktop)
                        }
                    }
                    .padding(20)
                    .frame(maxWidth: .infinity, alignment: .topLeading)
                }
                .background(Color(nsColor: .windowBackgroundColor))
            }
        }
    }

    private func message(_ value: String, symbol: String, color: Color) -> some View {
        Label(value, systemImage: symbol)
            .font(.callout)
            .foregroundStyle(color)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal)
            .padding(.top, 12)
    }

}

private struct DesktopCard: View {
    @ObservedObject var model: AppModel
    let desktop: Desktop

    private var isRunning: Bool { desktop.status == "running" }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(alignment: .top, spacing: 12) {
                Image(systemName: isRunning ? "display.2" : "display")
                    .font(.title2)
                    .foregroundStyle(isRunning ? Color.accentColor : .secondary)
                    .frame(width: 28, height: 28)
                    .accessibilityHidden(true)

                VStack(alignment: .leading, spacing: 4) {
                    Text(desktop.name)
                        .font(.headline)
                        .lineLimit(1)
                        .truncationMode(.tail)
                        .help(desktop.name)
                    Text("VM \(desktop.vmid) · \(desktop.node)")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .help("VM \(desktop.vmid) · \(desktop.node)")
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                status
            }

            HStack(spacing: 16) {
                Label("\(desktop.cpuCount) 核", systemImage: "cpu")
                Label(formatMemory(desktop.memoryTotal), systemImage: "memorychip")
            }
            .font(.caption)
            .foregroundStyle(.secondary)

            Divider()

            action
                .frame(maxWidth: .infinity, minHeight: 28, alignment: .trailing)
        }
        .padding(16)
        .frame(maxWidth: 380, minHeight: 166, alignment: .topLeading)
        .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 10))
        .overlay {
            RoundedRectangle(cornerRadius: 10)
                .stroke(Color(nsColor: .separatorColor), lineWidth: 1)
        }
        .accessibilityElement(children: .contain)
    }

    private var status: some View {
        Text(DesktopLibrary.statusTitle(desktop.status))
            .font(.caption.weight(.medium))
            .foregroundStyle(isRunning ? Color.accentColor : .secondary)
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(.quaternary, in: Capsule())
    }

    @ViewBuilder
    private var action: some View {
        if model.connectingVMID == desktop.vmid {
            HStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text(model.connectionStatus)
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                Button("取消") { model.cancelConnection() }
                    .buttonStyle(.borderless)
            }
        } else {
            Button(isRunning ? "连接" : "启动并连接") {
                model.beginConnection(to: desktop)
            }
            .buttonStyle(.borderedProminent)
            .disabled(model.connectingVMID != nil)
        }
    }

    private func formatMemory(_ bytes: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: bytes, countStyle: .memory)
    }
}

private struct DesktopSessionView: View {
    @ObservedObject var model: AppModel
    @ObservedObject var session: EmbeddedRDPSession
    @State private var isFullScreen = false

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 12) {
                Button {
                    model.disconnectSession(for: session.vmid)
                } label: {
                    Label("断开", systemImage: "chevron.left")
                }
                .buttonStyle(.borderless)

                Divider().frame(height: 18)
                Text(session.desktopName).fontWeight(.medium)
                status
                Spacer()
                Button {
                    (session.view.window ?? NSApplication.shared.keyWindow)?.toggleFullScreen(nil)
                } label: {
                    Image(systemName: isFullScreen ? "arrow.down.right.and.arrow.up.left" : "arrow.up.left.and.arrow.down.right")
                }
                .buttonStyle(.borderless)
                .help(isFullScreen ? "退出全屏" : "全屏")
                .accessibilityLabel(isFullScreen ? "退出全屏" : "全屏")
            }
            .padding(.horizontal, 14)
            .frame(height: 42)
            .background(.bar)

            ZStack {
                Color.black
                EmbeddedRDPView(remoteView: session.view)
                if session.state == .connecting {
                    ProgressView("正在建立安全连接")
                        .padding(18)
                        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 10))
                }
            }
        }
        .onAppear {
            isFullScreen = session.view.window?.styleMask.contains(.fullScreen) == true
        }
        .onReceive(NotificationCenter.default.publisher(for: NSWindow.didEnterFullScreenNotification)) { _ in
            isFullScreen = true
        }
        .onReceive(NotificationCenter.default.publisher(for: NSWindow.didExitFullScreenNotification)) { _ in
            isFullScreen = false
        }
    }

    @ViewBuilder
    private var status: some View {
        switch session.state {
        case .connecting:
            Text("连接中").foregroundStyle(.secondary)
        case .connected:
            Text("已连接").foregroundStyle(.secondary)
        case .closed, .failed:
            EmptyView()
        }
    }
}
