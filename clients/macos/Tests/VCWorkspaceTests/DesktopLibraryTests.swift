import Testing
@testable import VCWorkspace

private let desktops = [
    Desktop(vmid: 9101, name: "Debian 13 工作站", node: "infra-node3", status: "running", cpuCount: 4, memoryTotal: 8_589_934_592),
    Desktop(vmid: 9113, name: "Windows 11 Design", node: "infra-node4", status: "stopped", cpuCount: 8, memoryTotal: 17_179_869_184),
]

@Test
func desktopSearchMatchesNameNodeVMIDAndLocalizedStatus() {
    #expect(DesktopLibrary.filtered(desktops, query: "debian").map(\.vmid) == [9101])
    #expect(DesktopLibrary.filtered(desktops, query: "INFRA-NODE4").map(\.vmid) == [9113])
    #expect(DesktopLibrary.filtered(desktops, query: "9113").map(\.vmid) == [9113])
    #expect(DesktopLibrary.filtered(desktops, query: "运行中").map(\.vmid) == [9101])
}

@Test
func desktopSearchRequiresEveryTokenAndPreservesOrder() {
    #expect(DesktopLibrary.filtered(desktops, query: "  windows   node4 ").map(\.vmid) == [9113])
    #expect(DesktopLibrary.filtered(desktops, query: "debian stopped").isEmpty)
    #expect(DesktopLibrary.filtered(desktops, query: "   ").map(\.vmid) == [9101, 9113])
}
