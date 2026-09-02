import Foundation

enum DesktopLibrary {
    static func filtered(_ desktops: [Desktop], query: String) -> [Desktop] {
        let tokens = normalized(query)
            .split(whereSeparator: { $0.isWhitespace })
            .map(String.init)
        guard !tokens.isEmpty else { return desktops }

        return desktops.filter { desktop in
            let searchable = normalized([
                desktop.name,
                String(desktop.vmid),
                desktop.node,
                desktop.status,
                statusTitle(desktop.status),
            ].joined(separator: " "))
            return tokens.allSatisfy(searchable.contains)
        }
    }

    static func statusTitle(_ status: String) -> String {
        switch status {
        case "running": return "运行中"
        case "stopped": return "已停止"
        case "paused": return "已暂停"
        default: return status
        }
    }

    private static func normalized(_ value: String) -> String {
        value.folding(
            options: [.caseInsensitive, .diacriticInsensitive, .widthInsensitive],
            locale: .current
        )
    }
}
