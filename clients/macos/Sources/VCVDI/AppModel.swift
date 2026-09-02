import AppKit
import AuthenticationServices
import Combine
import Foundation

@MainActor
final class AppModel: NSObject, ObservableObject, ASWebAuthenticationPresentationContextProviding {
    enum State { case signedOut, loading, signedIn }

    @Published var server = UserDefaults.standard.string(forKey: "server") ?? "http://127.0.0.1:8080"
    @Published var username = ""
    @Published var password = ""
    @Published var system: SystemState?
    @Published var desktops: [Desktop] = []
    @Published var state: State = .loading
    @Published var errorMessage = ""
    @Published var isRefreshing = false
    @Published var connectingVMID: Int?
    @Published var connectionStatus = ""
    @Published private(set) var activeSession: EmbeddedRDPSession?
    @Published var sessionMessage = ""
    @Published private(set) var pendingConnectionVMID: Int?

    private var token: String?
    private var webSession: ASWebAuthenticationSession?
    private var connectionTask: Task<Void, Never>?
    private let connectionQuality = ConnectionQuality.managedClientDefault

    override init() {
        super.init()
    }

    func start() async {
        let initialServer = server
        token = await Task.detached {
            SessionKeychain.load(for: initialServer)
        }.value
        await loadSystem()
        guard token != nil else { state = .signedOut; return }
        if await loadDesktops() { await resumePendingAppLink(refreshDesktops: false) }
    }

    func loadSystem() async {
        do {
            system = try await client().system()
            UserDefaults.standard.set(server, forKey: "server")
            errorMessage = ""
        } catch {
            system = nil
            state = .signedOut
            errorMessage = error.localizedDescription
        }
    }

    func login() async {
        state = .loading
        do {
            let session = try await client().localLogin(username: username, password: password)
            try accept(session)
            password = ""
            if await loadDesktops() { await resumePendingAppLink(refreshDesktops: false) }
        } catch {
            state = .signedOut
            errorMessage = error.localizedDescription
        }
    }

