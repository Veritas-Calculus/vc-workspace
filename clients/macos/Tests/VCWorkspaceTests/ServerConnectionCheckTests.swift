import Foundation
import Testing
@testable import VCWorkspace

private final class ConnectionCheckProtocol: URLProtocol, @unchecked Sendable {
    override class func canInit(with _: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        // The old endpoint deliberately never answers; changing it must cancel the request.
        guard request.url?.host != "pending.example.test" else { return }
        if request.url?.host == "failed.example.test" {
            client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
            return
        }
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(#"{"name":"checked","oidc_configured":true,"oidc_name":"Test SSO"}"#.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@Suite @MainActor
struct ServerConnectionCheckTests {
    func fixture() -> (AppModel, URLSession) {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [ConnectionCheckProtocol.self]
        let session = URLSession(configuration: config)
        var dependencies = AppModelDependencies()
        dependencies.makeClient = { try APIClient(server: $0, session: session) }
        dependencies.loadToken = { _ in nil }
        dependencies.saveToken = { _, _ in }
        dependencies.deleteToken = {}
        dependencies.saveServer = { _ in }
        let model = AppModel(dependencies: dependencies)
        model.server = "https://success.example.test"
        model.state = .signedOut
        return (model, session)
    }

    @Test func successFailureRetryAndAddressEdit() async {
        let (model, session) = fixture()
        defer { session.invalidateAndCancel() }
        #expect(model.serverCheck == .idle)
        model.errorMessage = "Previous startup error"
        await model.checkServerConnection()
        #expect(model.serverCheck == .succeeded)
        #expect(model.system?.name == "checked")
        #expect(model.errorMessage.isEmpty)
        model.server = "https://failed.example.test"
        #expect(model.serverCheck == .idle)
        #expect(model.system == nil)
        await model.checkServerConnection()
        guard case .failed(let message) = model.serverCheck else {
            Issue.record("A failed probe must replace the pending state")
            return
        }
        #expect(!message.isEmpty)
        #expect(model.state == .signedOut)
        model.server = "https://success.example.test"
        await model.checkServerConnection()
        #expect(model.serverCheck == .succeeded)
    }

    @Test func explicitFailureDoesNotSignOutOrOverwriteSessionError() async {
        let (model, session) = fixture()
        defer { session.invalidateAndCancel() }
        model.server = "https://failed.example.test"
        model.state = .signedIn
        model.errorMessage = "Existing session notice"
        await model.checkServerConnection()
        #expect(model.state == .signedIn)
        #expect(model.errorMessage == "Existing session notice")
        guard case .failed = model.serverCheck else {
            Issue.record("Connection failure must be visible without signing out")
            return
        }
    }

    @Test func addressChangeCancelsOldProbeWithoutOverwritingNewResult() async throws {
        let (model, session) = fixture()
        defer { session.invalidateAndCancel() }
        model.server = "https://pending.example.test"
        let old = Task { await model.checkServerConnection() }
        for _ in 0..<1000 {
            if model.serverCheck == .checking { break }
            await Task.yield()
        }
        #expect(model.serverCheck == .checking)
        // Repeated action while busy must neither cancel nor create a second probe.
        await model.checkServerConnection()
        #expect(model.serverCheck == .checking)
        model.server = "https://success.example.test"
        #expect(model.serverCheck == .idle)
        await model.checkServerConnection()
        await old.value
        #expect(model.serverCheck == .succeeded)
        #expect(model.system?.name == "checked")
        #expect(model.errorMessage.isEmpty)
    }

    @Test func cancellationAndBlankAddressReturnToIdle() async {
        let (model, session) = fixture()
        defer { session.invalidateAndCancel() }
        model.server = "https://pending.example.test"
        let pending = Task { await model.checkServerConnection() }
        for _ in 0..<1000 {
            if model.serverCheck == .checking { break }
            await Task.yield()
        }
        #expect(model.serverCheck == .checking)
        pending.cancel()
        await pending.value
        #expect(model.serverCheck == .idle)
        model.server = "  "
        await model.checkServerConnection()
        #expect(model.serverCheck == .idle)
    }

    @Test func startupProbeDoesNotDisplayAnUnrequestedSuccessLabel() async {
        let (model, session) = fixture()
        defer { session.invalidateAndCancel() }
        await model.loadSystem()
        #expect(model.serverCheck == .idle)
        #expect(model.system?.name == "checked")
    }

    @Test func errorsAreActionableAndDoNotExposeRawServerText() {
        #expect(serverConnectionErrorMessage(URLError(.timedOut)) == "检查连接超时，请重试。")
        #expect(serverConnectionErrorMessage(URLError(.cannotConnectToHost)) == "无法连接到服务器，请检查地址后重试。")
        #expect(serverConnectionErrorMessage(URLError(.serverCertificateUntrusted)).contains("证书"))
        #expect(!serverConnectionErrorMessage(APIClientError.rejected(code: "internal", message: "secret upstream detail", status: 500)).contains("secret"))
    }
}
