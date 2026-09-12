import SwiftUI
import AppKit

@MainActor
private final class AppDelegate: NSObject, NSApplicationDelegate, ObservableObject {
    weak var model: AppModel?
    private var mainWindow: NSWindow?
    @Published private(set) var isFullScreen = false

    override init() {
        NSWindow.allowsAutomaticWindowTabbing = false
        super.init()
    }

    func registerMainWindow(_ window: NSWindow) {
        updateWindowChrome(window)
        guard mainWindow !== window else { return }

        if let mainWindow {
            NotificationCenter.default.removeObserver(
                self,
                name: nil,
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
        for name in [NSWindow.didEnterFullScreenNotification, NSWindow.didExitFullScreenNotification] {
            NotificationCenter.default.addObserver(self, selector: #selector(windowModeChanged(_:)), name: name, object: window)
        }
    }

    private func updateWindowChrome(_ window: NSWindow) {
        // SwiftUI rebuilds content during recovery. Keep the system titlebar
        // transparent and its redundant fullscreen controls out of the RDP
        // canvas; the session toolbar and standard menu own Exit Full Screen.
        window.titlebarAppearsTransparent = true
        let fullscreen = window.styleMask.contains(.fullScreen)
        if isFullScreen != fullscreen {
            // Registration may be called by updateNSView. Publish outside the
            // current SwiftUI layout pass, using the current window state.
            Task { @MainActor [weak self, weak window] in
                guard let self, let window, self.mainWindow === window else { return }
                self.isFullScreen = window.styleMask.contains(.fullScreen)
            }
        }
        for button in [NSWindow.ButtonType.closeButton, .miniaturizeButton, .zoomButton] {
            window.standardWindowButton(button)?.isHidden = fullscreen
        }
    }

    @objc private func windowModeChanged(_ notification: Notification) {
        if let window = notification.object as? NSWindow { updateWindowChrome(window) }
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
struct VCWorkspaceApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var model = AppModel()
    @FocusedValue(\.workspaceSearchAction) private var focusDesktopSearch

    var body: some Scene {
        Window("VC Workspace", id: "main") {
            WorkspaceRootView(model: model)
            // A hidden title alone leaves AppKit's separate fullscreen
            // toolbar window over the RDP canvas, even with no NSToolbar.
            .toolbar(appDelegate.isFullScreen ? .hidden : .visible, for: .windowToolbar)
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
        .commands {
            CommandGroup(replacing: .newItem) { }

            CommandGroup(after: .textEditing) {
                Button("搜索桌面") { focusDesktopSearch?() }
                    .keyboardShortcut("f", modifiers: .command)
                    .disabled(focusDesktopSearch == nil || model.state != .signedIn
                              || model.connectionJourney != nil || model.activeSession != nil)
            }

            CommandMenu("连接") {
                Button("刷新桌面") {
                    Task { await model.loadDesktops() }
                }
                .keyboardShortcut("r", modifiers: .command)
                .disabled(
                    model.state != .signedIn
                        || model.isRefreshing
                        || model.connectionJourney != nil
                        || model.activeSession != nil
                )

                Divider()

                if let session = model.activeSession {
                    Button("断开连接") {
                        model.disconnectSession(for: session.vmid)
                    }
                    .keyboardShortcut("d", modifiers: [.command, .shift])
                    .disabled(session.state != .connected)
                } else if let journey = model.connectionJourney {
                    if journey.stage.canCancel {
                        Button("取消连接") { model.cancelConnection() }
                            .keyboardShortcut(".", modifiers: .command)
                    } else if journey.stage.canRetry {
                        Button("重新连接") { model.retryConnection() }
                            .keyboardShortcut("r", modifiers: [.command, .shift])
                        Button("返回桌面库") { model.returnToDesktopLibrary() }
                    } else if journey.stage == .unavailable {
                        Button("返回桌面库") { model.returnToDesktopLibrary() }
                    } else {
                        Button("正在结束会话") { }
                            .disabled(true)
                    }
                } else {
                    Button("断开连接") { }
                        .disabled(true)
                }
            }

            CommandGroup(replacing: .help) {
                Button("VC Workspace 项目主页") {
                    NSWorkspace.shared.open(Self.projectURL)
                }
                Button("报告问题…") {
                    NSWorkspace.shared.open(Self.issuesURL)
                }
            }
        }

        Settings {
            ClientSettingsView(model: model)
        }
    }

    private static let projectURL = URL(string: "https://github.com/Veritas-Calculus/vc-workspace")!
    private static let issuesURL = URL(string: "https://github.com/Veritas-Calculus/vc-workspace/issues")!
}

/// Animate presentation only. The remote NSView stays outside the animated
/// branches: connection identity and first-frame timing belong to the session.
struct WorkspaceRootView: View {
    @ObservedObject var model: AppModel
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var desktopSearch = ""

    var body: some View {
        Group {
            switch model.state {
            case .loading, .signedOut:
                ZStack {
                    if model.state == .loading {
                        WorkspaceLoadingView(stage: model.loadingStage)
                            .transition(.opacity)
                    } else {
                        LoginView(model: model)
                            .transition(WorkspaceStyle.transition(reduced: reduceMotion))
                    }
                }
                .animation(WorkspaceStyle.motion(reduced: reduceMotion), value: model.state)
            case .signedIn:
                if let session = model.activeSession {
                    DesktopSessionView(model: model, session: session)
                } else {
                    ZStack {
                        if let journey = model.connectionJourney {
                            DesktopConnectionJourneyView(model: model, journey: journey)
                                .transition(WorkspaceStyle.transition(reduced: reduceMotion))
                        } else {
                            DesktopListView(model: model, searchText: $desktopSearch)
                                .transition(.opacity)
                        }
                    }
                    .animation(WorkspaceStyle.motion(reduced: reduceMotion), value: model.connectionJourney != nil)
                }
            }
        }
        .background(WorkspaceStyle.canvas)
        .onChange(of: model.state) { _, state in
            if state == .signedOut { desktopSearch = "" }
        }
    }
}

struct ClientSettingsView: View {
    @ObservedObject var model: AppModel
    private var isChecking: Bool { model.serverCheck == .checking }

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            Text("服务器")
                .font(.title3.weight(.semibold))

            VStack(alignment: .leading, spacing: 6) {
                Text("控制面地址")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                TextField("https://workspace.example.com", text: $model.server)
                    .accessibilityLabel("控制面地址")
                    .textFieldStyle(.roundedBorder)
                    .textContentType(.URL)
                    .disabled(model.state == .signedIn)
                    .onSubmit { checkConnection() }
            }

            if model.state == .signedIn {
                Text("退出登录后可以更改服务器。")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            ServerConnectionFeedback(state: model.serverCheck)

            HStack {
                Spacer()
                Button(isChecking ? "正在检查…" : "检查连接") { checkConnection() }
                    .disabled(isChecking || model.server.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .padding(24)
        .frame(width: 440)
    }

    private func checkConnection() {
        Task { await model.checkServerConnection() }
    }
}


private struct DesktopSessionView: View {
    @ObservedObject var model: AppModel
    @ObservedObject var session: EmbeddedRDPSession
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var isFullScreen = false
    @State private var hasRevealedSession = false

    var body: some View {
        Group {
            if let journey = model.connectionJourney, journey.stage == .disconnecting || journey.stage == .cancelling {
                DesktopConnectionJourneyView(model: model, journey: journey)
            } else if session.state == .connecting || !hasRevealedSession {
                DesktopConnectionJourneyView(
                    model: model,
                    journey: model.connectionJourney ?? fallbackJourney(stage: .opening)
                )
            } else {
                VStack(spacing: 0) {
                    HStack(spacing: 10) {
                        Button {
                            model.disconnectSession(for: session.vmid)
                        } label: {
                            Label("断开连接", systemImage: "rectangle.portrait.and.arrow.right")
                        }
                        .buttonStyle(.borderless)

                        Divider().frame(height: 16)
                        Text(session.desktopName)
                            .font(.callout.weight(.medium))
                            .lineLimit(1)
                            .truncationMode(.middle)
                        Spacer()
                        if session.displayState == .scaled {
                            Button("重新调整画面") { session.retryDisplayAdjustment() }
                                .buttonStyle(.borderless)
                                .help("远端未及时更新分辨率，当前使用缩放画面。点击重试。")
                        }
                        Button {
                            let window = session.view.window ?? NSApplication.shared.keyWindow
                            window?.toggleFullScreen(nil)
                        } label: {
                            Image(systemName: isFullScreen ? "arrow.down.right.and.arrow.up.left" : "arrow.up.left.and.arrow.down.right")
                        }
                        .buttonStyle(.borderless)
                        .help(isFullScreen ? "退出全屏" : "全屏")
                        .accessibilityLabel(isFullScreen ? "退出全屏" : "全屏")
                    }
                    .padding(.horizontal, 12)
                    .frame(height: 38)
                    .background(.bar)

                    ZStack {
                        Color.black
                        EmbeddedRDPView(session: session)

                        if session.displayState == .adjusting {
                            SessionDisplayTransitionView()
                        }
                    }
                    .clipped()
                }
            }
        }
        .onAppear {
            isFullScreen = session.view.window?.styleMask.contains(.fullScreen) == true
        }
        .background {
            MainWindowReader { window in
                // Reconnecting replaces this view without a new fullscreen
                // notification; the RDP NSView is not attached at onAppear.
                Task { @MainActor in
                    isFullScreen = window.styleMask.contains(.fullScreen)
                }
            }
            .frame(width: 0, height: 0)
        }
        .task(id: session.state) {
            guard session.state == .connected else {
                hasRevealedSession = false
                return
            }
            try? await Task.sleep(for: .milliseconds(350))
            guard !Task.isCancelled, session.state == .connected else { return }
            withAnimation(reduceMotion ? nil : .easeOut(duration: 0.18)) {
                hasRevealedSession = true
            }
            model.sessionDidConnect(sessionID: session.id)
        }
        .onReceive(NotificationCenter.default.publisher(for: NSWindow.didEnterFullScreenNotification)) { notification in
            guard notification.object as? NSWindow === session.view.window else { return }
            isFullScreen = true
        }
        .onReceive(NotificationCenter.default.publisher(for: NSWindow.didExitFullScreenNotification)) { notification in
            guard notification.object as? NSWindow === session.view.window else { return }
            isFullScreen = false
        }
    }

    private func fallbackJourney(stage: DesktopConnectionStage) -> DesktopConnectionJourney {
        let desktop = model.desktops.first(where: { $0.vmid == session.vmid }) ?? Desktop(
            vmid: session.vmid,
            name: session.desktopName,
            node: "",
            status: "running",
            cpuCount: 0,
            memoryTotal: 0
        )
        return DesktopConnectionJourney(desktop: desktop, stage: stage)
    }
}
