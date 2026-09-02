import Foundation
import Testing
@testable import VCVDI

@Test
func appLinkParsesDesktopConnectionIntent() throws {
    let url = try #require(URL(string: "vc-vdi://connect?vmid=158"))
    #expect(VCWorkspaceAppLink.parse(url) == .connect(vmid: 158))
}

@Test
func appLinkRejectsSecretsAndAmbiguousTargets() throws {
    let invalidLinks = [
        "vc-vdi://connect?vmid=0",
        "vc-vdi://connect?vmid=-1",
        "vc-vdi://connect?vmid=158&token=secret",
        "vc-vdi://connect?vmid=158&vmid=159",
        "vc-vdi://user:secret@connect?vmid=158",
        "vc-vdi://connect/path?vmid=158",
        "vc-vdi://connect?vmid=158#token",
        "https://workspace.example.com/connect?vmid=158",
        "vc-vdi://auth?code=oauth-callback",
    ]
    for value in invalidLinks {
        let url = try #require(URL(string: value))
        #expect(VCWorkspaceAppLink.parse(url) == nil)
    }
}
