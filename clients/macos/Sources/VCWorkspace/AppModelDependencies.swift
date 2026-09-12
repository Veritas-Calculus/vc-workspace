import Foundation

@MainActor
struct AppModelDependencies {
    var makeClient: (String) throws -> APIClient = { try APIClient(server: $0) }
    var makeSession: (DesktopConnection, Int, String, ConnectionQuality,
                      @escaping @MainActor (UUID, EmbeddedRDPSessionEnd) -> Void) async throws -> EmbeddedRDPSession = {
        try await EmbeddedRDPSession(connection: $0, vmid: $1, desktopName: $2, quality: $3, onTermination: $4)
    }
    // Tests use an in-memory credential store, never the user's real Keychain.
    var loadToken: @Sendable (String) -> String? = { SessionKeychain.load(for: $0) }
    var saveToken: (String, String) throws -> Void = { try SessionKeychain.save($0, for: $1) }
    var deleteToken: () -> Void = { SessionKeychain.delete() }
    var saveServer: (String) -> Void = { UserDefaults.standard.set($0, forKey: "server") }
}
