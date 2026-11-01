import SwiftUI
import AppKit

// MARK: - VisualEffectBackground

/// Bridges an `NSVisualEffectView` so the popover can use the native menu
/// material — brighter and more opaque than SwiftUI's `.regularMaterial`, which
/// renders quite dark over dark content. This matches the look of standard
/// macOS menu-bar menus (e.g. the Granola menu).
struct VisualEffectBackground: NSViewRepresentable {
    var material: NSVisualEffectView.Material = .menu
    var blendingMode: NSVisualEffectView.BlendingMode = .behindWindow

    func makeNSView(context: Context) -> NSVisualEffectView {
        let view = NSVisualEffectView()
        view.material = material
        view.blendingMode = blendingMode
        view.state = .active
        return view
    }

    func updateNSView(_ nsView: NSVisualEffectView, context: Context) {
        nsView.material = material
        nsView.blendingMode = blendingMode
        nsView.state = .active
    }
}
