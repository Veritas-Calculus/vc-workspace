import Testing
@testable import VCVDI

@Test
func desktopConnectionRetriesEveryColdStartTransition() {
    #expect(isRetryableDesktopConnectionPreparationCode("desktop_not_running"))
    #expect(isRetryableDesktopConnectionPreparationCode("desktop_not_ready"))
    #expect(isRetryableDesktopConnectionPreparationCode("desktop_network_unavailable"))
    #expect(!isRetryableDesktopConnectionPreparationCode("desktop_policy_not_applied"))
    #expect(!isRetryableDesktopConnectionPreparationCode("permission_denied"))
}
