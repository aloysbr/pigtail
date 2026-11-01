import SwiftUI
import AppKit
import MenuBarCore

// MARK: - Adaptive colors

extension Color {
    /// Hairline border for the popover card, tuned per theme to mirror system
    /// menus: a light (white) edge in dark mode where it must stand out against a
    /// dark fill, and a very subtle light-gray edge in light mode (a faint clear
    /// border, never a heavy dark line).
    static var popoverCardBorder: Color {
        Color(nsColor: NSColor(name: nil) { appearance in
            let isDark = appearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua
            return isDark
                ? NSColor.white.withAlphaComponent(0.22)
                : NSColor.black.withAlphaComponent(0.08)
        })
    }
}

// MARK: - PopoverMetrics

/// Shared layout constants for the popover. `shadowMargin` is the transparent
/// inset around the card that gives the SwiftUI `.shadow()` room to render
/// without being clipped by the panel's edges. MenuBarController subtracts it when
/// positioning so the card top — not the window top — anchors below the menu bar.
enum PopoverMetrics {
    static let shadowMargin: CGFloat = 20
}

// MARK: - PopoverActions

/// Actions surfaced by the views; MenuBarController supplies the implementations.
struct PopoverActions {
    var stopAll: () -> Void
    var quit: () -> Void
    var openWindow: () -> Void = {}
}

// MARK: - PopoverView

/// The card around the usage view (D2): the content is `PopoverContent` (Core, tested); this file
/// only draws the material, border and shadow and reports the card's size to the panel.
struct PopoverView: View {
    let state: PopoverState
    let loaded: Bool
    let usage: UsageDisplay
    let lidDisplay: LidDisplay
    let lidNotificationsDenied: Bool
    let detailExpanded: Binding<Bool>
    @ObservedObject var loginItem: LoginItemController
    let actions: PopoverActions
    let onLidAction: (LidAction) -> Void
    var onSizeChange: (PanelMeasure) -> Void = { _ in }

    var body: some View {
        PopoverContent(
            state: state,
            loaded: loaded,
            usage: usage,
            lid: lidDisplay,
            lidNotificationsDenied: lidNotificationsDenied,
            detailExpanded: detailExpanded,
            loginEnabled: loginItem.binding,
            loginNeedsApproval: loginItem.requiresApproval,
            stopAll: actions.stopAll,
            quit: actions.quit,
            onLidAction: onLidAction,
            openWindow: actions.openWindow
        )
        // The visual "card": rounded corners + material + shadow, the sole background since the
        // NSPanel is transparent. An adaptive overlay brightens the `.menu` material over dark content.
        .background {
            ZStack {
                VisualEffectBackground(material: .menu)
                Color(nsColor: .textBackgroundColor).opacity(0.55)
            }
        }
        .clipShape(RoundedRectangle(cornerRadius: 12))
        .overlay(
            RoundedRectangle(cornerRadius: 12)
                .strokeBorder(Color.popoverCardBorder, lineWidth: 1)
        )
        .shadow(color: .black.opacity(0.18), radius: 16, x: 0, y: 4)
        // Margin >= shadow radius (+ y offset) so the blur is not clipped by the panel's edges.
        .padding(PopoverMetrics.shadowMargin)
        // Measured after .padding: the window-content size, mirrored by the host window.
        // The window-content size and the folds' heights, measured together (PanelSizePolicy).
        .reportsPanelMeasure(onSizeChange)
    }
}
