import Testing
@testable import VCWorkspace

@Test
func connectionQualityHasStableRuntimeValues() {
    #expect(ConnectionQuality.adaptive.runtimeValue == 0)
    #expect(ConnectionQuality.lowBandwidth.runtimeValue == 1)
    #expect(ConnectionQuality.managedClientDefault == .adaptive)
}
