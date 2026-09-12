import Darwin
import Foundation
import OSLog
import Security

struct NativeGatewayConnection: Decodable {
    static let subprotocolName = "vc-workspace-rdp.v1"
    let url: String
    let subprotocol: String
    let ticket: String
    let expiresAt: Date
    let certificateSHA256: String

    enum CodingKeys: String, CodingKey {
        case url, subprotocol, ticket
        case expiresAt = "expires_at"
        case certificateSHA256 = "certificate_sha256"
    }

    func validatedURL(now: Date = .now) throws -> URL {
        guard url.hasPrefix("wss://"), !url.unicodeScalars.contains(where: { CharacterSet.whitespacesAndNewlines.contains($0) || CharacterSet.controlCharacters.contains($0) }),
              !url.dropFirst(6).prefix(while: { $0 != "/" }).hasSuffix(":"),
              let parts = URLComponents(string: url), parts.scheme == "wss",
              let host = parts.host, !host.isEmpty, !host.contains("%"),
              parts.user == nil, parts.password == nil, parts.query == nil, parts.fragment == nil,
              parts.percentEncodedPath == "/gateway/v1/rdp",
              parts.port == nil || (1...65535).contains(parts.port!),
              let target = parts.url, subprotocol == Self.subprotocolName,
              expiresAt > now, certificateSHA256.utf8.count == 64,
              certificateSHA256.utf8.allSatisfy({ (48...57).contains($0) || (97...102).contains($0) }),
              ticket.utf8.count == 47, ticket.hasPrefix("gwt_") else { throw GatewayTunnelError.invalidDescriptor }
        let encoded = String(ticket.dropFirst(4))
        let base64 = encoded.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/") + "="
        guard let bytes = Data(base64Encoded: base64), bytes.count == 32,
              bytes.base64EncodedString().replacingOccurrences(of: "+", with: "-")
                .replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "") == encoded else {
            throw GatewayTunnelError.invalidDescriptor
        }
        return target
    }
}

enum GatewayTunnelError: LocalizedError {
    case invalidDescriptor, unavailable, rejected, closed
    var errorDescription: String? {
        switch self {
        case .invalidDescriptor: return "网关连接信息无效或已过期，请重新连接"
        case .unavailable: return "无法连接桌面网关，请检查网络后重试"
        case .rejected: return "桌面网关未授权此连接，请重新连接"
        case .closed: return "桌面网关连接已断开"
        }
    }
}

private enum GatewayStreamError: Error {
    case socketRead(Int32), socketWrite(Int32), unexpectedText, invalidBinarySize(Int)
}

// No TCP listener and no pathname socket: only this process owns the two ends.
// DispatchIO performs non-blocking I/O without blocking Swift's cooperative pool.
// Each read batch queues at most 32 KiB, even if the WebSocket sender stalls.
final class GatewaySocket: @unchecked Sendable {
    private let channel: DispatchIO
    private let queue = DispatchQueue(label: "ac.plz.vc-workspace.gateway-socket")

    init(descriptor: Int32) {
        channel = DispatchIO(type: .stream, fileDescriptor: descriptor, queue: queue) { _ in
            Darwin.close(descriptor)
        }
        channel.setLimit(lowWater: 1)
        channel.setLimit(highWater: 32768)
    }

    func readBatch() -> AsyncThrowingStream<Data, Error> {
        AsyncThrowingStream { continuation in
            channel.read(offset: 0, length: 32768, queue: queue) { done, data, error in
                if let data, !data.isEmpty { continuation.yield(Data(data)) }
                if done {
                    if error == 0 { continuation.finish() }
                    else if error == ECANCELED { continuation.finish(throwing: GatewayTunnelError.closed) }
                    else { continuation.finish(throwing: GatewayStreamError.socketRead(error)) }
                }
            }
        }
    }

    func write(_ data: Data) async throws {
        let bytes = data.withUnsafeBytes { DispatchData(bytes: $0) }
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
            channel.write(offset: 0, data: bytes, queue: queue) { done, _, error in
                guard done else { return }
                if error == 0 { continuation.resume() }
                else if error == ECANCELED { continuation.resume(throwing: GatewayTunnelError.closed) }
                else { continuation.resume(throwing: GatewayStreamError.socketWrite(error)) }
            }
        }
    }

    func close() { channel.close(flags: .stop) }
    deinit { channel.close(flags: .stop) }
}

private final class GatewayHandshake: @unchecked Sendable {
    private let lock = NSLock()
    private var result: Result<Void, Error>?
    private var continuation: CheckedContinuation<Void, Error>?

