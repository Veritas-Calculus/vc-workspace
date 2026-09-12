import Foundation

enum VCWorkspaceAppLink: Equatable {
    case connect(vmid: Int)

    static func parse(_ url: URL) -> VCWorkspaceAppLink? {
        let acceptedSchemes = ["vc-workspace", "vc-vdi"]
        guard url.absoluteString.utf8.count <= 256,
              url.scheme.map({ acceptedSchemes.contains($0.lowercased()) }) == true,
              url.host?.lowercased() == "connect",
              url.user == nil,
              url.password == nil,
              url.path.isEmpty,
              url.fragment == nil,
              let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              let queryItems = components.queryItems,
              queryItems.count == 1,
              queryItems[0].name == "vmid",
              let value = queryItems[0].value,
              !value.isEmpty,
              value.utf8.allSatisfy({ (48...57).contains($0) }),
              let vmid = Int(value),
              vmid > 0 else { return nil }
        return .connect(vmid: vmid)
    }
}
