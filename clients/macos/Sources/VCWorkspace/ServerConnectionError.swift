import Foundation

func serverConnectionErrorMessage(_ error: Error) -> String {
    if let error = error as? URLError {
        switch error.code {
        case .timedOut:
            return "检查连接超时，请重试。"
        case .notConnectedToInternet, .networkConnectionLost:
            return "网络连接不可用，请检查网络后重试。"
        case .cannotFindHost, .dnsLookupFailed:
            return "找不到服务器，请检查地址。"
        case .serverCertificateUntrusted, .serverCertificateHasBadDate,
             .serverCertificateHasUnknownRoot, .serverCertificateNotYetValid,
             .secureConnectionFailed:
            return "无法验证服务器的安全连接，请联系管理员检查证书。"
        default:
            return "无法连接到服务器，请检查地址后重试。"
        }
    }
    if let error = error as? APIClientError {
        switch error {
        case .invalidServer, .insecureServer:
            return error.localizedDescription
        case .invalidResponse, .rejected:
            return "服务器未返回有效的工作空间信息，请检查地址或联系管理员。"
        }
    }
    return "服务器未返回有效的工作空间信息，请检查地址或联系管理员。"
}
