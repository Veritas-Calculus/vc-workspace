import Foundation

enum ClientLoadingStage: Equatable {
    case starting
    case restoringSession
    case signingIn
    case loadingDesktops

    var title: String {
        switch self {
        case .starting:
            "正在启动 VC Workspace"
        case .restoringSession:
            "正在恢复会话"
        case .signingIn:
            "正在登录"
        case .loadingDesktops:
            "正在加载桌面"
        }
    }

    var detail: String {
        switch self {
        case .starting:
            "正在连接到服务器。"
        case .restoringSession:
            "正在验证已保存的登录状态。"
        case .signingIn:
            "正在验证你的账户。"
        case .loadingDesktops:
            "正在读取你可以使用的桌面。"
        }
    }
}

enum DesktopConnectionStage: Equatable {
    case starting
    case preparing
    case opening
    case reconnecting
    case cancelling
    case disconnecting
    case disconnected
    case failed
    case unavailable

    var isPending: Bool {
        switch self {
        case .starting, .preparing, .opening, .reconnecting, .cancelling, .disconnecting:
            true
        case .disconnected, .failed, .unavailable:
            false
        }
    }

    var canCancel: Bool {
        switch self {
        case .starting, .preparing, .opening, .reconnecting:
            true
        case .cancelling, .disconnecting, .disconnected, .failed, .unavailable:
            false
        }
    }

    var canRetry: Bool {
        switch self {
        case .disconnected, .failed:
            true
        case .starting, .preparing, .opening, .reconnecting, .cancelling, .disconnecting, .unavailable:
            false
        }
    }

    var title: String {
        switch self {
        case .starting:
            "正在启动桌面"
        case .preparing:
            "正在准备桌面"
        case .opening:
            "正在连接"
        case .reconnecting:
            "正在恢复连接"
        case .cancelling:
            "正在取消"
        case .disconnecting:
            "正在断开连接"
        case .disconnected:
            "连接已断开"
        case .failed:
            "无法连接"
        case .unavailable:
            "桌面不可用"
        }
    }

    var detail: String {
        switch self {
        case .starting:
            "桌面启动后将自动继续连接。"
        case .preparing:
            "正在等待桌面服务就绪。"
        case .opening:
            "正在建立安全的远程会话。"
        case .reconnecting:
            "连接暂时中断，正在尝试回到你的桌面。"
        case .cancelling:
            "正在停止本次连接。"
        case .disconnecting:
            "正在断开与桌面的连接。"
        case .disconnected:
            "可以重新连接，或返回桌面库。"
        case .failed:
            "请检查网络后重试。"
        case .unavailable:
            "此桌面已不可用或不再分配给你，请返回桌面库。"
        }
    }
}

struct DesktopConnectionJourney: Equatable {
    let desktop: Desktop
    var stage: DesktopConnectionStage
    var message: String?

    var detail: String {
        let trimmed = message?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? stage.detail : trimmed
    }
}

func userFacingDesktopConnectionError(_ error: Error) -> String {
    switch error {
    case DesktopConnectionError.startTimedOut:
        "桌面启动时间较长，请稍后重试。"
    case DesktopConnectionError.servicesTimedOut:
        "桌面服务暂时无法连接，请稍后重试。"
    case APIClientError.insecureServer:
        "控制面连接不安全，请检查服务器设置。"
    case APIClientError.invalidServer:
        "控制面地址无效，请检查服务器设置。"
    case let gateway as GatewayTunnelError:
        gateway.localizedDescription
    case APIClientError.rejected(let code, _, _):
        switch code {
        case "permission_denied":
            "你没有连接此桌面的权限。"
        case "authentication_required":
            "登录已过期，请重新登录。"
        case "desktop_policy_not_applied":
            "桌面策略暂时无法应用，请联系管理员。"
        case "managed_vm_not_found":
            DesktopConnectionStage.unavailable.detail
        default:
            "桌面暂时无法连接，请稍后重试。"
        }
    default:
        "暂时无法连接到桌面，请检查网络后重试。"
    }
}

func userFacingNativeSessionError(_ code: String) -> String {
    switch code {
    case "VCW_GATEWAY_CERTIFICATE_REJECTED":
        "桌面安全验证失败，请联系管理员。"
    case "VCW_GATEWAY_REDIRECT_REJECTED":
        "桌面连接目标发生变化，请重新连接。"
    default:
        "远程连接已中断，可以重试或返回桌面库。"
    }
}

func desktopConnectionFailureStage(_ error: Error) -> DesktopConnectionStage {
    if case APIClientError.rejected(_, _, let status) = error, status == 403 || status == 404 {
        return .unavailable
    }
    return .failed
}

enum DesktopConnectionError: LocalizedError {
    case startTimedOut
    case servicesTimedOut

    var errorDescription: String? {
        userFacingDesktopConnectionError(self)
    }
}
