import AppKit
import Darwin
import Foundation
import OSLog
import SwiftUI

struct RDPViewport: Equatable {
    let pixelWidth: UInt32
    let pixelHeight: UInt32
    let desktopScaleFactor: UInt32

    init?(pointSize: CGSize, backingScaleFactor: CGFloat) {
        guard pointSize.width.isFinite, pointSize.height.isFinite,
              pointSize.width > 0, pointSize.height > 0,
              backingScaleFactor.isFinite, backingScaleFactor > 0 else {
            return nil
        }

        // Render one remote pixel per physical display pixel, while sending
        // the matching RDP desktop scale so controls retain their AppKit point
        // size. This is native Retina behavior: sharper text without a smaller
        // desktop UI. Cap at 2× because higher supersampling only adds traffic.
        let renderScale = min(max(backingScaleFactor, 1), 2)
        let rawWidth = Int((pointSize.width * renderScale).rounded())
        let rawHeight = Int((pointSize.height * renderScale).rounded())
        let clampedWidth = min(max(rawWidth, 200), 8_192)

        // RDP Display Control requires an even monitor width.
        pixelWidth = UInt32(clampedWidth - clampedWidth % 2)
        pixelHeight = UInt32(min(max(rawHeight, 200), 8_192))
        desktopScaleFactor = UInt32((renderScale * 100).rounded())
    }
}

enum EmbeddedRDPError: LocalizedError {
    case invalidConnection
    case runtimeUnavailable
    case incompatibleRuntime
    case symbolMissing(String)
    case sessionCreationFailed

    // Stable diagnostics only: never log symbol names, credentials or endpoints.
    var diagnosticCode: String {
        switch self {
        case .invalidConnection: "invalid_descriptor"
        case .runtimeUnavailable: "runtime_unavailable"
        case .incompatibleRuntime: "runtime_incompatible"
        case .symbolMissing: "runtime_symbol_missing"
        case .sessionCreationFailed: "session_creation_failed"
        }
    }

    var errorDescription: String? {
        switch self {
        case .invalidConnection:
            return "控制面返回了无效的 RDP 连接信息"
        case .runtimeUnavailable:
            return "VC Workspace 的远程桌面运行时不完整，请重新安装应用"
        case .incompatibleRuntime:
            return "远程桌面运行时版本不兼容，请更新 VC Workspace"
        case .symbolMissing(let name):
            return "远程桌面运行时缺少 \(name)，请重新安装应用"
        case .sessionCreationFailed:
            return "无法创建原生远程桌面会话"
        }
    }
}

enum EmbeddedRDPSessionEnd: Equatable {
    case closed
    case failed(String)
}

// The session owns one transport. Keeping the native boundary injectable lets
// lifecycle tests deliver an immediate failure without loading FreeRDP or a VM.
@MainActor
struct EmbeddedRDPTransport {
    let view: NSView
    let state: () -> Int32
    let displayState: () -> Int32
    let error: () -> String
    let setViewport: (UInt32, UInt32, UInt32) -> Void
    let stop: () -> Void
    let destroy: () -> Void

    static func validate(_ connection: DesktopConnection, now: Date = .now) throws {
        guard !connection.host.isEmpty, !connection.host.contains("\0"),
              (1...Int(UInt16.max)).contains(connection.port),
              !connection.username.isEmpty, !connection.username.contains("\0"),
              !connection.password.isEmpty, !connection.password.contains("\0"),
              connection.sessionPolicy.hasValidSnapshotHash else { throw EmbeddedRDPError.invalidConnection }
        switch (connection.protocolName, connection.gateway) {
        case ("rdp", nil): return
        case ("rdp-gateway", .some(let descriptor)):
            // Independent clocks can differ slightly even with NTP. The
            // issuance sanity check is not an expiry grant: neither the local
            // ticket deadline nor the server's one-use/lease checks is extended.
            guard connection.issuedAt <= now.addingTimeInterval(5),
                  connection.issuedAt < connection.expiresAt,
                  descriptor.expiresAt <= connection.expiresAt else {
                Logger(subsystem: "ac.plz.vc-workspace", category: "rdp-session").error("descriptor time rejected future_ms=\(connection.issuedAt.timeIntervalSince(now) * 1000, privacy: .public) ticket_after_session=\(descriptor.expiresAt > connection.expiresAt)")
                throw EmbeddedRDPError.invalidConnection
            }
            _ = try descriptor.validatedURL(now: now)
        default: throw EmbeddedRDPError.invalidConnection
        }
    }

