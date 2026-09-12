import Foundation
import Testing
@testable import VCWorkspace

@Test
func appLinkParsesDesktopConnectionIntent() throws {
    let url = try #require(URL(string: "vc-workspace://connect?vmid=158"))
    #expect(VCWorkspaceAppLink.parse(url) == .connect(vmid: 158))
}

@Test
func appLinkAcceptsLegacySchemeDuringMigration() throws {
    let url = try #require(URL(string: "vc-vdi://connect?vmid=158"))
    #expect(VCWorkspaceAppLink.parse(url) == .connect(vmid: 158))
}

@Test
func appLinkRejectsSecretsAndAmbiguousTargets() throws {
    let invalidLinks = [
        "vc-workspace://connect?vmid=0",
        "vc-workspace://connect?vmid=-1",
        "vc-workspace://connect?vmid=158&token=secret",
        "vc-workspace://connect?vmid=158&vmid=159",
        "vc-workspace://user:secret@connect?vmid=158",
        "vc-workspace://connect/path?vmid=158",
        "vc-workspace://connect?vmid=158#token",
        "https://workspace.example.com/connect?vmid=158",
        "vc-workspace://auth?code=oauth-callback",
    ]
    for value in invalidLinks {
        let url = try #require(URL(string: value))
        #expect(VCWorkspaceAppLink.parse(url) == nil)
    }
}
