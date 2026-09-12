import AppKit
import CryptoKit
import Darwin
import Foundation
import Testing
@testable import VCWorkspace

private func gatewayDescriptor(url: String = "wss://gateway.example/gateway/v1/rdp",
                               subprotocol: String = NativeGatewayConnection.subprotocolName,
                               ticket: String = "gwt_" + String(repeating: "A", count: 43),
                               expiresAt: Date = .now.addingTimeInterval(30),
                               pin: String = String(repeating: "a", count: 64)) -> NativeGatewayConnection {
    NativeGatewayConnection(url: url, subprotocol: subprotocol, ticket: ticket,
                            expiresAt: expiresAt, certificateSHA256: pin)
}

@Test func gatewayDescriptorRejectsAmbiguousOrInsecureAuthority() throws {
    for url in ["ws://gateway.example/gateway/v1/rdp", "https://gateway.example/gateway/v1/rdp",
                "wss://user:secret@gateway.example/gateway/v1/rdp",
                "wss://gateway.example/gateway/v1/rdp?", "wss://gateway.example/gateway/v1/rdp#",
                "wss://gateway.example:0/gateway/v1/rdp", "wss://gateway.example:65536/gateway/v1/rdp",
                "wss://gateway.example:/gateway/v1/rdp", "wss://gateway.example/gateway/v1/rdp\n",
                "wss://gate way.example/gateway/v1/rdp",
                "wss://gateway.example/gateway/v1/%72dp", "wss://gateway.example/gateway/v1/rdp/",
                "wss://gateway.example/other", "wss://[fe80::1%25en0]/gateway/v1/rdp"] {
        #expect(throws: GatewayTunnelError.self) { try gatewayDescriptor(url: url).validatedURL() }
    }
    #expect(try gatewayDescriptor().validatedURL().scheme == "wss")
    #expect(try gatewayDescriptor(url: "wss://gateway.example:8443/gateway/v1/rdp").validatedURL().port == 8443)
    #expect(throws: GatewayTunnelError.self) { try gatewayDescriptor(expiresAt: .distantPast).validatedURL() }
    #expect(throws: GatewayTunnelError.self) { try gatewayDescriptor(subprotocol: "other").validatedURL() }
    for pin in ["", String(repeating: "A", count: 64), String(repeating: "0", count: 63), String(repeating: "g", count: 64)] {
        #expect(throws: GatewayTunnelError.self) { try gatewayDescriptor(pin: pin).validatedURL() }
    }
    for ticket in ["", "gwt_" + String(repeating: "A", count: 42),
                   "gwt_" + String(repeating: "A", count: 42) + "B", // Noncanonical padding bits.
                   "gwt_" + String(repeating: "+", count: 43)] {
        #expect(throws: GatewayTunnelError.self) { try gatewayDescriptor(ticket: ticket).validatedURL() }
    }
}

@Test @MainActor func gatewaySessionsNeverShareBrowserCredentialsOrCache() {
    let configuration = GatewayTunnel.configuration()
    #expect(configuration.tlsMinimumSupportedProtocolVersion == .TLSv13)
    #expect(configuration.httpCookieStorage == nil)
    #expect(configuration.urlCredentialStorage == nil)
    #expect(configuration.urlCache == nil)
    #expect(!configuration.httpShouldSetCookies)
    #expect(!configuration.waitsForConnectivity)
    #expect(configuration.requestCachePolicy == .reloadIgnoringLocalCacheData)
}

