import Foundation
import Testing
@testable import VCVDI

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