    static func open(connection: DesktopConnection, quality: ConnectionQuality) async throws -> Self {
        // Reject malformed policy/credentials before redeeming a one-time ticket.
        try validate(connection)
        if connection.protocolName == "rdp-gateway", let descriptor = connection.gateway {
            let tunnel = try await GatewayTunnel.open(descriptor)
            defer { tunnel.releaseNativeDescriptor() }
            do {
                try Task.checkCancellation()
                return try native(connection: connection, quality: quality, tunnel: tunnel)
            } catch { tunnel.close(); throw error }
        }
        return try native(connection: connection, quality: quality)
    }

    static func native(connection: DesktopConnection, quality: ConnectionQuality, tunnel: GatewayTunnel? = nil) throws -> Self {
        // Ticket expiry only governs opening WSS, not the established stream.
        // open() validates it before redemption; native() can run just after it.
        let routed = connection.protocolName == "rdp-gateway" && connection.gateway != nil && tunnel != nil
        guard ((connection.protocolName == "rdp" && connection.gateway == nil && tunnel == nil) || routed), !connection.host.isEmpty, !connection.host.contains("\0"),
              (1...Int(UInt16.max)).contains(connection.port),
              !connection.username.isEmpty, !connection.username.contains("\0"),
              !connection.password.isEmpty, !connection.password.contains("\0"),
              connection.sessionPolicy.hasValidSnapshotHash else {
            throw EmbeddedRDPError.invalidConnection
        }
        let runtime = try EmbeddedRDPRuntime()
        let handle = connection.host.withCString { host in
            connection.username.withCString { username in
                connection.password.withCString { password in
                    (connection.gateway?.certificateSHA256 ?? "").withCString { certificate in
                    runtime.create(host, UInt16(connection.port), username, password, quality.runtimeValue,
                                   connection.sessionPolicy.clipboardRedirection ? 1 : 0,
                                   connection.sessionPolicy.driveRedirection ? 1 : 0,
                                   tunnel?.nativeDescriptor ?? -1, certificate)
                    }
                }
            }
        }
        guard let handle, let viewPointer = runtime.sessionView(handle) else {
            if let handle { runtime.destroy(handle) }
            throw EmbeddedRDPError.sessionCreationFailed
        }
        return Self(
            view: Unmanaged<NSView>.fromOpaque(viewPointer).takeUnretainedValue(),
            state: { runtime.sessionState(handle) },
            displayState: { runtime.displayState(handle) },
            error: { runtime.sessionError(handle).flatMap(String.init(validatingUTF8:)) ?? "" },
            setViewport: { _ = runtime.setViewport(handle, $0, $1, $2) },
            stop: { tunnel?.close(); runtime.stop(handle) },
            destroy: { tunnel?.close(); runtime.destroy(handle) }
        )
    }
}

@MainActor
final class EmbeddedRDPSession: ObservableObject, Identifiable {
    private static let logger = Logger(subsystem: "ac.plz.vc-workspace", category: "rdp-session")
    enum State: Int32 {
        case connecting = 0
        case connected = 1
        case closed = 2
        case failed = 3
    }

    enum DisplayState: Int32 {
        case ready = 0
        case adjusting = 1
        case scaled = 2
    }

    let id = UUID()
    let vmid: Int
    let desktopName: String
    let view: NSView
    @Published private(set) var state: State = .connecting
    @Published private(set) var displayState: DisplayState = .ready

