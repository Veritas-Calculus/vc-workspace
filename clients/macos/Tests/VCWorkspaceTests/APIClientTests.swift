import Foundation
import Testing
@testable import VCWorkspace

@Test
func invalidCredentialsUseTheClientLanguage() {
    let error = APIClientError.rejected(code: "invalid_credentials", message: "Username or password is incorrect", status: 401)
    #expect(error.localizedDescription == "用户名或密码不正确")
}

private final class APIClientStubURLProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var handler: ((URLRequest) throws -> (HTTPURLResponse, Data))?

    override class func canInit(with _: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        do {
            guard let handler = Self.handler else { throw URLError(.badServerResponse) }
            let (response, data) = try handler(request)
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            if !data.isEmpty { client?.urlProtocol(self, didLoad: data) }
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}
}

private final class APIClientPolicyStubURLProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var handler: ((URLRequest) throws -> (HTTPURLResponse, Data))?

    override class func canInit(with _: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        do {
            guard let handler = Self.handler else { throw URLError(.badServerResponse) }
            let (response, data) = try handler(request)
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            if !data.isEmpty { client?.urlProtocol(self, didLoad: data) }
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}
}

@Test
func serverIdentifierIsStableForKeychainBinding() {
    #expect(normalizedServerIdentifier(" HTTPS://VDI.Example.com/ ") == "https://vdi.example.com")
    #expect(normalizedServerIdentifier("http://127.0.0.1:8080/") == "http://127.0.0.1:8080")
    #expect(normalizedServerIdentifier("https://user:secret@vdi.example.com") == nil)
    #expect(normalizedServerIdentifier("https://vdi.example.com?token=secret") == nil)
}

@Test
func remoteControlPlaneRequiresHTTPS() {
    do {
        _ = try APIClient(server: "http://vdi.example.com")
        Issue.record("expected an insecure remote server to be rejected")
    } catch APIClientError.insecureServer {
        // Expected.
    } catch {
        Issue.record("unexpected error: \(error)")
    }
}

@Test
func localDevelopmentControlPlaneCanUseHTTP() throws {
    let client = try APIClient(server: "http://127.0.0.1:8080/")
    #expect(client.baseURL.absoluteString == "http://127.0.0.1:8080")
}

@Test
func releasingDesktopConnectionUsesAuthenticatedDelete() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [APIClientStubURLProtocol.self]
    APIClientStubURLProtocol.handler = { request in
        #expect(request.url?.path == "/api/v1/native/connections/connection-123")
        #expect(request.httpMethod == "DELETE")
        #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer native-token")
        return (HTTPURLResponse(url: request.url!, statusCode: 204, httpVersion: nil, headerFields: nil)!, Data())
    }
    defer { APIClientStubURLProtocol.handler = nil }
    let client = try APIClient(server: "http://127.0.0.1:8080", session: URLSession(configuration: configuration))
    try await client.releaseConnection(id: "connection-123", token: "native-token")
}

@Test
func connectionDescriptorCarriesEnforcedSessionPolicy() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [APIClientPolicyStubURLProtocol.self]
    APIClientPolicyStubURLProtocol.handler = { request in
        #expect(request.url?.path == "/api/v1/native/desktops/158/connections")
        #expect(request.value(forHTTPHeaderField: "X-VC-Workspace-Desktop-Scale") == "200")
        #expect(request.value(forHTTPHeaderField: "X-VC-Workspace-Transport") == NativeGatewayConnection.subprotocolName)
        let body = """
        {
          "id":"connection-policy","protocol":"rdp","host":"10.31.0.158","port":3389,
          "username":"vcwexample","password":"temporary","issued_at":"2026-09-05T00:00:00Z",
          "expires_at":"2026-09-05T08:00:00Z","desktop_id":"158",
          "session_policy":{"version":1,"revision":4,"clipboard_redirection":false,
          "drive_redirection":false,"managed_background":true,
          "hash":"sha256:cc5afecf0f8597a5b2ba8f2f5fd793ca32ba74566d0a4547496c5e1546e98654"}
        }
        """
        return (HTTPURLResponse(url: request.url!, statusCode: 201, httpVersion: nil, headerFields: ["Content-Type": "application/json"])!, Data(body.utf8))
    }
    defer { APIClientPolicyStubURLProtocol.handler = nil }
    let client = try APIClient(server: "http://127.0.0.1:8080", session: URLSession(configuration: configuration))
    let connection = try await client.createConnection(vmid: 158, token: "native-token", desktopScaleFactor: 200)
    #expect(connection.sessionPolicy.revision == 4)
    #expect(connection.sessionPolicy.clipboardRedirection == false)
    #expect(connection.sessionPolicy.driveRedirection == false)
    #expect(connection.sessionPolicy.managedBackground == true)
    #expect(connection.sessionPolicy.hasValidSnapshotHash)

    let changed = NativeSessionPolicy(
        version: connection.sessionPolicy.version,
        revision: connection.sessionPolicy.revision,
        clipboardRedirection: true,
        driveRedirection: connection.sessionPolicy.driveRedirection,
        managedBackground: connection.sessionPolicy.managedBackground,
        hash: connection.sessionPolicy.hash
    )
    #expect(!changed.hasValidSnapshotHash)
}