    func loginWithOIDC() {
        do {
            let api = try client()
            let session = ASWebAuthenticationSession(url: api.oidcStartURL(), callbackURLScheme: "vc-vdi") { [weak self] callback, error in
                guard let self else { return }
                Task { @MainActor in
                    if let error { self.errorMessage = error.localizedDescription; return }
                    guard let code = callback.flatMap({ URLComponents(url: $0, resolvingAgainstBaseURL: false) })?.queryItems?.first(where: { $0.name == "code" })?.value else {
                        self.errorMessage = "OIDC 回调缺少授权码"
                        return
                    }
                        self.state = .loading
                        do {
                            let nativeSession = try await api.exchange(code: code)
                            try self.accept(nativeSession)
                            if await self.loadDesktops() { await self.resumePendingAppLink(refreshDesktops: false) }
                    } catch {
                        self.state = .signedOut
                        self.errorMessage = error.localizedDescription
                    }
                }
            }
            session.presentationContextProvider = self
            session.prefersEphemeralWebBrowserSession = false
            webSession = session
            session.start()
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    @discardableResult
    func loadDesktops() async -> Bool {
        guard let token else { state = .signedOut; return false }
        isRefreshing = true
        defer { isRefreshing = false }
        do {
            desktops = try await client().desktops(token: token)
            state = .signedIn
            errorMessage = ""
            return true
        } catch APIClientError.rejected(_, _, let status) where status == 401 {
            self.token = nil
            SessionKeychain.delete()
            state = .signedOut
            errorMessage = "登录已过期，请重新登录"
            return false
        } catch {
            state = .signedIn
            errorMessage = error.localizedDescription
            return false
        }
    }

    func handleIncomingURL(_ url: URL) {
        guard url.scheme?.lowercased() == "vc-vdi", url.host?.lowercased() == "connect" else { return }
        guard case .connect(let vmid) = VCWorkspaceAppLink.parse(url) else {
            pendingConnectionVMID = nil
            errorMessage = "无法打开此 VC Workspace 链接"
            return
        }
        pendingConnectionVMID = vmid
        errorMessage = ""
        Task { [weak self] in
            await self?.resumePendingAppLink(refreshDesktops: true)
        }
    }

    func beginConnection(to desktop: Desktop) {
        guard connectionTask == nil else { return }
        connectionTask = Task { [weak self] in
            await self?.connect(to: desktop)
        }
    }

    func cancelConnection() {
        connectionTask?.cancel()
    }

    func focusSession(for vmid: Int) {
        guard activeSession?.vmid == vmid else { return }
        NSApplication.shared.keyWindow?.makeKeyAndOrderFront(nil)
    }

    func disconnectSession(for vmid: Int) {
        guard activeSession?.vmid == vmid else { return }
        sessionMessage = "正在断开远程桌面"
        activeSession?.disconnect()
    }

    func prepareForTermination() {
        connectionTask?.cancel()
        activeSession?.disconnect()
        activeSession?.destroy()
    }

    func prepareForWindowClose() {
        connectionTask?.cancel()
        activeSession?.disconnect()
        activeSession?.destroy()
        activeSession = nil
        sessionMessage = ""
    }

    private func connect(to desktop: Desktop) async {
        guard let token, connectingVMID == nil else { return }
        connectingVMID = desktop.vmid
        errorMessage = ""
        sessionMessage = ""
        defer {
            connectingVMID = nil
            connectionStatus = ""
            connectionTask = nil
        }

        do {
            var current = desktop
            if current.status != "running" {
                connectionStatus = "正在启动桌面"
                do {
                    _ = try await client().changePower(
                        vmid: current.vmid,
                        action: "start",
                        token: token,
                        idempotencyKey: "mac-start-\(current.vmid)-\(UUID().uuidString)"
                    )
                } catch APIClientError.rejected(let code, _, _) where code == "power_state_conflict" {
                    // A concurrent request already started the desktop. Continue to readiness polling.
                }
                current = try await waitUntilRunning(vmid: current.vmid, token: token)
            }

            connectionStatus = "正在准备桌面"
            let connection = try await waitForConnection(vmid: current.vmid, token: token)
            connectionStatus = "正在打开远程桌面"
            let session = try EmbeddedRDPSession(
                connection: connection,
                vmid: current.vmid,
                desktopName: current.name,
                quality: connectionQuality
            ) { [weak self] result in
                self?.remoteSessionEnded(vmid: current.vmid, result: result)
            }
            activeSession = session
            sessionMessage = ""
        } catch is CancellationError {
            sessionMessage = "已取消连接"
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    func logout() async {
        connectionTask?.cancel()
        connectionTask = nil
        activeSession?.disconnect()
        activeSession?.destroy()
        activeSession = nil
        if let token { try? await client().logout(token: token) }
        self.token = nil
        SessionKeychain.delete()
        desktops = []
        connectingVMID = nil
        connectionStatus = ""
        sessionMessage = ""
        pendingConnectionVMID = nil
        state = .signedOut
    }

    func presentationAnchor(for _: ASWebAuthenticationSession) -> ASPresentationAnchor {
        NSApplication.shared.keyWindow ?? NSApplication.shared.windows.first ?? ASPresentationAnchor()
    }

    private func client() throws -> APIClient { try APIClient(server: server) }

    private func accept(_ session: NativeSession) throws {
        try SessionKeychain.save(session.accessToken, for: server)
        token = session.accessToken
        UserDefaults.standard.set(server, forKey: "server")
        errorMessage = ""
    }

    private func resumePendingAppLink(refreshDesktops: Bool) async {
        guard let vmid = pendingConnectionVMID, token != nil, state == .signedIn else { return }
        if refreshDesktops, !(await loadDesktops()) {
            if state == .signedIn { pendingConnectionVMID = nil }
            return
        }
        guard pendingConnectionVMID == vmid else { return }
        if activeSession?.vmid == vmid {
            pendingConnectionVMID = nil
            focusSession(for: vmid)
            return
        }
        guard connectionTask == nil else {
            pendingConnectionVMID = nil
            if connectingVMID != vmid { errorMessage = "另一台桌面正在连接，请稍后重试" }
            return
        }
        guard let desktop = desktops.first(where: { $0.vmid == vmid }) else {
            pendingConnectionVMID = nil
            errorMessage = "VM \(vmid) 不存在或当前账号无权访问"
            return
        }
        pendingConnectionVMID = nil
        beginConnection(to: desktop)
    }

    private func remoteSessionEnded(vmid: Int, result: EmbeddedRDPSessionEnd) {
        guard activeSession?.vmid == vmid else { return }
        activeSession?.destroy()
        activeSession = nil
        switch result {
        case .closed:
            sessionMessage = "远程桌面会话已结束"
        case .failed(let message):
            errorMessage = message.isEmpty ? "远程桌面意外退出，请重试" : message
        }
    }

    private func waitUntilRunning(vmid: Int, token: String) async throws -> Desktop {
        for attempt in 0..<60 {
            try Task.checkCancellation()
            let latest = try await client().desktops(token: token)
            desktops = latest
            if let desktop = latest.first(where: { $0.vmid == vmid }), desktop.status == "running" {
                return desktop
            }
            connectionStatus = "正在启动桌面 · \((attempt + 1) * 2) 秒"
            try await Task.sleep(for: .seconds(2))
        }
        throw DesktopConnectionError.startTimedOut
    }

    private func waitForConnection(vmid: Int, token: String) async throws -> DesktopConnection {
        for attempt in 0..<120 {
            try Task.checkCancellation()
            do {
                return try await client().createConnection(vmid: vmid, token: token)
            } catch APIClientError.rejected(let code, _, _) where isRetryableDesktopConnectionPreparationCode(code) {
                connectionStatus = "正在准备桌面服务 · \((attempt + 1) * 3) 秒"
                try await Task.sleep(for: .seconds(3))
            }
        }
        throw DesktopConnectionError.servicesTimedOut
    }
}

func isRetryableDesktopConnectionPreparationCode(_ code: String) -> Bool {
    ["desktop_not_running", "desktop_not_ready", "desktop_network_unavailable"].contains(code)
}

private enum DesktopConnectionError: LocalizedError {
    case startTimedOut
    case servicesTimedOut

    var errorDescription: String? {
        switch self {
        case .startTimedOut: return "桌面启动超时，请稍后重试"
        case .servicesTimedOut: return "桌面服务准备超时，请检查镜像中的 Guest Agent 和 xrdp"
        }
    }
}