    private var transport: EmbeddedRDPTransport?
    private var pollTimer: Timer?
    private var resizeTask: Task<Void, Never>?
    private var requestedViewport: RDPViewport?
    private var sentViewport: RDPViewport?
    private var finished = false
    private var started = false
    private let startedAt = ContinuousClock.now
    private let onTermination: @MainActor (UUID, EmbeddedRDPSessionEnd) -> Void

    convenience init(
        connection: DesktopConnection,
        vmid: Int,
        desktopName: String,
        quality: ConnectionQuality,
        onTermination: @escaping @MainActor (UUID, EmbeddedRDPSessionEnd) -> Void
    ) async throws {
        self.init(transport: try await .open(connection: connection, quality: quality),
                  vmid: vmid, desktopName: desktopName, onTermination: onTermination)
    }

    init(transport: EmbeddedRDPTransport, vmid: Int, desktopName: String,
         onTermination: @escaping @MainActor (UUID, EmbeddedRDPSessionEnd) -> Void) {
        self.transport = transport
        self.view = transport.view
        self.vmid = vmid
        self.desktopName = desktopName
        self.onTermination = onTermination
    }

    // The owner must install this session before starting observation: native
    // creation can already have failed, and poll() can terminate synchronously.
    func start() {
        guard !started, !finished, transport != nil else { return }
        started = true
        pollTimer = Timer.scheduledTimer(withTimeInterval: 0.25, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.poll() }
        }
        poll()
    }

    func disconnect() {
        guard let transport, !finished else { return }
        Self.logger.notice("stop requested attempt=\(self.id.uuidString, privacy: .public)")
        transport.stop()
        poll()
    }

    func retryDisplayAdjustment() {
        guard let requestedViewport, !finished else { return }
        send(requestedViewport, force: true)
    }

    func updateViewport(
        pointSize: CGSize,
        backingScaleFactor: CGFloat,
        immediately: Bool = false
    ) {
        guard let viewport = RDPViewport(
            pointSize: pointSize,
            backingScaleFactor: backingScaleFactor
        ) else { return }

        if immediately {
            resizeTask?.cancel()
            resizeTask = nil
            requestedViewport = viewport
            send(viewport, force: true)
            return
        }
        guard viewport != requestedViewport else { return }
        requestedViewport = viewport
        resizeTask?.cancel()
        resizeTask = Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: 180_000_000)
            guard !Task.isCancelled, let self else { return }
            self.resizeTask = nil
            self.send(viewport)
        }
    }

    func destroy() {
        finished = true
        resizeTask?.cancel()
        resizeTask = nil
        pollTimer?.invalidate()
        pollTimer = nil
        guard let transport else { return }
        self.transport = nil
        transport.destroy()
    }

    private func poll() {
        guard let transport, started, !finished else { return }
        guard let next = State(rawValue: transport.state()) else { return }
        if let display = DisplayState(rawValue: transport.displayState()), displayState != display {
            displayState = display
            view.needsDisplay = true
        }
        if state != next {
            Self.logger.notice("state attempt=\(self.id.uuidString, privacy: .public) from=\(self.state.rawValue) to=\(next.rawValue) elapsed=\(String(describing: self.startedAt.duration(to: .now)), privacy: .public)")
            state = next
        }
        switch next {
        case .connecting, .connected:
            return
        case .closed:
            finish(.closed)
        case .failed:
            let message = transport.error()
            finish(.failed(message.isEmpty ? "远程桌面连接失败" : message))
        }
    }

    private func finish(_ result: EmbeddedRDPSessionEnd) {
        guard !finished else { return }
        finished = true
        pollTimer?.invalidate()
        pollTimer = nil
        onTermination(id, result)
    }

    private func send(_ viewport: RDPViewport, force: Bool = false) {
        guard let transport, !finished, force || viewport != sentViewport else { return }
        transport.setViewport(
            viewport.pixelWidth,
            viewport.pixelHeight,
            viewport.desktopScaleFactor
        )
        sentViewport = viewport
        view.needsDisplay = true
        // Viewport reporting can run during AppKit/SwiftUI layout. Publish
        // presentation changes on the next turn, never from updateNSView.
        Task { @MainActor [weak self] in self?.poll() }
    }
}