    func resolve(_ value: Result<Void, Error>) {
        lock.lock()
        guard result == nil else { lock.unlock(); return }
        result = value
        let pending = continuation
        continuation = nil
        lock.unlock()
        pending?.resume(with: value)
    }

    func wait() async throws {
        try await withCheckedThrowingContinuation { pending in
            lock.lock()
            if let result { lock.unlock(); pending.resume(with: result) }
            else { continuation = pending; lock.unlock() }
        }
    }
}

private final class GatewaySessionDelegate: NSObject, URLSessionWebSocketDelegate, @unchecked Sendable {
    let handshake = GatewayHandshake()
#if DEBUG
    // Only compiled into test/debug builds. Never install a CA in the user's
    // Keychain or ship a runtime certificate-validation bypass.
    var testAnchor: SecCertificate?
#endif

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol protocolName: String?) {
        handshake.resolve(protocolName == NativeGatewayConnection.subprotocolName ? .success(()) : .failure(GatewayTunnelError.rejected))
    }
    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didCloseWith closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?) {
        handshake.resolve(.failure(GatewayTunnelError.closed))
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        handshake.resolve(.failure(GatewayTunnelError.unavailable))
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
        handshake.resolve(.failure(GatewayTunnelError.rejected))
    }
    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        authenticate(challenge, completionHandler)
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        authenticate(challenge, completionHandler)
    }
    private func authenticate(_ challenge: URLAuthenticationChallenge, _ complete: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
#if DEBUG
        if let anchor = testAnchor, challenge.protectionSpace.host == "127.0.0.1",
           challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
           let trust = challenge.protectionSpace.serverTrust {
            if SecTrustSetAnchorCertificates(trust, [anchor] as CFArray) == errSecSuccess,
               SecTrustSetAnchorCertificatesOnly(trust, true) == errSecSuccess,
               SecTrustEvaluateWithError(trust, nil) {
                complete(.useCredential, URLCredential(trust: trust))
            } else { complete(.cancelAuthenticationChallenge, nil) }
            return
        }
#endif
        complete(challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust ? .performDefaultHandling : .cancelAuthenticationChallenge, nil)
    }
}

@MainActor
final class GatewayTunnel {
    private static let logger = Logger(subsystem: "ac.plz.vc-workspace", category: "gateway-transport")
    private var bridgeDescriptor: Int32
    private let socket: GatewaySocket
    private let session: URLSession
    private let webSocket: URLSessionWebSocketTask
    private let delegate: GatewaySessionDelegate
    private var reader: Task<Void, Never>?
    private var writer: Task<Void, Never>?
    private(set) var ended = false

    static func configuration() -> URLSessionConfiguration {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.tlsMinimumSupportedProtocolVersion = .TLSv13
        configuration.httpShouldSetCookies = false
        configuration.httpCookieStorage = nil
        configuration.urlCredentialStorage = nil
        configuration.urlCache = nil
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        configuration.waitsForConnectivity = false
        configuration.timeoutIntervalForRequest = 10
        configuration.timeoutIntervalForResource = 9 * 60 * 60
        return configuration
    }

    private init(url: URL, sessionDelegate: GatewaySessionDelegate) throws {
        var pair: [Int32] = [-1, -1]
        guard socketpair(AF_UNIX, SOCK_STREAM, 0, &pair) == 0 else { throw GatewayTunnelError.unavailable }
        for fd in pair {
            var one: Int32 = 1
            guard fcntl(fd, F_SETFD, FD_CLOEXEC) == 0,
                  setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout.size(ofValue: one))) == 0 else {
                pair.forEach { Darwin.close($0) }
                throw GatewayTunnelError.unavailable
            }
        }
        bridgeDescriptor = pair[0]
        socket = GatewaySocket(descriptor: pair[1])
        delegate = sessionDelegate
        session = URLSession(configuration: Self.configuration(), delegate: delegate, delegateQueue: nil)
        webSocket = session.webSocketTask(with: url, protocols: [NativeGatewayConnection.subprotocolName])
        webSocket.maximumMessageSize = 65536
    }

    static func open(_ descriptor: NativeGatewayConnection) async throws -> GatewayTunnel {
        try await open(descriptor, delegate: GatewaySessionDelegate())
    }

#if DEBUG
    static func openForTesting(_ descriptor: NativeGatewayConnection, anchor: Data) async throws -> GatewayTunnel {
        guard try descriptor.validatedURL().host == "127.0.0.1",
              let certificate = SecCertificateCreateWithData(nil, anchor as CFData) else { throw GatewayTunnelError.invalidDescriptor }
        let delegate = GatewaySessionDelegate()
        delegate.testAnchor = certificate
        return try await open(descriptor, delegate: delegate)
    }
