import AppKit
import AuthenticationServices
import Combine
import Foundation
import OSLog

@MainActor
final class AppModel: NSObject, ObservableObject, ASWebAuthenticationPresentationContextProviding {
    enum State: Equatable { case signedOut, loading, signedIn }

    @Published var server = UserDefaults.standard.string(forKey: "server") ?? "http://127.0.0.1:8080" {
        didSet {
            guard server != oldValue else { return }
            invalidateSystemProbe()
            system = nil
            errorMessage = ""
        }
    }
    enum ServerCheck: Equatable {
        case idle, checking, succeeded, failed(String)
    }
    @Published private(set) var serverCheck: ServerCheck = .idle
    private var systemProbe: Task<SystemState, Error>?
    private var systemProbeID: UUID?
    @Published var username = ""
    @Published var password = ""
    @Published var system: SystemState?
    @Published var desktops: [Desktop] = []
    @Published var state: State = .loading
    @Published var errorMessage = ""
    @Published var isRefreshing = false
    @Published var connectingVMID: Int?
    @Published private(set) var loadingStage: ClientLoadingStage = .starting
    @Published private(set) var connectionJourney: DesktopConnectionJourney?
    @Published private(set) var activeSession: EmbeddedRDPSession?
    @Published private(set) var pendingConnectionVMID: Int?

    private var token: String?
    private var webSession: ASWebAuthenticationSession?
    private var connectionTask: Task<Void, Never>?
    private var connectionAttemptID: UUID?
    private let dependencies: AppModelDependencies
    private var sessionStopFallbackTask: Task<Void, Never>?
    private var endedSessionCheckTask: Task<Void, Never>?
    private var endedSessionCheckID: UUID?
    private var activeConnectionID: String?
    private var cancelledSessionID: UUID?
    private let connectionQuality = ConnectionQuality.managedClientDefault
    private var recoveryPolicy = SessionRecoveryPolicy()
    private var isRecovering = false
    private static let recoveryLogger = Logger(subsystem: "ac.plz.vc-workspace", category: "rdp-recovery")

    override convenience init() {
        self.init(dependencies: AppModelDependencies())
    }

    init(dependencies: AppModelDependencies) {
        self.dependencies = dependencies
        super.init()
    }

    func start() async {
        loadingStage = .starting
        let initialServer = server
        let loadToken = dependencies.loadToken
        token = await Task.detached {
            loadToken(initialServer)
        }.value
        if token != nil {
            loadingStage = .restoringSession
        }
        await loadSystem()
        guard token != nil else { state = .signedOut; return }
        loadingStage = .loadingDesktops
        if await loadDesktops() { await resumePendingAppLink(refreshDesktops: false) }
    }

    func checkServerConnection() async {
        guard serverCheck != .checking,
              !server.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        if state == .signedOut { errorMessage = "" }
        await loadSystem(showFeedback: true)
    }

    private func invalidateSystemProbe() {
        systemProbeID = nil
        systemProbe?.cancel()
        systemProbe = nil
        serverCheck = .idle
    }

    func loadSystem(showFeedback: Bool = false) async {
        invalidateSystemProbe()
        let endpoint = server
        let id = UUID()
        systemProbeID = id
        if showFeedback { serverCheck = .checking }
        let makeClient = dependencies.makeClient
        let task = Task { try await makeClient(endpoint).system() }
        systemProbe = task
        defer {
            if systemProbeID == id {
                systemProbe = nil
                systemProbeID = nil
            }
        }
        do {
            let result = try await withTaskCancellationHandler {
                try await task.value
            } onCancel: { task.cancel() }
            guard systemProbeID == id, server == endpoint else { return }
            guard !Task.isCancelled else { serverCheck = .idle; return }
            system = result
            dependencies.saveServer(endpoint)
            if showFeedback { serverCheck = .succeeded }
            else { errorMessage = "" }
        } catch {
            guard systemProbeID == id, server == endpoint else { return }
            guard !Task.isCancelled else { serverCheck = .idle; return }
            system = nil
            if showFeedback {
                // A read-only connectivity check must never terminate a login or desktop session.
                serverCheck = .failed(serverConnectionErrorMessage(error))
            } else {
                state = .signedOut
                errorMessage = error.localizedDescription
            }
        }
    }

