import AppKit
import SwiftUI

/// Native presentation tokens. System colours resolve appearance/contrast;
/// the remote framebuffer and the unmodified brand asset are fixed-colour exceptions.
enum WorkspaceStyle {
    static let canvas = Color(nsColor: NSColor(name: nil) { appearance in
        resolvedSurface(appearance, elevated: false)
    })
    static let surface = Color(nsColor: NSColor(name: nil) { appearance in
        resolvedSurface(appearance, elevated: true)
    })
    static let ink = Color(nsColor: .labelColor)
    static let line = Color(nsColor: .separatorColor)
    static let controlRadius: CGFloat = 9
    static let cardRadius: CGFloat = 16
    static let pageInset: CGFloat = 28
    static let gap: CGFloat = 16
    static let controlHeight: CGFloat = 40
    static let title = Font.system(size: 28, weight: .semibold)
    static let heading = Font.system(size: 17, weight: .semibold)

    private static func resolvedSurface(_ appearance: NSAppearance, elevated: Bool) -> NSColor {
        var resolved = NSColor.windowBackgroundColor
        appearance.performAsCurrentDrawingAppearance {
            let dark = appearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
            let base = NSColor.windowBackgroundColor.usingColorSpace(.deviceRGB) ?? .windowBackgroundColor
            // Tahoe gives both native surfaces the same colour. A small neutral
            // tonal step retains hierarchy without blur, shadows or translucency.
            let fraction = dark ? (elevated ? 0.045 : 0) : (elevated ? 0 : 0.035)
            resolved = base.blended(withFraction: fraction, of: dark ? .white : .black) ?? base
        }
        return resolved
    }

    static func motion(reduced: Bool) -> Animation {
        reduced ? .easeOut(duration: 0.12) : .spring(response: 0.32, dampingFraction: 1)
    }

    static func transition(reduced: Bool) -> AnyTransition {
        reduced ? .opacity : .opacity.combined(with: .offset(y: 8))
    }

    static func animatesActivity(pending: Bool, reduced: Bool, active: Bool) -> Bool {
        pending && !reduced && active
    }
}

/// Command family: label is the only slot; native Button owns activation,
/// cancellation, focus, disabled semantics and shortcuts. No gesture recognizer.
struct WorkspaceActionStyle: ButtonStyle {
    enum Emphasis { case primary, secondary }
    var emphasis: Emphasis = .primary

    func makeBody(configuration: Configuration) -> some View {
        WorkspaceActionBody(configuration: configuration, emphasis: emphasis)
    }
}

private struct WorkspaceActionBody: View {
    let configuration: ButtonStyleConfiguration
    let emphasis: WorkspaceActionStyle.Emphasis
    @Environment(\.isEnabled) private var enabled
    @Environment(\.isFocused) private var focused
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var hovering = false

    var body: some View {
        configuration.label
            .font(.system(size: 13, weight: .semibold))
            .padding(.horizontal, WorkspaceStyle.gap)
            .frame(minHeight: WorkspaceStyle.controlHeight)
            .foregroundStyle(emphasis == .primary ? WorkspaceStyle.surface : WorkspaceStyle.ink)
            .background {
                RoundedRectangle(cornerRadius: WorkspaceStyle.controlRadius, style: .continuous)
                    .fill(emphasis == .primary ? WorkspaceStyle.ink : WorkspaceStyle.surface)
                    .opacity(enabled && (hovering || configuration.isPressed) ? 0.84 : 1)
            }
            .overlay {
                RoundedRectangle(cornerRadius: WorkspaceStyle.controlRadius, style: .continuous)
                    .strokeBorder(focused ? Color.accentColor : WorkspaceStyle.line.opacity(emphasis == .secondary ? 0.7 : 0), lineWidth: focused ? 2 : 1)
            }
            .contentShape(RoundedRectangle(cornerRadius: WorkspaceStyle.controlRadius))
            .opacity(enabled ? 1 : 0.4)
            .scaleEffect(configuration.isPressed && enabled && !reduceMotion ? 0.98 : 1)
            .animation(WorkspaceStyle.motion(reduced: reduceMotion), value: configuration.isPressed)
            .animation(.easeOut(duration: 0.12), value: hovering)
            .onHover { hovering = $0 && enabled }
    }
}

/// This is an activity indicator, never a percentage. The clock is paused when
/// motion is reduced, the scene is inactive, or work has reached a terminal state.
struct WorkspaceActivityStroke: View {
    var pending = true
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        let animate = WorkspaceStyle.animatesActivity(pending: pending, reduced: reduceMotion, active: scenePhase == .active)
        TimelineView(.animation(minimumInterval: 1.0 / 30, paused: !animate)) { context in
            let phase = animate ? context.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 1.8) / 1.8 : 0.18
            GeometryReader { geometry in
                Capsule().fill(WorkspaceStyle.line.opacity(0.35))
                Capsule().fill(WorkspaceStyle.ink.opacity(0.7))
                    .frame(width: geometry.size.width * 0.3)
                    .offset(x: (geometry.size.width * 1.3) * phase - geometry.size.width * 0.3)
            }
            .clipShape(Capsule())
        }
        .frame(width: 112, height: 4)
        .accessibilityHidden(true)
    }
}
