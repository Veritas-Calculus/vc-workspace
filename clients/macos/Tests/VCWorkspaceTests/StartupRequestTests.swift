import Foundation
import Testing
@testable import VCWorkspace

private final class SystemProbeURLProtocol: URLProtocol, @unchecked Sendable {
    override class func canInit(with _: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        #expect(request.url?.path == "/api/v1/system")
        #expect(request.timeoutInterval == 8)
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(#"{"name":"VC Workspace","oidc_configured":false,"oidc_name":""}"#.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@Test
func startupProbeUsesAShortRequestTimeout() async throws {
    let configuration = APIClient.interactiveSessionConfiguration()
    #expect(configuration.waitsForConnectivity == false)
    #expect(configuration.timeoutIntervalForResource == 90) // Longer RDP preparation remains supported.
    configuration.protocolClasses = [SystemProbeURLProtocol.self]
    let session = URLSession(configuration: configuration)
    defer { session.invalidateAndCancel() }
    let client = try APIClient(server: "http://127.0.0.1:8080", session: session)
    #expect(try await client.system().name == "VC Workspace")
}