private final class EmbeddedRDPRuntime {
    typealias Version = @convention(c) () -> Int32
    typealias Create = @convention(c) (
        UnsafePointer<CChar>, UInt16, UnsafePointer<CChar>, UnsafePointer<CChar>, Int32, Int32, Int32,
        Int32, UnsafePointer<CChar>
    ) -> UnsafeMutableRawPointer?
    typealias SessionView = @convention(c) (UnsafeMutableRawPointer) -> UnsafeMutableRawPointer?
    typealias SessionState = @convention(c) (UnsafeMutableRawPointer) -> Int32
    typealias SessionError = @convention(c) (UnsafeMutableRawPointer) -> UnsafePointer<CChar>?
    typealias SetViewport = @convention(c) (
        UnsafeMutableRawPointer, UInt32, UInt32, UInt32
    ) -> Int32
    typealias Stop = @convention(c) (UnsafeMutableRawPointer) -> Void
    typealias Destroy = @convention(c) (UnsafeMutableRawPointer) -> Void

    let create: Create
    let sessionView: SessionView
    let sessionState: SessionState
    let displayState: SessionState
    let sessionError: SessionError
    let setViewport: SetViewport
    let stop: Stop
    let destroy: Destroy
    private let library: UnsafeMutableRawPointer

    init(bundle: Bundle = .main, environment: [String: String] = ProcessInfo.processInfo.environment) throws {
        guard let path = Self.runtimePath(bundle: bundle, environment: environment) else {
            throw EmbeddedRDPError.runtimeUnavailable
        }
        let frameworksURL = URL(fileURLWithPath: path).deletingLastPathComponent()
        let modulesPath = frameworksURL.appendingPathComponent("ossl-modules").path
        if FileManager.default.fileExists(atPath: modulesPath) {
            setenv("OPENSSL_MODULES", modulesPath, 1)
        }
        guard let library = dlopen(path, RTLD_NOW | RTLD_LOCAL) else {
            throw EmbeddedRDPError.runtimeUnavailable
        }
        self.library = library

        let version: Version = try Self.symbol("vcw_rdp_runtime_version", in: library)
        guard version() == 5 else { throw EmbeddedRDPError.incompatibleRuntime }
        create = try Self.symbol("vcw_rdp_session_create", in: library)
        sessionView = try Self.symbol("vcw_rdp_session_view", in: library)
        sessionState = try Self.symbol("vcw_rdp_session_state", in: library)
        displayState = try Self.symbol("vcw_rdp_session_display_state", in: library)
        sessionError = try Self.symbol("vcw_rdp_session_error", in: library)
        setViewport = try Self.symbol("vcw_rdp_session_set_viewport", in: library)
        stop = try Self.symbol("vcw_rdp_session_stop", in: library)
        destroy = try Self.symbol("vcw_rdp_session_destroy", in: library)
    }

    static func runtimePath(
        bundle: Bundle = .main,
        environment: [String: String] = ProcessInfo.processInfo.environment,
        fileManager: FileManager = .default
    ) -> String? {
#if DEBUG
        let configured = environment["VC_WORKSPACE_RDP_RUNTIME_PATH"].map { [$0] } ?? []
#else
        let configured: [String] = []
#endif
        let bundled = bundle.privateFrameworksURL.map {
            [$0.appendingPathComponent("libVCWorkspaceRDP.dylib").path]
        } ?? []
        return (configured + bundled).first(where: fileManager.fileExists(atPath:))
    }

    private static func symbol<T>(_ name: String, in library: UnsafeMutableRawPointer) throws -> T {
        guard let pointer = dlsym(library, name) else { throw EmbeddedRDPError.symbolMissing(name) }
        return unsafeBitCast(pointer, to: T.self)
    }
}

private final class EmbeddedRDPContainerView: NSView {
    var onViewportChange: ((CGSize, CGFloat, Bool) -> Void)?
    private var windowObservers: [NSObjectProtocol] = []

