import AppKit
import Foundation
import Testing
@testable import VCWorkspace

@MainActor
private final class TestTransport {
    var state: Int32 = 0
    var stopState: Int32 = 3
    var reads = 0
    var stops = 0
    var destroys = 0
    var viewports = 0
    var error = "ERRCONNECT_CONNECT_TRANSPORT_FAILED"

    func make() -> EmbeddedRDPTransport {
        EmbeddedRDPTransport(
            view: NSView(), state: { self.reads += 1; return self.state },
            displayState: { 0 }, error: { self.error },
            setViewport: { _, _, _ in self.viewports += 1 },
            stop: { self.stops += 1; self.state = self.stopState },
            destroy: { self.destroys += 1 }
        )
    }
}

private final class LifecycleURLProtocol: URLProtocol, @unchecked Sendable {
    // Old models can still be finishing asynchronous DELETEs when the next
    // test starts. Route by fixture, never through one replaceable handler.
    @MainActor static var handlers: [String: (URLRequest) async -> (Int, String)] = [:]
    override class func canInit(with _: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Task { @MainActor in
            guard let id = request.value(forHTTPHeaderField: "X-Test-Lifecycle"),
                  let handler = Self.handlers[id] else {
                client?.urlProtocol(self, didFailWithError: URLError(.badServerResponse))
                return
            }
            let (status, body) = await handler(request)
            let response = HTTPURLResponse(url: request.url!, statusCode: status,
                                           httpVersion: nil, headerFields: ["Content-Type": "application/json"])!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: Data(body.utf8))
            client?.urlProtocolDidFinishLoading(self)
        }
    }
    override func stopLoading() {}
}

@MainActor
private final class EndCheckGate {
    var requested = false
    private var continuation: CheckedContinuation<(Int, String), Never>?

    func wait() async -> (Int, String) {
        requested = true
        return await withCheckedContinuation { continuation = $0 }
    }

    func finish(status: Int, body: String) {
        continuation?.resume(returning: (status, body))
        continuation = nil
    }
}

@Suite(.serialized)
@MainActor
struct ConnectionLifecycleTests {
    private let desktop = Desktop(vmid: 160, name: "Lifecycle test", node: "test",
                                  status: "running", cpuCount: 2, memoryTotal: 2_147_483_648)