#endif

    private static func open(_ descriptor: NativeGatewayConnection, delegate: GatewaySessionDelegate) async throws -> GatewayTunnel {
        let url = try descriptor.validatedURL()
        try Task.checkCancellation()
        let tunnel = try GatewayTunnel(url: url, sessionDelegate: delegate)
        let deadline = Task { @MainActor in
            try? await Task.sleep(for: .seconds(min(10, max(0, descriptor.expiresAt.timeIntervalSinceNow))))
            if !Task.isCancelled { tunnel.close() }
        }
        defer { deadline.cancel() }
        do {
            try await withTaskCancellationHandler {
                tunnel.webSocket.resume()
                try await tunnel.delegate.handshake.wait()
                try Task.checkCancellation()
                try await tunnel.webSocket.send(.string(descriptor.ticket))
                guard case .string("ready") = try await tunnel.webSocket.receive() else { throw GatewayTunnelError.rejected }
                try Task.checkCancellation()
                guard !tunnel.ended else { throw GatewayTunnelError.closed }
                tunnel.startPumps()
            } onCancel: {
                Task { @MainActor in tunnel.close() }
            }
            return tunnel
        } catch {
            tunnel.close()
            if Task.isCancelled { throw CancellationError() }
            // Foundation diagnostics can include an endpoint; don't send them
            // to the UI/log or preserve remote error/close frame contents.
            throw (error as? GatewayTunnelError) ?? GatewayTunnelError.unavailable
        }
    }

    // The C bridge duplicates this descriptor before returning. Release our
    // copy immediately afterwards, including if native creation fails.
    var nativeDescriptor: Int32 { ended ? -1 : bridgeDescriptor }
    func releaseNativeDescriptor() {
        if bridgeDescriptor >= 0 { Darwin.close(bridgeDescriptor); bridgeDescriptor = -1 }
    }

    func close() {
        guard !ended else { return }
        ended = true
        delegate.handshake.resolve(.failure(GatewayTunnelError.closed))
        webSocket.cancel()
        session.invalidateAndCancel()
        socket.close()
        releaseNativeDescriptor()
        reader?.cancel(); writer?.cancel()
        reader = nil; writer = nil
    }

    private func startPumps() {
        reader = Task { [weak self, socket, webSocket] in
            do {
                while !Task.isCancelled {
                    var count = 0
                    for try await data in socket.readBatch() {
                        try Task.checkCancellation()
                        count += data.count
                        try await webSocket.send(.data(data))
                    }
                    if count == 0 { break }
                }
            } catch { self?.recordFailure(error, direction: "outbound") }
            self?.close()
        }
        writer = Task { [weak self, socket, webSocket] in
            do {
                while !Task.isCancelled {
                    guard case .data(let data) = try await webSocket.receive() else { throw GatewayStreamError.unexpectedText }
                    guard !data.isEmpty, data.count <= 65536 else { throw GatewayStreamError.invalidBinarySize(data.count) }
                    try await socket.write(data)
                }
            } catch { self?.recordFailure(error, direction: "inbound") }
            self?.close()
        }
    }

    private func recordFailure(_ error: Error, direction: String) {
        guard !ended, !Task.isCancelled else { return }
        let value = error as NSError
        // Only locally defined domains/codes. Never log peer messages, URLs,
        // ticket frames, Foundation userInfo or arbitrary error descriptions.
        var domain = value.domain == NSURLErrorDomain ? "url" : value.domain == NSPOSIXErrorDomain ? "posix" : "transport"
        var code = domain == "transport" ? 0 : value.code
        if let stream = error as? GatewayStreamError {
            switch stream {
            case .socketRead(let errno): domain = "socket_read"; code = Int(errno)
            case .socketWrite(let errno): domain = "socket_write"; code = Int(errno)
            case .unexpectedText: domain = "unexpected_text"; code = 0
            case .invalidBinarySize(let size): domain = "invalid_binary_size"; code = size
            }
        }
        Self.logger.notice("tunnel ended direction=\(direction, privacy: .public) domain=\(domain, privacy: .public) code=\(code) close=\(self.webSocket.closeCode.rawValue)")
    }

    deinit {
        webSocket.cancel()
        session.invalidateAndCancel()
        socket.close()
        if bridgeDescriptor >= 0 { Darwin.close(bridgeDescriptor) }
        reader?.cancel(); writer?.cancel()
    }
}