@Test @MainActor func gatewayIssuanceAllowsBoundedClockSkewWithoutExtendingExpiry() throws {
    let now = Date(timeIntervalSince1970: 1_800_000_000)
    let policy = NativeSessionPolicy(version: 1, revision: 4, clipboardRedirection: false,
        driveRedirection: false, managedBackground: true,
        hash: "sha256:cc5afecf0f8597a5b2ba8f2f5fd793ca32ba74566d0a4547496c5e1546e98654")
    func connection(skew: TimeInterval, ticketLifetime: TimeInterval = 30,
                    lifetime: TimeInterval = 300) -> DesktopConnection {
        DesktopConnection(id: "clock-test", protocolName: "rdp-gateway", host: "192.0.2.1", port: 3389,
            username: "test-only", password: "test-only", issuedAt: now.addingTimeInterval(skew),
            expiresAt: now.addingTimeInterval(lifetime), desktopID: "160", sessionPolicy: policy,
            gateway: gatewayDescriptor(expiresAt: now.addingTimeInterval(ticketLifetime)))
    }
    for skew: TimeInterval in [-1, 0, 0.033, 0.168, 5] {
        try EmbeddedRDPTransport.validate(connection(skew: skew), now: now)
    }
    #expect(throws: EmbeddedRDPError.self) { try EmbeddedRDPTransport.validate(connection(skew: 5.001), now: now) }
    #expect(throws: EmbeddedRDPError.self) {
        try EmbeddedRDPTransport.validate(connection(skew: 1e18), now: now)
    }
    for expiry: TimeInterval in [0, -0.001, -5] {
        #expect(throws: GatewayTunnelError.self) {
            try EmbeddedRDPTransport.validate(connection(skew: 0.033, ticketLifetime: expiry), now: now)
        }
    }
    #expect(throws: EmbeddedRDPError.self) {
        try EmbeddedRDPTransport.validate(connection(skew: 0, ticketLifetime: 31, lifetime: 30), now: now)
    }
    #expect(throws: EmbeddedRDPError.self) {
        try EmbeddedRDPTransport.validate(connection(skew: 5, ticketLifetime: 1, lifetime: 5), now: now)
    }
}

private func socketPair() throws -> (GatewaySocket, GatewaySocket) {
    var fds: [Int32] = [-1, -1]
    guard Darwin.socketpair(AF_UNIX, SOCK_STREAM, 0, &fds) == 0 else { throw GatewayTunnelError.unavailable }
    for fd in fds {
        var one: Int32 = 1
        _ = setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout.size(ofValue: one)))
    }
    return (GatewaySocket(descriptor: fds[0]), GatewaySocket(descriptor: fds[1]))
}

private func readBytes(_ socket: GatewaySocket, count: Int) async throws -> Data {
    var output = Data()
    while output.count < count {
        var batchCount = 0
        for try await data in socket.readBatch() {
            output.append(data)
            batchCount += data.count
            if output.count >= count { return output }
        }
        if batchCount == 0 { break }
    }
    return output
}

@Test func gatewaySocketDeliversPartialBatchesWithoutWaitingFor32KiB() async throws {
    let (sender, receiver) = try socketPair()
    defer { sender.close(); receiver.close() }
    let deadline = Task { try? await Task.sleep(for: .seconds(3)); if !Task.isCancelled { sender.close(); receiver.close() } }
    defer { deadline.cancel() }
    let expected = Data([0, 255, 3, 4, 5])
    async let received = readBytes(receiver, count: expected.count)
    try await sender.write(expected)
    #expect(try await received == expected)
}

