import AppKit
import Darwin
import Foundation
import SwiftUI

enum EmbeddedRDPError: LocalizedError {
    case invalidConnection
    case runtimeUnavailable
    case incompatibleRuntime
    case symbolMissing(String)
    case sessionCreationFailed

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

@MainActor
final class EmbeddedRDPSession: ObservableObject, Identifiable {
    enum State: Int32 {
        case connecting = 0
        case connected = 1
        case closed = 2
        case failed = 3
    }

    let id = UUID()
    let vmid: Int
    let desktopName: String
    let view: NSView
    @Published private(set) var state: State = .connecting

    private let runtime: EmbeddedRDPRuntime
    private var handle: UnsafeMutableRawPointer?
    private var pollTimer: Timer?
    private var finished = false
    private let onTermination: @MainActor (EmbeddedRDPSessionEnd) -> Void

    init(
        connection: DesktopConnection,
        vmid: Int,
        desktopName: String,
        quality: ConnectionQuality,
        onTermination: @escaping @MainActor (EmbeddedRDPSessionEnd) -> Void
    ) throws {
        guard connection.protocolName == "rdp", !connection.host.isEmpty, connection.port > 0,
              !connection.username.isEmpty, !connection.password.isEmpty else {
            throw EmbeddedRDPError.invalidConnection
        }

        let runtime = try EmbeddedRDPRuntime()
        let sessionHandle = connection.host.withCString { host in
            connection.username.withCString { username in
                connection.password.withCString { password in
                    runtime.create(host, UInt16(connection.port), username, password, quality.runtimeValue)
                }
            }
        }
        guard let sessionHandle, let viewPointer = runtime.sessionView(sessionHandle) else {
            if let sessionHandle { runtime.destroy(sessionHandle) }
            throw EmbeddedRDPError.sessionCreationFailed
        }

        self.runtime = runtime
        self.handle = sessionHandle
        self.view = Unmanaged<NSView>.fromOpaque(viewPointer).takeUnretainedValue()
        self.vmid = vmid
        self.desktopName = desktopName
        self.onTermination = onTermination
        startPolling()
    }

    func disconnect() {
        guard let handle, !finished else { return }
        runtime.stop(handle)
        poll()
    }

    func destroy() {
        pollTimer?.invalidate()
        pollTimer = nil
        guard let handle else { return }
        self.handle = nil
        runtime.destroy(handle)
    }

    private func startPolling() {
        pollTimer = Timer.scheduledTimer(withTimeInterval: 0.25, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.poll() }
        }
        poll()
    }

    private func poll() {
        guard let handle, !finished else { return }
        guard let next = State(rawValue: runtime.sessionState(handle)) else { return }
        state = next
        switch next {
        case .connecting, .connected:
            return
        case .closed:
            finish(.closed)
        case .failed:
            let message = runtime.sessionError(handle).flatMap(String.init(validatingUTF8:)) ?? "远程桌面连接失败"
            finish(.failed(message.isEmpty ? "远程桌面连接失败" : message))
        }
    }

    private func finish(_ result: EmbeddedRDPSessionEnd) {
        guard !finished else { return }
        finished = true
        pollTimer?.invalidate()
        pollTimer = nil
        onTermination(result)
    }
}

private final class EmbeddedRDPRuntime {
    typealias Version = @convention(c) () -> Int32
    typealias Create = @convention(c) (
        UnsafePointer<CChar>, UInt16, UnsafePointer<CChar>, UnsafePointer<CChar>, Int32
    ) -> UnsafeMutableRawPointer?
    typealias SessionView = @convention(c) (UnsafeMutableRawPointer) -> UnsafeMutableRawPointer?
    typealias SessionState = @convention(c) (UnsafeMutableRawPointer) -> Int32
    typealias SessionError = @convention(c) (UnsafeMutableRawPointer) -> UnsafePointer<CChar>?
    typealias Stop = @convention(c) (UnsafeMutableRawPointer) -> Void
    typealias Destroy = @convention(c) (UnsafeMutableRawPointer) -> Void

    let create: Create
    let sessionView: SessionView
    let sessionState: SessionState
    let sessionError: SessionError
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
        guard version() == 1 else { throw EmbeddedRDPError.incompatibleRuntime }
        create = try Self.symbol("vcw_rdp_session_create", in: library)
        sessionView = try Self.symbol("vcw_rdp_session_view", in: library)
        sessionState = try Self.symbol("vcw_rdp_session_state", in: library)
        sessionError = try Self.symbol("vcw_rdp_session_error", in: library)
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

struct EmbeddedRDPView: NSViewRepresentable {
    let remoteView: NSView

    func makeNSView(context: Context) -> NSView {
        let container = NSView()
        container.wantsLayer = true
        container.layer?.backgroundColor = NSColor.black.cgColor
        remoteView.translatesAutoresizingMaskIntoConstraints = false
        container.addSubview(remoteView)
        NSLayoutConstraint.activate([
            remoteView.leadingAnchor.constraint(equalTo: container.leadingAnchor),
            remoteView.trailingAnchor.constraint(equalTo: container.trailingAnchor),
            remoteView.topAnchor.constraint(equalTo: container.topAnchor),
            remoteView.bottomAnchor.constraint(equalTo: container.bottomAnchor),
        ])
        DispatchQueue.main.async {
            container.window?.makeFirstResponder(remoteView)
        }
        return container
    }

    func updateNSView(_ nsView: NSView, context: Context) {}

    static func dismantleNSView(_ nsView: NSView, coordinator: Void) {
        nsView.subviews.forEach { $0.removeFromSuperview() }
    }
}
