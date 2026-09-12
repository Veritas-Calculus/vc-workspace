import Foundation

enum ConnectionQuality: String {
    case adaptive
    case lowBandwidth

    // The interactive client always starts in the adaptive profile. Alternate
    // profiles remain available to managed policy and diagnostics, not as a
    // user-facing connection choice.
    static let managedClientDefault: ConnectionQuality = .adaptive

    var runtimeValue: Int32 {
        switch self {
        case .adaptive: return 0
        case .lowBandwidth: return 1
        }
    }
}
