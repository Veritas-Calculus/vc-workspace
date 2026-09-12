import Foundation
import Testing
@testable import VCWorkspace

@Test func recoveryRetriesOnlyExplicitNetworkFailures() {
    for name in ["ERRCONNECT_CONNECT_TRANSPORT_FAILED", "ERRCONNECT_CONNECT_FAILED",
                 "ERRCONNECT_DNS_ERROR", "ERRCONNECT_DNS_NAME_NOT_FOUND"] {
        var policy = SessionRecoveryPolicy()
        #expect(policy.nextDelay(for: .failed(name), now: 100) == .seconds(2))
    }
    for result in [EmbeddedRDPSessionEnd.closed,
                   .failed("ERRCONNECT_LOGON_FAILURE"), .failed("ERRCONNECT_TLS_CONNECT_FAILED"),
                   .failed("ERRCONNECT_CONNECT_CANCELLED"), .failed("ERRCONNECT_ACCESS_DENIED"),
                   .failed("ERRCONNECT_CONNECT_UNDEFINED"),
                   .failed("ERRINFO_RPC_INITIATED_LOGOFF"), .failed("password=secret"),
                   .failed("VCW_GATEWAY_CERTIFICATE_REJECTED"), .failed("VCW_GATEWAY_REDIRECT_REJECTED"),
                   .failed("ERRCONNECT_CONNECT_TRANSPORT_FAILED extra")] {
        var policy = SessionRecoveryPolicy()
        #expect(policy.nextDelay(for: result, now: 100) == nil)
    }
}

@Test func gatewayFailureCopyIsSanitizedAndSpecific() {
    #expect(userFacingNativeSessionError("VCW_GATEWAY_CERTIFICATE_REJECTED") == "桌面安全验证失败，请联系管理员。")
    #expect(userFacingNativeSessionError("VCW_GATEWAY_REDIRECT_REJECTED") == "桌面连接目标发生变化，请重新连接。")
    #expect(userFacingNativeSessionError("secret protocol details") == "远程连接已中断，可以重试或返回桌面库。")
    #expect(userFacingDesktopConnectionError(GatewayTunnelError.rejected) == "桌面网关未授权此连接，请重新连接")
}

@Test func recoveryBudgetDoesNotResetWhenAFlappingSessionConnects() {
    var policy = SessionRecoveryPolicy()
    let failure = EmbeddedRDPSessionEnd.failed("ERRCONNECT_CONNECT_TRANSPORT_FAILED")
    #expect(policy.nextDelay(for: failure, now: 100) == .seconds(2))
    #expect(policy.nextDelay(for: failure, now: 106) == .seconds(5))
    #expect(policy.nextDelay(for: failure, now: 115) == nil)
    #expect(policy.nextDelay(for: failure, now: 219) == nil)
    #expect(policy.nextDelay(for: failure, now: 226) == .seconds(2))
    #expect(policy.nextDelay(for: failure, now: .nan) == nil)
}

@Test func recoveryHasProgressAndAnExitButNoDuplicateRetry() {
    #expect(DesktopConnectionStage.reconnecting.isPending)
    #expect(DesktopConnectionStage.reconnecting.canCancel)
    #expect(!DesktopConnectionStage.reconnecting.canRetry)
    #expect(DesktopConnectionStage.reconnecting.title == "正在恢复连接")
}

@Test func revokedOrMissingDesktopDoesNotOfferEndlessRetry() {
    for status in [403, 404] {
        let error = APIClientError.rejected(code: "managed_vm_not_found", message: "secret", status: status)
        #expect(desktopConnectionFailureStage(error) == .unavailable)
        #expect(!desktopConnectionFailureStage(error).canRetry)
        #expect(!desktopConnectionFailureStage(error).isPending)
        #expect(userFacingDesktopConnectionError(error) == DesktopConnectionStage.unavailable.detail)
    }
    #expect(desktopConnectionFailureStage(APIClientError.rejected(code: "internal_error", message: "", status: 503)) == .failed)
}