    override func layout() {
        super.layout()
        reportViewport(immediately: false)
    }

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        removeWindowObservers()
        guard let window else { return }

        let center = NotificationCenter.default
        let immediateNotifications: [Notification.Name] = [
            NSWindow.didEndLiveResizeNotification,
            NSWindow.didEnterFullScreenNotification,
            NSWindow.didExitFullScreenNotification,
            NSWindow.didChangeBackingPropertiesNotification,
        ]
        windowObservers = immediateNotifications.map { name in
            center.addObserver(forName: name, object: window, queue: .main) { [weak self] _ in
                self?.reportViewport(immediately: true)
            }
        }
        DispatchQueue.main.async { [weak self] in
            self?.reportViewport(immediately: true)
        }
    }

    func invalidate() {
        removeWindowObservers()
        onViewportChange = nil
    }

    func refreshViewport(immediately: Bool) {
        reportViewport(immediately: immediately)
    }

    private func reportViewport(immediately: Bool) {
        guard bounds.width > 0, bounds.height > 0 else { return }
#if DEBUG
        if immediately, let window {
            // Geometry only; no desktop name, user, host, credentials or pixels.
            let logger = Logger(subsystem: "ac.plz.vc-workspace", category: "rdp-geometry")
            logger.notice("viewport bounds=\(NSStringFromRect(self.bounds), privacy: .public) visible=\(NSStringFromRect(self.visibleRect), privacy: .public) inWindow=\(NSStringFromRect(self.convert(self.bounds, to: nil)), privacy: .public) contentLayout=\(NSStringFromRect(window.contentLayoutRect), privacy: .public) fullscreen=\(window.styleMask.contains(.fullScreen))")
        }
#endif
        onViewportChange?(bounds.size, window?.backingScaleFactor ?? 1, immediately)
    }

    private func removeWindowObservers() {
        let center = NotificationCenter.default
        windowObservers.forEach(center.removeObserver)
        windowObservers.removeAll()
    }

    deinit {
        removeWindowObservers()
    }
}

struct EmbeddedRDPView: NSViewRepresentable {
    let session: EmbeddedRDPSession

    func makeNSView(context: Context) -> NSView {
        let container = EmbeddedRDPContainerView()
        container.wantsLayer = true
        container.layer?.backgroundColor = NSColor.black.cgColor
        let remoteView = session.view
        remoteView.translatesAutoresizingMaskIntoConstraints = false
        container.addSubview(remoteView)
        NSLayoutConstraint.activate([
            remoteView.leadingAnchor.constraint(equalTo: container.leadingAnchor),
            remoteView.trailingAnchor.constraint(equalTo: container.trailingAnchor),
            remoteView.topAnchor.constraint(equalTo: container.topAnchor),
            remoteView.bottomAnchor.constraint(equalTo: container.bottomAnchor),
        ])
        container.onViewportChange = { [weak session] size, backingScaleFactor, immediately in
            session?.updateViewport(
                pointSize: size,
                backingScaleFactor: backingScaleFactor,
                immediately: immediately
            )
        }
        DispatchQueue.main.async {
            container.layoutSubtreeIfNeeded()
            container.refreshViewport(immediately: true)
            container.window?.makeFirstResponder(remoteView)
        }
        return container
    }

    func updateNSView(_ nsView: NSView, context: Context) {
        guard let container = nsView as? EmbeddedRDPContainerView else { return }
        container.onViewportChange = { [weak session] size, backingScaleFactor, immediately in
            session?.updateViewport(
                pointSize: size,
                backingScaleFactor: backingScaleFactor,
                immediately: immediately
            )
        }
        // State-only renders must not turn a failed negotiation into an
        // endless forced retry. Geometry/window notifications own requests.
        container.refreshViewport(immediately: false)
    }

    static func dismantleNSView(_ nsView: NSView, coordinator: Void) {
        (nsView as? EmbeddedRDPContainerView)?.invalidate()
        nsView.subviews.forEach { $0.removeFromSuperview() }
    }
}
