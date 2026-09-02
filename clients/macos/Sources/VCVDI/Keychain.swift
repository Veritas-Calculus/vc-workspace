import Foundation
import Security

enum SessionKeychainError: LocalizedError {
    case writeFailed(OSStatus)

    var errorDescription: String? {
        switch self {
        case .writeFailed(let status):
            return "无法将登录状态保存到钥匙串（\(status)）"
        }
    }
}

enum SessionKeychain {
    private static let service = "ac.plz.vc-vdi"
    private static let account = "native-session"

    private struct StoredSession: Codable {
        let server: String
        let token: String
    }

    static func load(for server: String) -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var item: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
              let data = item as? Data else { return nil }
        guard let stored = try? JSONDecoder().decode(StoredSession.self, from: data),
              stored.server == normalizedServerIdentifier(server) else { return nil }
        return stored.token
    }

    static func save(_ token: String, for server: String) throws {
        guard let server = normalizedServerIdentifier(server) else { return }
        delete()
        let value = try JSONEncoder().encode(StoredSession(server: server, token: token))
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecValueData as String: value,
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
        ]
        let status = SecItemAdd(query as CFDictionary, nil)
        guard status == errSecSuccess else { throw SessionKeychainError.writeFailed(status) }
    }

    static func delete() {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        SecItemDelete(query as CFDictionary)
    }
}
