import Foundation

struct SystemState: Decodable {
    let name: String
    let oidcConfigured: Bool
    let oidcName: String

    enum CodingKeys: String, CodingKey {
        case name
        case oidcConfigured = "oidc_configured"
        case oidcName = "oidc_name"
    }
}

struct NativeSession: Decodable {
    let accessToken: String
    let expiresAt: Date
    let user: User

    enum CodingKeys: String, CodingKey {
        case accessToken = "access_token"
        case expiresAt = "expires_at"
        case user
    }
}

struct User: Decodable {
    let id: String
    let username: String
    let displayName: String

    enum CodingKeys: String, CodingKey {
        case id, username
        case displayName = "display_name"
    }
}

struct DesktopList: Decodable { let desktops: [Desktop] }

struct Desktop: Decodable, Identifiable {
    let vmid: Int
    let name: String
    let node: String
    let status: String
    let cpuCount: Int
    let memoryTotal: Int64

    var id: Int { vmid }

    enum CodingKeys: String, CodingKey {
        case vmid, name, node, status
        case cpuCount = "cpu_count"
        case memoryTotal = "memory_total"
    }
}

struct Job: Decodable {
    let id: String
    let state: String
}

struct DesktopConnection: Decodable {
    let id: String
    let protocolName: String
    let host: String
    let port: Int
    let username: String
    let password: String
    let issuedAt: Date
    let desktopID: String

    enum CodingKeys: String, CodingKey {
        case id, host, port, username, password
        case protocolName = "protocol"
        case issuedAt = "issued_at"
        case desktopID = "desktop_id"
    }
}

struct APIProblem: Decodable {
    struct Detail: Decodable {
        let code: String
        let message: String
    }
    let error: Detail
}

enum APIClientError: LocalizedError {
    case invalidServer
    case insecureServer
    case rejected(code: String, message: String, status: Int)
    case invalidResponse

    var errorDescription: String? {
        switch self {
        case .invalidServer: return "控制面地址无效"
        case .insecureServer: return "非本机控制面必须使用 HTTPS"
        case .rejected(_, let message, _): return message
        case .invalidResponse: return "控制面返回了无法识别的数据"
        }
    }
}

struct APIClient {
    let baseURL: URL
    private let decoder: JSONDecoder
    private let session: URLSession

    init(server: String, session: URLSession? = nil) throws {
        guard let identifier = normalizedServerIdentifier(server), let url = URL(string: identifier),
              let scheme = url.scheme, let host = url.host else {
            throw APIClientError.invalidServer
        }
        if scheme == "http", !["127.0.0.1", "localhost", "::1"].contains(host) {
            throw APIClientError.insecureServer
        }
        baseURL = url
        decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        if let session {
            self.session = session
        } else {
            let configuration = URLSessionConfiguration.ephemeral
            configuration.waitsForConnectivity = true
            configuration.timeoutIntervalForRequest = 30
            configuration.timeoutIntervalForResource = 90
            configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
            self.session = URLSession(configuration: configuration)
        }
    }

    func system() async throws -> SystemState { try await request(path: "/api/v1/system") }

    func localLogin(username: String, password: String) async throws -> NativeSession {
        try await request(path: "/api/v1/auth/native/login", method: "POST", body: ["username": username, "password": password])
    }

    func exchange(code: String) async throws -> NativeSession {
        try await request(path: "/api/v1/auth/native/exchange", method: "POST", body: ["code": code])
    }

    func desktops(token: String) async throws -> [Desktop] {
        let result: DesktopList = try await request(path: "/api/v1/native/desktops", token: token)
        return result.desktops
    }

    func changePower(vmid: Int, action: String, token: String, idempotencyKey: String) async throws -> Job {
        try await request(
            path: "/api/v1/native/desktops/\(vmid)/actions/\(action)",
            method: "POST",
            token: token,
            headers: ["Idempotency-Key": idempotencyKey]
        )
    }

    func createConnection(vmid: Int, token: String) async throws -> DesktopConnection {
        try await request(path: "/api/v1/native/desktops/\(vmid)/connections", method: "POST", token: token, timeoutInterval: 80)
    }

    func logout(token: String) async throws {
        let _: EmptyResponse = try await request(path: "/api/v1/auth/native/logout", method: "POST", token: token)
    }

    func oidcStartURL() -> URL {
        baseURL.appending(path: "/api/v1/auth/oidc/start").appending(queryItems: [.init(name: "client", value: "macos")])
    }

    private func request<Response: Decodable>(path: String, method: String = "GET", body: [String: String]? = nil, token: String? = nil, headers: [String: String] = [:], timeoutInterval: TimeInterval = 30) async throws -> Response {
        var request = URLRequest(url: baseURL.appending(path: path))
        request.httpMethod = method
        request.timeoutInterval = timeoutInterval
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue("no-store", forHTTPHeaderField: "Cache-Control")
        if let body {
            request.httpBody = try JSONEncoder().encode(body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        if let token { request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        for (name, value) in headers { request.setValue(value, forHTTPHeaderField: name) }
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw APIClientError.invalidResponse }
        guard (200..<300).contains(http.statusCode) else {
            let problem = try? decoder.decode(APIProblem.self, from: data)
            throw APIClientError.rejected(
                code: problem?.error.code ?? "http_\(http.statusCode)",
                message: problem?.error.message ?? "请求失败（\(http.statusCode)）",
                status: http.statusCode
            )
        }
        if Response.self == EmptyResponse.self, data.isEmpty { return EmptyResponse() as! Response }
        return try decoder.decode(Response.self, from: data)
    }
}

private struct EmptyResponse: Decodable {}

func normalizedServerIdentifier(_ rawValue: String) -> String? {
    let value = rawValue.trimmingCharacters(in: .whitespacesAndNewlines)
    guard var components = URLComponents(string: value),
          let scheme = components.scheme?.lowercased(), ["http", "https"].contains(scheme),
          let host = components.host?.lowercased(), !host.isEmpty,
          components.user == nil, components.password == nil,
          components.query == nil, components.fragment == nil else { return nil }
    components.scheme = scheme
    components.host = host
    while components.path.hasSuffix("/") {
        components.path.removeLast()
    }
    return components.string
}