@Test func gatewaySocketCloseUnblocksBackpressuredIO() async throws {
    let (sender, receiver) = try socketPair()
    defer { sender.close(); receiver.close() }
    let start = ContinuousClock.now
    let write = Task { try await sender.write(Data(repeating: 7, count: 8 * 1024 * 1024)) }
    try await Task.sleep(for: .milliseconds(50))
    sender.close()
    do { try await write.value; Issue.record("A blocked write must fail after close") }
    catch { #expect(error is GatewayTunnelError) }
    #expect(start.duration(to: .now) < .seconds(2))
    receiver.close()
    do { for try await _ in receiver.readBatch() { Issue.record("Closed socket cannot yield bytes") } }
    catch { #expect(error is GatewayTunnelError) }
}

private struct GatewayFixtureMetadata: Decodable {
    let url: String
    let ca: Data
    let ticket: String
    let certificate_sha256: String
    var descriptor: NativeGatewayConnection {
        gatewayDescriptor(url: url, ticket: ticket, pin: certificate_sha256)
    }
}

private struct GatewayFixtureResult: Decodable {
    let requests, authorized, closed, bytes: Int
    let guest_tls, guest_error, peer_closed: Bool
}

private enum GatewayFixtureError: Error { case invalidOutput }

// Metadata (including the one-time test ticket) stays inside anonymous pipes;
// test failures print counters only. The Go process has its own 45-second limit.
@MainActor
private final class GatewayFixture {
    let process = Process()
    let input = Pipe()
    let output = Pipe()
    let metadata: GatewayFixtureMetadata

    init(mode: String) async throws {
        process.executableURL = URL(fileURLWithPath: ProcessInfo.processInfo.environment["VC_WORKSPACE_TEST_GATEWAY_FIXTURE"]!)
        process.arguments = ["-test.run", "^TestMacGatewayFixture$"]
        var environment = ProcessInfo.processInfo.environment
        environment["VC_WORKSPACE_MAC_GATEWAY_FIXTURE"] = mode
        process.environment = environment
        process.standardInput = input
        process.standardOutput = output
        process.standardError = FileHandle.nullDevice
        try process.run()
        let handle = output.fileHandleForReading
        do {
            metadata = try await Task.detached {
                var line = Data()
                while line.count < 8192 {
                    guard let byte = try handle.read(upToCount: 1), !byte.isEmpty else { throw GatewayFixtureError.invalidOutput }
                    if byte == Data([10]) { break }
                    line.append(byte)
                }
                let prefix = Data("VCW_FIXTURE ".utf8)
                guard line.starts(with: prefix) else { throw GatewayFixtureError.invalidOutput }
                return try JSONDecoder().decode(GatewayFixtureMetadata.self, from: line.dropFirst(prefix.count))
            }.value
        } catch {
            try? input.fileHandleForWriting.close()
            process.terminate()
            throw GatewayFixtureError.invalidOutput
        }
    }

    func finish() async throws -> GatewayFixtureResult {
        try input.fileHandleForWriting.close()
        let handle = output.fileHandleForReading
        let remaining = try await Task.detached { try handle.readToEnd() ?? Data() }.value
        // Process is launched/observed on the main run loop. waitUntilExit on
        // a different cooperative thread can miss its termination wake-up.
        let deadline = ContinuousClock.now + .seconds(3)
        while process.isRunning, ContinuousClock.now < deadline { try await Task.sleep(for: .milliseconds(10)) }
        guard !process.isRunning else { process.terminate(); throw GatewayFixtureError.invalidOutput }
        #expect(process.terminationStatus == 0)
        guard let text = String(data: remaining, encoding: .utf8),
              let line = text.split(separator: "\n").first(where: { $0.hasPrefix("VCW_RESULT ") }) else {
            throw GatewayFixtureError.invalidOutput
        }
        return try JSONDecoder().decode(GatewayFixtureResult.self, from: Data(line.dropFirst(11).utf8))
    }

    deinit {
        try? input.fileHandleForWriting.close()
        if process.isRunning { process.terminate() }
    }
}

@Suite(.serialized, .enabled(if: ProcessInfo.processInfo.environment["VC_WORKSPACE_TEST_GATEWAY_FIXTURE"] != nil))
@MainActor
struct GatewayInteropTests {
    private func awaitClosed(_ tunnel: GatewayTunnel) async throws {
        let deadline = ContinuousClock.now + .seconds(4)
        while !tunnel.ended, ContinuousClock.now < deadline { try await Task.sleep(for: .milliseconds(10)) }
        #expect(tunnel.ended)
        #expect(tunnel.nativeDescriptor == -1)
    }

    @Test func actualPublicGatewayEchoesBinaryAndClosesTheGuest() async throws {
        let fixture = try await GatewayFixture(mode: "echo")
        let tunnel = try await GatewayTunnel.openForTesting(fixture.metadata.descriptor, anchor: fixture.metadata.ca)
        defer { tunnel.close() }
        let fd = dup(tunnel.nativeDescriptor)
        #expect(fd >= 0)
        let peer = GatewaySocket(descriptor: fd)
        defer { peer.close() }
        tunnel.releaseNativeDescriptor()
        let deadline = Task { try? await Task.sleep(for: .seconds(5)); if !Task.isCancelled { tunnel.close(); peer.close() } }
        defer { deadline.cancel() }
        let expected = Data((0..<262144).map { UInt8(truncatingIfNeeded: $0) })
        async let received = readBytes(peer, count: expected.count)
        try await peer.write(expected)
        let actual = try await received
        #expect(actual.count == expected.count)
        #expect(actual == expected)
        peer.close()
        try await awaitClosed(tunnel)
        let result = try await fixture.finish()
        #expect(result.requests == 1)
        #expect(result.authorized == 1)
        #expect(result.closed == 1)
        #expect(result.bytes == expected.count)
        #expect(result.peer_closed)
    }

    @Test func revokedLeaseClosesAnIdleClientWithoutWaitingForInput() async throws {
        let fixture = try await GatewayFixture(mode: "revoke")
        let tunnel = try await GatewayTunnel.openForTesting(fixture.metadata.descriptor, anchor: fixture.metadata.ca)
        defer { tunnel.close() }
        try await awaitClosed(tunnel)
        let result = try await fixture.finish()
        #expect(result.authorized == 1)
        #expect(result.closed == 1)
        #expect(result.peer_closed)
    }

    // Explicit long-running transport regression, not an OS/RDP or WAN proof.
    // Pause the consumer while sending multi-megabyte bursts, then leave the
    // same connection idle beyond ordinary HTTP timeouts before sending again.
    @Test(.enabled(if: ProcessInfo.processInfo.environment["VC_WORKSPACE_TEST_GATEWAY_SOAK"] == "true"))
    func sustainedBurstsAndLongIdleKeepTheSameAuthorizedTunnel() async throws {
        let fixture = try await GatewayFixture(mode: "soak")
        let tunnel = try await GatewayTunnel.openForTesting(fixture.metadata.descriptor, anchor: fixture.metadata.ca)
        defer { tunnel.close() }
        let fd = dup(tunnel.nativeDescriptor)
        try #require(fd >= 0)
        let peer = GatewaySocket(descriptor: fd)
        defer { peer.close() }
        tunnel.releaseNativeDescriptor()
        let watchdog = Task {
            try? await Task.sleep(for: .seconds(180))
            if !Task.isCancelled { tunnel.close(); peer.close() }
        }
        defer { watchdog.cancel() }
        var generator: UInt64 = 0x51c095876734abde
        let payload = Data((0..<(2 * 1024 * 1024)).map { _ in
            generator = generator &* 6364136223846793005 &+ 1442695040888963407
            return UInt8(truncatingIfNeeded: generator >> 32)
        })
        let digest = SHA256.hash(data: payload)
        var exchanged = 0
        for iteration in 0..<60 {
            let sending = Task { try await peer.write(payload) }
            if iteration.isMultiple(of: 10) { try await Task.sleep(for: .milliseconds(250)) }
            let received = try await readBytes(peer, count: payload.count)
            try await sending.value
            try #require(received.count == payload.count)
            try #require(SHA256.hash(data: received) == digest)
            try #require(!tunnel.ended)
            exchanged += payload.count
            try await Task.sleep(for: .milliseconds(1500))
        }
        print("Gateway soak: 120 MiB round trip verified; starting 60-second idle interval")
        try await Task.sleep(for: .seconds(60))
        try #require(!tunnel.ended)
        async let finalRead = readBytes(peer, count: 1)
        try await peer.write(Data([42]))
        let finalBytes = try await finalRead
        try #require(finalBytes == Data([42]))
        peer.close()
        try await awaitClosed(tunnel)
        let result = try await fixture.finish()
        #expect(result.requests == 1)
        #expect(result.authorized == 1)
        #expect(result.closed == 1)
        #expect(result.bytes == exchanged + 1)
        #expect(result.peer_closed)
    }

    @Test func redeemedTunnelOutlivesTicketAndHandshakeTimeoutWhileIdle() async throws {
        let fixture = try await GatewayFixture(mode: "echo")
        let descriptor = gatewayDescriptor(url: fixture.metadata.url, ticket: fixture.metadata.ticket,
            expiresAt: .now.addingTimeInterval(2), pin: fixture.metadata.certificate_sha256)
        let tunnel = try await GatewayTunnel.openForTesting(descriptor, anchor: fixture.metadata.ca)
        defer { tunnel.close() }
        try await Task.sleep(for: .seconds(11))
        #expect(!tunnel.ended, "Ticket/handshake deadlines must not cap an authorized, idle stream")
        tunnel.close()
        let result = try await fixture.finish()
        #expect(result.authorized == 1)
        #expect(result.closed == 1)
        #expect(result.peer_closed)
    }

    @Test func consumedTicketCannotOpenAnotherNativeTunnel() async throws {
        let fixture = try await GatewayFixture(mode: "echo")
        let tunnel = try await GatewayTunnel.openForTesting(fixture.metadata.descriptor, anchor: fixture.metadata.ca)
        tunnel.close()
        try await Task.sleep(for: .milliseconds(100))
        do {
            let replay = try await GatewayTunnel.openForTesting(fixture.metadata.descriptor, anchor: fixture.metadata.ca)
            replay.close()
            Issue.record("Consumed ticket was replayed")
        } catch { #expect(error is GatewayTunnelError) }
        let result = try await fixture.finish()
        #expect(result.requests == 2)
        #expect(result.authorized == 1)
        #expect(result.closed == 1)
        #expect(result.peer_closed)
    }

    @Test func invalidPolicyIsRejectedBeforeOpeningGateway() async throws {
        let fixture = try await GatewayFixture(mode: "echo")
        let policy = NativeSessionPolicy(version: 1, revision: 4, clipboardRedirection: false,
            driveRedirection: false, managedBackground: true, hash: "invalid")
        let connection = DesktopConnection(id: "invalid", protocolName: "rdp-gateway", host: "192.0.2.1", port: 3389,
            username: "test-only", password: "test-only", issuedAt: .now, expiresAt: .now.addingTimeInterval(300),
            desktopID: "160", sessionPolicy: policy, gateway: fixture.metadata.descriptor)
        do {
            let transport = try await EmbeddedRDPTransport.open(connection: connection, quality: .adaptive)
            transport.destroy()
            Issue.record("Invalid policy was accepted")
        } catch { #expect(error is EmbeddedRDPError) }
        let result = try await fixture.finish()
        #expect(result.requests == 0)
        #expect(result.authorized == 0)
    }

    @Test(arguments: ["redirect", "wrong_protocol", "binary_ready"])
    func handshakeRejectsUnexpectedResponses(mode: String) async throws {
        let fixture = try await GatewayFixture(mode: mode)
        do {
            let tunnel = try await GatewayTunnel.openForTesting(fixture.metadata.descriptor, anchor: fixture.metadata.ca)
            tunnel.close()
            Issue.record("Invalid handshake was accepted")
        } catch { #expect(error is GatewayTunnelError) }
        let result = try await fixture.finish()
        #expect(result.requests == 1, "Redirects must not be followed")
        #expect(result.authorized == 0)
    }

    @Test(arguments: ["text", "oversized"])
    func invalidPostHandshakeFramesCloseTheTunnel(mode: String) async throws {
        let fixture = try await GatewayFixture(mode: mode)
        let tunnel = try await GatewayTunnel.openForTesting(fixture.metadata.descriptor, anchor: fixture.metadata.ca)
        defer { tunnel.close() }
        try await awaitClosed(tunnel)
        _ = try await fixture.finish()
    }

    @Test func untrustedGatewayIsRejectedBeforeSendingTicket() async throws {
        let fixture = try await GatewayFixture(mode: "echo")
        do {
            let tunnel = try await GatewayTunnel.open(fixture.metadata.descriptor)
            tunnel.close()
            Issue.record("System trust must reject the isolated test CA")
        } catch { #expect(error is GatewayTunnelError) }
        let result = try await fixture.finish()
        #expect(result.requests == 0)
        #expect(result.authorized == 0)
    }

    @Test func cancelDuringReadyWaitClosesPromptly() async throws {
        let fixture = try await GatewayFixture(mode: "stall")
        let pending = Task { try await GatewayTunnel.openForTesting(fixture.metadata.descriptor, anchor: fixture.metadata.ca) }
        try await Task.sleep(for: .milliseconds(200))
        let start = ContinuousClock.now
        pending.cancel()
        do { let tunnel = try await pending.value; tunnel.close(); Issue.record("Cancelled connection was accepted") }
        catch { #expect(error is CancellationError) }
        #expect(start.duration(to: .now) < .seconds(2))
        _ = try await fixture.finish()
    }

    @Test(.enabled(if: ProcessInfo.processInfo.environment["VC_WORKSPACE_RDP_RUNTIME_PATH"] != nil), arguments: [true, false])
    func bundledNativeBridgePinsTheGuestInsideWSS(matchingPin: Bool) async throws {
        let fixture = try await GatewayFixture(mode: "native_pin")
        let descriptor = gatewayDescriptor(url: fixture.metadata.url, ticket: fixture.metadata.ticket,
                                          pin: matchingPin ? fixture.metadata.certificate_sha256 : String(repeating: "0", count: 64))
        let tunnel = try await GatewayTunnel.openForTesting(descriptor, anchor: fixture.metadata.ca)
        defer { tunnel.close() }
        let policy = NativeSessionPolicy(version: 1, revision: 4, clipboardRedirection: false,
            driveRedirection: false, managedBackground: true,
            hash: "sha256:cc5afecf0f8597a5b2ba8f2f5fd793ca32ba74566d0a4547496c5e1546e98654")
        let connection = DesktopConnection(id: "isolated", protocolName: "rdp-gateway", host: "192.0.2.1", port: 3389,
            username: "test-only", password: "test-only", issuedAt: .now, expiresAt: .now.addingTimeInterval(300),
            desktopID: "160", sessionPolicy: policy, gateway: descriptor)
        _ = NSApplication.shared
        let transport = try EmbeddedRDPTransport.native(connection: connection, quality: .adaptive, tunnel: tunnel)
        tunnel.releaseNativeDescriptor()
        defer { transport.destroy() }
        let deadline = ContinuousClock.now + .seconds(10)
        while transport.state() < 2, ContinuousClock.now < deadline { try await Task.sleep(for: .milliseconds(20)) }
        #expect(transport.state() == 3, "Fixture intentionally stops before desktop negotiation completes")
        #expect((transport.error() == "VCW_GATEWAY_CERTIFICATE_REJECTED") == !matchingPin)
        transport.stop()
        let result = try await fixture.finish()
        #expect(result.authorized == 1)
        #expect(result.closed == 1)
        #expect(result.guest_tls)
        #expect(!result.guest_error)
        #expect(matchingPin ? result.bytes > 0 : result.bytes == 0)
        #expect(result.peer_closed)
    }
}
