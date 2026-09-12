import Foundation

/// One recovery owner, above the transport: every attempt revalidates access
/// and obtains fresh credentials from the control plane. Never retry TLS,
/// authentication, server logoff, unknown errors or intentional disconnects.
struct SessionRecoveryPolicy {
    private var attempts: [TimeInterval] = []

    mutating func nextDelay(for result: EmbeddedRDPSessionEnd, now: TimeInterval) -> Duration? {
        guard case .failed(let code) = result,
              ["ERRCONNECT_CONNECT_TRANSPORT_FAILED", "ERRCONNECT_CONNECT_FAILED",
               "ERRCONNECT_DNS_ERROR", "ERRCONNECT_DNS_NAME_NOT_FOUND"].contains(code),
              now.isFinite else { return nil }
        attempts.removeAll { now - $0 >= 120 }
        guard attempts.count < 2 else { return nil }
        attempts.append(now)
        return .seconds(attempts.count == 1 ? 2 : 5)
    }
}
