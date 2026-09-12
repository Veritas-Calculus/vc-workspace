import Testing
@testable import VCWorkspace

@Test
func clientLoadingStagesExplainTheCurrentStep() {
    #expect(ClientLoadingStage.starting.title == "正在启动 VC Workspace")
    #expect(ClientLoadingStage.restoringSession.title == "正在恢复会话")
    #expect(ClientLoadingStage.signingIn.title == "正在登录")
    #expect(ClientLoadingStage.loadingDesktops.title == "正在加载桌面")

    let details = [
        ClientLoadingStage.starting.detail,
        ClientLoadingStage.restoringSession.detail,
        ClientLoadingStage.signingIn.detail,
        ClientLoadingStage.loadingDesktops.detail,
    ]
    #expect(Set(details).count == details.count)
}

@Test
func desktopConnectionRetriesEveryColdStartTransition() {
    #expect(isRetryableDesktopConnectionPreparationCode("desktop_not_running"))
    #expect(isRetryableDesktopConnectionPreparationCode("desktop_not_ready"))
    #expect(isRetryableDesktopConnectionPreparationCode("desktop_network_unavailable"))
    #expect(!isRetryableDesktopConnectionPreparationCode("desktop_policy_not_applied"))
    #expect(!isRetryableDesktopConnectionPreparationCode("permission_denied"))
}

@Test
func desktopConnectionJourneyKeepsProgressAndRecoveryStatesDistinct() {
    let desktop = Desktop(
        vmid: 158,
        name: "Debian 13 工作站",
        node: "infra-node6",
        status: "running",
        cpuCount: 4,
        memoryTotal: 3_221_225_472
    )

    let progress = DesktopConnectionJourney(desktop: desktop, stage: .preparing)
    #expect(progress.stage.isPending)
    #expect(progress.stage.canCancel)
    #expect(!progress.stage.canRetry)
    #expect(progress.stage.title == "正在准备桌面")
    #expect(!progress.detail.contains("秒"))

    let disconnected = DesktopConnectionJourney(desktop: desktop, stage: .disconnected)
    #expect(!disconnected.stage.isPending)
    #expect(!disconnected.stage.canCancel)
    #expect(disconnected.stage.canRetry)
    #expect(disconnected.stage.title == "连接已断开")
    // Transport closure neither proves logout nor guarantees desktop retention:
    // an administrator or the Guest expiry worker may also have ended it.
    #expect(disconnected.detail == "可以重新连接，或返回桌面库。")
    #expect(DesktopConnectionStage.disconnecting.detail == "正在断开与桌面的连接。")
}

@Test
func desktopConnectionFailureCopyDoesNotExposeGuestImplementation() {
    let message = userFacingDesktopConnectionError(DesktopConnectionError.servicesTimedOut)
    #expect(message == "桌面服务暂时无法连接，请稍后重试。")
    #expect(!message.localizedCaseInsensitiveContains("guest"))
    #expect(!message.localizedCaseInsensitiveContains("xrdp"))
    #expect(userFacingDesktopConnectionError(APIClientError.rejected(code: "permission_denied", message: "raw", status: 403)) == "你没有连接此桌面的权限。")
    #expect(userFacingDesktopConnectionError(APIClientError.rejected(code: "unknown", message: "raw protocol failure", status: 409)) == "桌面暂时无法连接，请稍后重试。")
}
