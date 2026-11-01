import AppKit
import SwiftUI

/// The usage view's colours (G5), values of the mockup's `viewer.css:29-55`: dynamic, light and dark.
/// The orange used for TEXT is darker in light mode (4.5:1 on the popover); fills keep `systemOrange`.
public enum BarPalette {
    private static func dynamic(light: NSColor, dark: NSColor) -> Color {
        Color(nsColor: NSColor(name: nil) { appearance in
            appearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua ? dark : light
        })
    }

    private static func rgb(_ hex: UInt32) -> NSColor {
        NSColor(srgbRed: CGFloat((hex >> 16) & 0xFF) / 255, green: CGFloat((hex >> 8) & 0xFF) / 255,
                blue: CGFloat(hex & 0xFF) / 255, alpha: 1)
    }

    /// Text: `#9C5A00` light / `systemOrange` dark.
    public static let orangeText = dynamic(light: rgb(0x9C5A00), dark: .systemOrange)
    /// Fills (dot, glyph, bar): `systemOrange`.
    public static let orangeFill = Color(nsColor: .systemOrange)
    /// `#C42A20` light / `#FF6B5E` dark.
    public static let red = dynamic(light: rgb(0xC42A20), dark: rgb(0xFF6B5E))
    /// `#1E7F3A` light / `#30D158` dark: the sober « it holds » dot.
    public static let green = dynamic(light: rgb(0x1E7F3A), dark: rgb(0x30D158))
    /// Secondary text (G5, `viewer.css:21`: fg2 = black at 64 %): SwiftUI's `.secondary` measures 3.95:1 on white.
    public static let textSecondaryLightHex: UInt32 = 0x5C5C5C
    /// Dark: 5.9:1 on the popover (about #262629).
    public static let textSecondaryDarkHex: UInt32 = 0xA1A1A6
    public static let textSecondary = dynamic(light: rgb(textSecondaryLightHex), dark: rgb(textSecondaryDarkHex))
    /// A bar's track.
    public static let track = dynamic(light: NSColor.black.withAlphaComponent(0.09), dark: NSColor.white.withAlphaComponent(0.12))
    /// A bar's fill (neutral).
    public static let fill = dynamic(light: NSColor.black.withAlphaComponent(0.52), dark: NSColor.white.withAlphaComponent(0.62))
}