    func login() async {
        guard state == .signedOut,
              !username.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              !password.isEmpty else { return }
        loadingStage = .signingIn
        state = .loading
        errorMessage = ""
        do {
            let session = try await client().localLogin(username: username, password: password)
            try accept(session)
            password = ""
            loadingStage = .loadingDesktops
            if await loadDesktops() { await resumePendingAppLink(refreshDesktops: false) }
        } catch {
            state = .signedOut
            errorMessage = error.localizedDescription
        }
    }

    func loginWithOIDC() {
        do {
            let api = try client()
            let session = ASWebAuthenticationSession(url: api.oidcStartURL(), callbackURLScheme: "vc-workspace") { [weak self] callback, error in
                guard let self else { return }
                Task { @MainActor in
                    if let error { self.errorMessage = error.localizedDescription; return }
                    guard let code = callback.flatMap({ URLComponents(url: $0, resolvingAgainstBaseURL: false) })?.queryItems?.first(where: { $0.name == "code" })?.value else {
                        self.errorMessage = "OIDC 回调缺少授权码"
                        return
                    }
                        self.loadingStage = .signingIn
                        self.state = .loading
                        do {
                            let nativeSession = try await api.exchange(code: code)
                            try self.accept(nativeSession)
                            self.loadingStage = .loadingDesktops
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
            dependencies.deleteToken()
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
        guard ["vc-workspace", "vc-vdi"].contains(url.scheme?.lowercased() ?? ""),
              url.host?.lowercased() == "connect" else { return }
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
        guard connectionAttemptID == nil, activeSession == nil,
              state == .signedIn, token != nil else { return }
        recoveryPolicy = SessionRecoveryPolicy()
        isRecovering = false
        connectionJourney = DesktopConnectionJourney(
            desktop: desktop,
            stage: desktop.status == "running" ? .preparing : .starting
        )
        launchConnection(to: desktop)
    }

    func cancelConnection() {
        guard connectionJourney?.stage.canCancel == true ||
              (connectionJourney == nil && activeSession?.state == .connecting) else { return }
        if isRecovering { Self.recoveryLogger.notice("recovery cancelled") }
        isRecovering = false
        if var journey = connectionJourney {
            journey.stage = .cancelling
            journey.message = nil
            connectionJourney = journey
        }
        invalidateConnectionAttempt()
        cancelledSessionID = activeSession?.id
        if activeSession == nil { connectionJourney = nil }
        activeSession?.disconnect()
        if let activeSession {
            scheduleSessionStopFallback(sessionID: activeSession.id, returnToLibrary: true)
        }
    }

    func focusSession(for vmid: Int) {
        guard activeSession?.vmid == vmid else { return }
        NSApplication.shared.keyWindow?.makeKeyAndOrderFront(nil)
    }

    func disconnectSession(for vmid: Int) {
        guard activeSession?.vmid == vmid else { return }
        guard let desktop = desktops.first(where: { $0.vmid == vmid }) else { return }
        connectionJourney = DesktopConnectionJourney(desktop: desktop, stage: .disconnecting)
        activeSession?.disconnect()
        if let activeSession { scheduleSessionStopFallback(sessionID: activeSession.id, returnToLibrary: false) }
    }

    func sessionDidConnect(sessionID: UUID) {
        guard let session = activeSession, session.id == sessionID, session.state == .connected,
              connectionJourney?.desktop.vmid == session.vmid,
              let stage = connectionJourney?.stage,
              [.preparing, .opening, .reconnecting].contains(stage) else { return }
        if isRecovering { Self.recoveryLogger.notice("recovery completed") }
        isRecovering = false
        connectionJourney = nil
    }

    func retryConnection() {
        guard let journey = connectionJourney, journey.stage.canRetry else { return }
        connectionJourney = nil
        beginConnection(to: journey.desktop)
    }

    func returnToDesktopLibrary() {
        guard connectionJourney?.stage.isPending == false else { return }
        invalidateEndedSessionCheck()
        let shouldRefresh = connectionJourney?.stage == .unavailable
        connectionJourney = nil
        errorMessage = ""
        if shouldRefresh { Task { await loadDesktops() } }
    }

    func prepareForTermination() {
        prepareForWindowClose()
    }

    func prepareForWindowClose() {
        invalidateEndedSessionCheck()
        sessionStopFallbackTask?.cancel()
        sessionStopFallbackTask = nil
        invalidateConnectionAttempt()
        // Detach first: stop() is allowed to synchronously report a failure.
        // Closing a window must never schedule an automatic recovery.
        let session = activeSession
        activeSession = nil
        cancelledSessionID = nil
        session?.disconnect()
        session?.destroy()
        releaseActiveConnection()
        connectionJourney = nil
    }

    private func launchConnection(to desktop: Desktop, delay: Duration? = nil) {
        invalidateEndedSessionCheck()
        let attemptID = UUID()
        connectionAttemptID = attemptID
        connectingVMID = desktop.vmid
        connectionTask = Task { @MainActor [weak self] in
            defer { self?.finishConnectionAttempt(attemptID) }
            do {
                if let delay { try await Task.sleep(for: delay) }
                try Task.checkCancellation()
                guard let self, self.connectionAttemptID == attemptID,
                      self.state == .signedIn else { return }
                await self.connect(to: desktop, attemptID: attemptID)
            } catch { /* An intentional cancellation has already reset the UI. */ }
        }
    }

    private func finishConnectionAttempt(_ id: UUID) {
        guard connectionAttemptID == id else { return }
        connectionAttemptID = nil
        connectingVMID = nil
        connectionTask = nil
    }

    private func invalidateConnectionAttempt() {
        connectionAttemptID = nil
        connectionTask?.cancel()
        connectionTask = nil
        connectingVMID = nil
        isRecovering = false
    }

    private func connect(to desktop: Desktop, attemptID: UUID) async {
        guard let token, connectionAttemptID == attemptID else { return }
        var pendingConnectionID: String?
        errorMessage = ""
        // Keep credentials and endpoint paired, including cancelled cleanup.
        let api: APIClient
        do { api = try client() }
        catch {
            connectionJourney = DesktopConnectionJourney(desktop: desktop, stage: .failed,
                                                          message: userFacingDesktopConnectionError(error))
            return
        }

        var current = desktop
        do {
            if current.status != "running" {
                updateConnectionStage(.starting, for: current.vmid)
                do {
                    _ = try await api.changePower(
                        vmid: current.vmid,
                        action: "start",
                        token: token,
                        idempotencyKey: "mac-start-\(current.vmid)-\(UUID().uuidString)"
                    )
                } catch APIClientError.rejected(let code, _, _) where code == "power_state_conflict" {
                    // A concurrent request already started the desktop. Continue to readiness polling.
                }
                current = try await waitUntilRunning(vmid: current.vmid, token: token, api: api)
            }

            try Task.checkCancellation()
            guard connectionAttemptID == attemptID else { return }
            // Recovery and manual retry must retain the state observed after
            // startup, not the stopped card that initiated this journey.
            if let journey = connectionJourney, journey.desktop.vmid == current.vmid {
                connectionJourney = DesktopConnectionJourney(
                    desktop: current, stage: journey.stage, message: journey.message
                )
            }
            updateConnectionStage(.preparing, for: current.vmid)
            let backingScale = NSApplication.shared.keyWindow?.backingScaleFactor ?? NSScreen.main?.backingScaleFactor ?? 1
            let desktopScaleFactor = backingScale >= 1.5 ? 200 : 100
            let connection = try await waitForConnection(
                vmid: current.vmid,
                token: token,
                desktopScaleFactor: desktopScaleFactor,
                api: api
            )
            pendingConnectionID = connection.id
            if Task.isCancelled || connectionAttemptID != attemptID {
                throw CancellationError()
            }
            updateConnectionStage(.opening, for: current.vmid)
            let session = try await dependencies.makeSession(connection, current.vmid, current.name, connectionQuality) { [weak self] sessionID, result in
                self?.remoteSessionEnded(sessionID: sessionID, result: result)
            }
            if Task.isCancelled || connectionAttemptID != attemptID {
                session.destroy()
                throw CancellationError()
            }
            activeSession = session
            cancelledSessionID = nil
            activeConnectionID = connection.id
            pendingConnectionID = nil
            session.start()
        } catch APIClientError.rejected(_, _, let status) where status == 401 {
            guard connectionAttemptID == attemptID, !Task.isCancelled else { return }
            if isRecovering { Self.recoveryLogger.notice("recovery ended; authentication required") }
            isRecovering = false
            self.token = nil
            dependencies.deleteToken()
            connectionJourney = nil
            state = .signedOut
            errorMessage = "登录已过期，请重新登录"
        } catch {
            if let pendingConnectionID {
                if let nativeError = error as? EmbeddedRDPError {
                    Self.recoveryLogger.error("connection preparation failed code=\(nativeError.diagnosticCode, privacy: .public)")
                }
                // This cleanup must not inherit the canceled connection task.
                await Task { try? await api.releaseConnection(id: pendingConnectionID, token: token) }.value
            }
            guard connectionAttemptID == attemptID, !Task.isCancelled else { return }
            if isRecovering { Self.recoveryLogger.notice("recovery ended; preparation failed") }
            isRecovering = false
            if error is CancellationError { connectionJourney = nil; return }
            connectionJourney = DesktopConnectionJourney(
                desktop: current,
                stage: desktopConnectionFailureStage(error),
                message: userFacingDesktopConnectionError(error)
            )
        }
    }

    func logout() async {
        let api = try? client()
        let previousToken = token
        prepareForWindowClose()
        self.token = nil
        dependencies.deleteToken()
        desktops = []
        connectingVMID = nil
        connectionJourney = nil
        pendingConnectionVMID = nil
        state = .loading
        if let previousToken { try? await api?.logout(token: previousToken) }
        state = .signedOut
    }

    func presentationAnchor(for _: ASWebAuthenticationSession) -> ASPresentationAnchor {
        NSApplication.shared.keyWindow ?? NSApplication.shared.windows.first ?? ASPresentationAnchor()
    }

    private func client() throws -> APIClient { try dependencies.makeClient(server) }

    private func accept(_ session: NativeSession) throws {
        try dependencies.saveToken(session.accessToken, server)
        token = session.accessToken
        dependencies.saveServer(server)
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

    private func remoteSessionEnded(sessionID: UUID, result: EmbeddedRDPSessionEnd) {
        guard let activeSession, activeSession.id == sessionID else { return }
        let vmid = activeSession.vmid
        sessionStopFallbackTask?.cancel()
        sessionStopFallbackTask = nil
        let desktop = connectionJourney?.desktop ?? desktops.first(where: { $0.vmid == vmid })
        let previousStage = connectionJourney?.stage
        let wasCancelled = cancelledSessionID == sessionID
        cancelledSessionID = nil
        self.activeSession = nil
        activeSession.destroy()
        releaseActiveConnection()
        guard let desktop else {
            connectionJourney = nil
            return
        }
        if wasCancelled || previousStage == .cancelling {
            connectionJourney = nil
            return
        }
        if previousStage == .disconnecting {
            connectionJourney = DesktopConnectionJourney(desktop: desktop, stage: .disconnected)
            return
        }
        if state == .signedIn, token != nil,
           let delay = recoveryPolicy.nextDelay(for: result, now: ProcessInfo.processInfo.systemUptime) {
            isRecovering = true
            connectionJourney = DesktopConnectionJourney(desktop: desktop, stage: .reconnecting)
            Self.recoveryLogger.notice("recovery scheduled; access will be revalidated")
            launchConnection(to: desktop, delay: delay)
            return
        }
        isRecovering = false
        switch result {
        case .closed:
            connectionJourney = DesktopConnectionJourney(desktop: desktop, stage: .disconnected)
        case .failed(let code):
            connectionJourney = DesktopConnectionJourney(
                desktop: desktop,
                stage: .failed,
                message: userFacingNativeSessionError(code)
            )
        }
        revalidateEndedSessionAccess(to: desktop)
    }

    private func invalidateEndedSessionCheck() {
        endedSessionCheckID = nil
        endedSessionCheckTask?.cancel()
        endedSessionCheckTask = nil
    }

    private func revalidateEndedSessionAccess(to desktop: Desktop) {
        guard state == .signedIn, let token, let api = try? client() else { return }
        invalidateEndedSessionCheck()
        let checkID = UUID()
        let checkedServer = server
        endedSessionCheckID = checkID
        // A transport error cannot distinguish lost access from a network fault.
        // This check is read-only: never mint credentials just to classify it.
        endedSessionCheckTask = Task { @MainActor [weak self] in
            defer {
                if self?.endedSessionCheckID == checkID {
                    self?.endedSessionCheckID = nil
                    self?.endedSessionCheckTask = nil
                }
            }
            do {
                let latest = try await api.desktops(token: token)
                try Task.checkCancellation()
                guard let self, self.ownsEndedSessionCheck(checkID, token: token, server: checkedServer, vmid: desktop.vmid) else { return }
                self.desktops = latest
                if !latest.contains(where: { $0.vmid == desktop.vmid }) {
                    self.connectionJourney = DesktopConnectionJourney(desktop: desktop, stage: .unavailable)
                }
            } catch APIClientError.rejected(_, _, let status) where status == 401 {
                guard !Task.isCancelled, let self,
                      self.ownsEndedSessionCheck(checkID, token: token, server: checkedServer, vmid: desktop.vmid) else { return }
                self.token = nil
                self.dependencies.deleteToken()
                self.desktops = []
                self.connectionJourney = nil
                self.state = .signedOut
                self.errorMessage = "登录已过期，请重新登录"
            } catch {
                // An unavailable control plane proves neither revocation nor a
                // network cause. Keep the neutral, recoverable transport state.
            }
        }
    }

    private func ownsEndedSessionCheck(_ id: UUID, token: String, server: String, vmid: Int) -> Bool {
        endedSessionCheckID == id && self.token == token && self.server == server &&
            state == .signedIn && activeSession == nil && connectionAttemptID == nil &&
            connectionJourney?.desktop.vmid == vmid
    }

    private func updateConnectionStage(_ stage: DesktopConnectionStage, for vmid: Int) {
        guard var journey = connectionJourney, journey.desktop.vmid == vmid else { return }
        journey.stage = isRecovering && (stage == .preparing || stage == .opening) ? .reconnecting : stage
        journey.message = nil
        connectionJourney = journey
    }

    private func scheduleSessionStopFallback(sessionID: UUID, returnToLibrary: Bool) {
        sessionStopFallbackTask?.cancel()
        sessionStopFallbackTask = Task { @MainActor [weak self] in
            do {
                try await Task.sleep(for: .seconds(8))
            } catch {
                return
            }
            guard let self, let session = self.activeSession, session.id == sessionID else { return }
            let vmid = session.vmid
            let desktop = self.connectionJourney?.desktop ?? self.desktops.first(where: { $0.vmid == vmid })
            self.activeSession = nil
            session.destroy()
            self.releaseActiveConnection()
            self.sessionStopFallbackTask = nil
            if returnToLibrary {
                self.connectionJourney = nil
            } else if let desktop {
                self.connectionJourney = DesktopConnectionJourney(desktop: desktop, stage: .disconnected)
            } else {
                self.connectionJourney = nil
            }
        }
    }

    private func releaseActiveConnection() {
        guard let connectionID = activeConnectionID else { return }
        activeConnectionID = nil
        guard let token else { return }
        guard let api = try? client() else { return }
        Task {
            try? await api.releaseConnection(id: connectionID, token: token)
        }
    }

    private func waitUntilRunning(vmid: Int, token: String, api: APIClient) async throws -> Desktop {
        for _ in 0..<60 {
            try Task.checkCancellation()
            let latest = try await api.desktops(token: token)
            try Task.checkCancellation()
            desktops = latest
            if let desktop = latest.first(where: { $0.vmid == vmid }), desktop.status == "running" {
                return desktop
            }
            try await Task.sleep(for: .seconds(2))
        }
        throw DesktopConnectionError.startTimedOut
    }

    private func waitForConnection(vmid: Int, token: String, desktopScaleFactor: Int, api: APIClient) async throws -> DesktopConnection {
        for _ in 0..<120 {
            try Task.checkCancellation()
            do {
                return try await api.createConnection(
                    vmid: vmid,
                    token: token,
                    desktopScaleFactor: desktopScaleFactor
                )
            } catch APIClientError.rejected(let code, _, _) where isRetryableDesktopConnectionPreparationCode(code) {
                try await Task.sleep(for: .seconds(3))
            }
        }
        throw DesktopConnectionError.servicesTimedOut
    }
}

func isRetryableDesktopConnectionPreparationCode(_ code: String) -> Bool {
    ["desktop_not_running", "desktop_not_ready", "desktop_network_unavailable"].contains(code)
}