    private func makeModel(transports: [TestTransport],
                           events: @escaping (String) -> Void = { _ in },
                           response: ((URLRequest) async -> (Int, String)?)? = nil,
                           beforeSession: @escaping (Int) async -> Void = { _ in },
                           callbacks: @escaping (@escaping @MainActor (UUID, EmbeddedRDPSessionEnd) -> Void) -> Void = { _ in }) async -> AppModel {
        var created = 0
        let fixtureID = UUID().uuidString
        LifecycleURLProtocol.handlers[fixtureID] = { request in
            let path = request.url!.path
            events("\(request.httpMethod ?? "GET") \(path)")
            if let override = await response?(request) { return override }
            if path == "/api/v1/system" {
                return (200, #"{"name":"test","oidc_configured":false,"oidc_name":""}"#)
            }
            if path == "/api/v1/native/desktops" {
                return (200, #"{"desktops":[{"vmid":160,"name":"Lifecycle test","node":"test","status":"running","cpu_count":2,"memory_total":2147483648}]}"#)
            }
            if path.hasSuffix("/connections"), request.httpMethod == "POST" {
                created += 1
                return (201, """
                {"id":"attempt-\(created)","protocol":"rdp","host":"192.0.2.1","port":3389,
                 "username":"test-only","password":"temporary-\(created)","desktop_id":"160",
                 "issued_at":"2026-09-06T00:00:00Z","expires_at":"2026-09-06T08:00:00Z",
                 "session_policy":{"version":1,"revision":4,"clipboard_redirection":false,
                 "drive_redirection":false,"managed_background":true,
                 "hash":"sha256:cc5afecf0f8597a5b2ba8f2f5fd793ca32ba74566d0a4547496c5e1546e98654"}}
                """)
            }
            return (204, "")
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [LifecycleURLProtocol.self]
        config.httpAdditionalHeaders = ["X-Test-Lifecycle": fixtureID]
        let urlSession = URLSession(configuration: config)
        var index = 0
        var dependencies = AppModelDependencies()
        dependencies.makeClient = { try APIClient(server: $0, session: urlSession) }
        dependencies.loadToken = { _ in "test-only-token" }
        dependencies.saveToken = { _, _ in }
        dependencies.deleteToken = {}
        dependencies.saveServer = { _ in }
        dependencies.makeSession = { connection, vmid, name, _, callback in
            #expect(index < transports.count, "No unrequested native connection is permitted")
            guard index < transports.count else { throw EmbeddedRDPError.sessionCreationFailed }
            let transport = transports[index]
            index += 1
            #expect(connection.id == "attempt-\(index)")
            #expect(connection.password == "temporary-\(index)", "Recovery must obtain new credentials")
            await beforeSession(index)
            callbacks(callback)
            return EmbeddedRDPSession(transport: transport.make(), vmid: vmid,
                                      desktopName: name, onTermination: callback)
        }
        let model = AppModel(dependencies: dependencies)
        model.server = "http://127.0.0.1:8080"
        await model.start()
        #expect(model.state == .signedIn)
        return model
    }

    private func eventually(_ condition: () -> Bool) async throws {
        let deadline = ContinuousClock.now + .seconds(4)
        while !condition(), ContinuousClock.now < deadline {
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(condition())
    }

    @Test func immediateFailureWaitsForOwnershipAndNotifiesExactlyOnce() {
        let native = TestTransport()
        native.state = 3
        var owner: EmbeddedRDPSession?
        var ended = 0
        let session = EmbeddedRDPSession(transport: native.make(), vmid: 160, desktopName: "test") { id, result in
            #expect(owner?.id == id)
            #expect(result == .failed("ERRCONNECT_CONNECT_TRANSPORT_FAILED"))
            ended += 1
            owner?.destroy()
            owner = nil
        }
        #expect(ended == 0)
        #expect(native.reads == 0, "Construction must not poll before AppModel installs the owner")
        owner = session
        session.start()
        session.start()
        session.disconnect()
        session.destroy()
        session.retryDisplayAdjustment()
        session.updateViewport(pointSize: CGSize(width: 1000, height: 700), backingScaleFactor: 2, immediately: true)
        #expect(ended == 1)
        #expect(native.reads == 1)
        #expect(native.destroys == 1)
        #expect(native.stops == 0)
        #expect(native.viewports == 0)
    }

    @Test func destructionBeforeObservationNeverEmitsARecoveryEvent() {
        let native = TestTransport()
        let session = EmbeddedRDPSession(transport: native.make(), vmid: 160, desktopName: "test") { _, _ in
            Issue.record("Destroyed sessions cannot notify the owner")
        }
        session.destroy()
        session.start()
        session.disconnect()
        session.destroy()
        #expect(native.destroys == 1)
        #expect(native.reads == 0)
    }

    @Test func immediateFailureKeepsRecoveryOwnedAndFetchesFreshCredentials() async throws {
        let failed = TestTransport(); failed.state = 3
        let recovered = TestTransport(); recovered.state = 1
        var requests: [String] = []
        let model = await makeModel(transports: [failed, recovered], events: { requests.append($0) })
        defer { model.prepareForWindowClose() }
        model.beginConnection(to: desktop)
        try await eventually { model.connectionJourney?.stage == .reconnecting }
        #expect(model.connectingVMID == 160, "The completed old task cannot clear the recovery task")
        model.beginConnection(to: desktop) // Must not bypass the pending recovery.
        try await eventually { model.activeSession?.state == .connected }
        #expect(failed.destroys == 1)
        #expect(requests.filter { $0 == "POST /api/v1/native/desktops/160/connections" }.count == 2)
        #expect(requests.contains("DELETE /api/v1/native/connections/attempt-1"))
        model.sessionDidConnect(sessionID: try #require(model.activeSession?.id))
        #expect(model.connectionJourney == nil)
    }

    @Test func recoveryAfterColdStartDoesNotStartTheRunningDesktopAgain() async throws {
        let failed = TestTransport(); failed.state = 3
        let recovered = TestTransport(); recovered.state = 1
        var starts = 0
        let model = await makeModel(transports: [failed, recovered], response: { request in
            if request.url?.path.hasSuffix("/actions/start") == true {
                starts += 1
                return (202, #"{"id":"start-once","state":"running"}"#)
            }
            return nil
        })
        defer { model.prepareForWindowClose() }
        let stopped = Desktop(vmid: desktop.vmid, name: desktop.name, node: desktop.node,
                              status: "stopped", cpuCount: desktop.cpuCount, memoryTotal: desktop.memoryTotal)
        model.beginConnection(to: stopped)
        try await eventually { model.activeSession?.state == .connected }
        #expect(starts == 1, "Recovery must use the observed running state, not the original stopped card")
        #expect(model.connectionJourney?.desktop.status == "running")
    }

    @Test func manualRetryAfterColdStartPreparationFailureKeepsRunningState() async throws {
        let connected = TestTransport(); connected.state = 1
        var starts = 0
        var rejected = false
        let model = await makeModel(transports: [connected], response: { request in
            if request.url?.path.hasSuffix("/actions/start") == true {
                starts += 1
                return (202, #"{"id":"start-once","state":"running"}"#)
            }
            if request.httpMethod == "POST", request.url?.path.hasSuffix("/connections") == true, !rejected {
                rejected = true
                return (502, #"{"error":{"code":"pve_unavailable","message":"test failure"}}"#)
            }
            return nil
        })
        defer { model.prepareForWindowClose() }
        model.beginConnection(to: Desktop(vmid: desktop.vmid, name: desktop.name, node: desktop.node,
                                         status: "stopped", cpuCount: desktop.cpuCount, memoryTotal: desktop.memoryTotal))
        try await eventually { model.connectionJourney?.stage == .failed && model.connectingVMID == nil }
        #expect(model.connectionJourney?.desktop.status == "running")
        model.retryConnection()
        try await eventually { model.activeSession?.state == .connected }
        #expect(starts == 1)
    }

    @Test func cancelledRecoveryCannotClearAReplacementAttempt() async throws {
        let failed = TestTransport(); failed.state = 3
        let replacement = TestTransport(); replacement.state = 1
        let model = await makeModel(transports: [failed, replacement])
        defer { model.prepareForWindowClose() }
        model.beginConnection(to: desktop)
        try await eventually { model.connectionJourney?.stage == .reconnecting }
        model.cancelConnection()
        #expect(model.connectionJourney == nil)
        model.beginConnection(to: desktop)
        #expect(model.connectingVMID == 160)
        try await eventually { model.activeSession?.state == .connected }
        try await Task.sleep(for: .milliseconds(2200))
        #expect(replacement.destroys == 0)
        #expect(model.activeSession?.state == .connected)
    }

    @Test func lateGatewayCreationAfterCancelCannotReplaceTheNewSession() async throws {
        let first = TestTransport(); first.state = 1
        let second = TestTransport(); second.state = 1
        let gate = EndCheckGate()
        var requests: [String] = []
        let model = await makeModel(transports: [first, second], events: { requests.append($0) }, beforeSession: { index in
            if index == 1 { _ = await gate.wait() } // Deliberately ignores cancellation.
        })
        defer { gate.finish(status: 204, body: ""); model.prepareForWindowClose() }
        model.beginConnection(to: desktop)
        try await eventually { gate.requested }
        model.cancelConnection()
        model.beginConnection(to: desktop)
        try await eventually { model.activeSession?.state == .connected }
        let replacementID = model.activeSession?.id
        gate.finish(status: 204, body: "")
        try await eventually { first.destroys == 1 && requests.contains("DELETE /api/v1/native/connections/attempt-1") }
        #expect(first.reads == 0, "A stale async transport must be destroyed before observation")
        #expect(model.activeSession?.id == replacementID)
        #expect(model.activeSession?.state == .connected)
        #expect(second.destroys == 0)
        #expect(!requests.contains("DELETE /api/v1/native/connections/attempt-2"))
    }

    @Test func windowCloseAndStaleCallbacksNeverReconnectOrDestroyTheNextSession() async throws {
        let first = TestTransport(); first.state = 1
        let second = TestTransport(); second.state = 1
        var callbacks: [@MainActor (UUID, EmbeddedRDPSessionEnd) -> Void] = []
        let model = await makeModel(transports: [first, second], callbacks: { callbacks.append($0) })
        defer { model.prepareForWindowClose() }
        model.beginConnection(to: desktop)
        try await eventually { model.activeSession?.state == .connected }
        let oldID = try #require(model.activeSession?.id)
        model.prepareForWindowClose() // stop() immediately reports a network failure.
        #expect(first.stops == 1)
        #expect(first.destroys == 1)
        #expect(model.connectionJourney == nil)
        model.beginConnection(to: desktop)
        try await eventually { model.activeSession?.state == .connected }
        let currentID = model.activeSession?.id
        callbacks[0](oldID, .failed("ERRCONNECT_CONNECT_TRANSPORT_FAILED"))
        model.sessionDidConnect(sessionID: oldID)
        #expect(model.connectionJourney?.stage == .opening, "An old view's delayed reveal cannot complete this attempt")
        #expect(model.activeSession?.id == currentID)
        try await Task.sleep(for: .milliseconds(2200))
        #expect(model.activeSession?.id == currentID)
        #expect(second.destroys == 0)
    }

    @Test func logoutCannotRecoverFromAnImmediateStopFailure() async throws {
        let native = TestTransport(); native.state = 1
        var requests: [String] = []
        let model = await makeModel(transports: [native], events: { requests.append($0) })
        model.beginConnection(to: desktop)
        try await eventually { model.activeSession?.state == .connected }
        await model.logout()
        try await Task.sleep(for: .milliseconds(2200))
        #expect(model.state == .signedOut)
        #expect(model.activeSession == nil)
        #expect(model.connectionJourney == nil)
        #expect(native.destroys == 1)
        #expect(requests.filter { $0 == "POST /api/v1/native/desktops/160/connections" }.count == 1)
    }

    @Test(arguments: [200, 401, 503])
    func endedSessionRechecksAccessWithoutIssuingCredentials(status: Int) async throws {
        let native = TestTransport(); native.state = 1
        var ended = false
        var checked = false
        var requests: [String] = []
        var notify: (@MainActor (UUID, EmbeddedRDPSessionEnd) -> Void)?
        let model = await makeModel(transports: [native], events: { requests.append($0) }, response: { request in
            guard ended, request.url?.path == "/api/v1/native/desktops" else { return nil }
            checked = true
            return (status, status == 200 ? #"{"desktops":[]}"# : #"{"error":{"code":"authentication_required","message":"test only"}}"#)
        }, callbacks: { notify = $0 })
        defer { model.prepareForWindowClose() }
        model.beginConnection(to: desktop)
        try await eventually { model.activeSession?.state == .connected && model.connectingVMID == nil }
        let id = try #require(model.activeSession?.id)
        model.sessionDidConnect(sessionID: id)
        ended = true
        notify?(id, .failed("ERRINFO_LOGOFF_BY_USER"))
        try await eventually { checked }
        if status == 200 {
            try await eventually { model.connectionJourney?.stage == .unavailable }
            #expect(model.desktops.isEmpty)
            #expect(model.connectionJourney?.stage.canRetry == false)
        } else if status == 401 {
            try await eventually { model.state == .signedOut }
            #expect(model.desktops.isEmpty)
            #expect(model.connectionJourney == nil)
        } else {
            #expect(model.state == .signedIn)
            #expect(model.connectionJourney?.stage == .failed)
            #expect(model.connectionJourney?.detail == "远程连接已中断，可以重试或返回桌面库。")
            #expect(model.desktops.count == 1, "A failed read cannot infer revoked access")
        }
        #expect(model.activeSession == nil)
        #expect(requests.filter { $0 == "POST /api/v1/native/desktops/160/connections" }.count == 1)
    }

    @Test(arguments: ["retry", "library", "logout", "close"], [200, 401])
    func delayedEndCheckCannotOverrideNewUserIntent(action: String, status: Int) async throws {
        let first = TestTransport(); first.state = 1
        let second = TestTransport(); second.state = 1
        let gate = EndCheckGate()
        var ended = false
        var notify: (@MainActor (UUID, EmbeddedRDPSessionEnd) -> Void)?
        let model = await makeModel(transports: [first, second], response: { request in
            guard ended, request.url?.path == "/api/v1/native/desktops" else { return nil }
            return await gate.wait()
        }, callbacks: { notify = $0 })
        defer {
            gate.finish(status: 200, body: #"{"desktops":[]}"#)
            model.prepareForWindowClose()
        }
        model.beginConnection(to: desktop)
        try await eventually { model.activeSession?.state == .connected && model.connectingVMID == nil }
        let id = try #require(model.activeSession?.id)
        model.sessionDidConnect(sessionID: id)
        ended = true
        notify?(id, .failed("ERRINFO_LOGOFF_BY_USER"))
        try await eventually { gate.requested }
        switch action {
        case "retry":
            model.retryConnection()
            try await eventually { model.activeSession?.state == .connected }
            model.sessionDidConnect(sessionID: try #require(model.activeSession?.id))
        case "library": model.returnToDesktopLibrary()
        case "logout": await model.logout()
        default: model.prepareForWindowClose()
        }
        gate.finish(status: status, body: status == 200 ? #"{"desktops":[]}"# : #"{"error":{"code":"authentication_required","message":"test only"}}"#)
        try await Task.sleep(for: .milliseconds(100))
        #expect(model.connectionJourney == nil)
        #expect(model.state == (action == "logout" ? .signedOut : .signedIn))
        #expect(model.desktops.count == (action == "logout" ? 0 : 1))
        #expect((model.activeSession != nil) == (action == "retry"))
        #expect(second.destroys == 0)
    }

    @Test func cancelStillWorksBetweenTheFirstFrameAndItsDelayedReveal() async throws {
        let native = TestTransport(); native.state = 1
        let model = await makeModel(transports: [native])
        defer { model.prepareForWindowClose() }
        model.beginConnection(to: desktop)
        try await eventually { model.activeSession?.state == .connected }
        let id = try #require(model.activeSession?.id)
        #expect(model.connectionJourney?.stage == .opening)
        model.cancelConnection()
        model.sessionDidConnect(sessionID: id)
        #expect(native.stops == 1)
        #expect(native.destroys == 1)
        #expect(model.activeSession == nil)
        #expect(model.connectionJourney == nil)
    }

    @Test func invalidPortsAreRejectedBeforeCallingTheNativeRuntime() {
        let policy = NativeSessionPolicy(version: 1, revision: 4, clipboardRedirection: false,
                                         driveRedirection: false, managedBackground: true,
                                         hash: "sha256:cc5afecf0f8597a5b2ba8f2f5fd793ca32ba74566d0a4547496c5e1546e98654")
        for port in [Int.min, -1, 0, 65_536, Int.max] {
            let connection = DesktopConnection(id: "test", protocolName: "rdp", host: "192.0.2.1", port: port,
                                               username: "test", password: "test", issuedAt: .now, expiresAt: .now,
                                               desktopID: "160", sessionPolicy: policy)
            do {
                _ = try EmbeddedRDPTransport.native(connection: connection, quality: .managedClientDefault)
                Issue.record("Invalid port accepted")
            } catch EmbeddedRDPError.invalidConnection {
                // A checked error instead of UInt16 conversion trapping.
            } catch { Issue.record("Unexpected native runtime access: \(error)") }
        }
    }

    @Test func cancellationDuringTransportTeardownDoesNotRecover() async throws {
        let native = TestTransport(); native.state = 1
        let model = await makeModel(transports: [native])
        defer { model.prepareForWindowClose() }
        model.beginConnection(to: desktop)
        try await eventually { model.activeSession?.state == .connected }
        let session = try #require(model.activeSession)
        model.sessionDidConnect(sessionID: session.id)
        native.state = 0 // MRDPView clears is_connected before its thread exits.
        session.updateViewport(pointSize: CGSize(width: 1000, height: 700), backingScaleFactor: 2, immediately: true)
        try await eventually { session.state == .connecting }
        #expect(model.connectionJourney == nil)
        model.cancelConnection()
        #expect(native.stops == 1)
        #expect(model.activeSession == nil)
        #expect(model.connectionJourney == nil)
        #expect(model.connectingVMID == nil)
    }
}
