import Foundation

/// What the status item says besides its glyph (D9, G11): no badge for now, so only
/// Travel Mode speaks, through the accessibility label and the tooltip. The text next to the
/// head (`LidStatusTitle`, R9) stays its visual signal.
public enum StatusItemSignal {
    /// « pigtail », plus « , <Travel Mode line> » while Travel Mode is on.
    public static func accessibilityLabel(lid: LidDisplay, timeZone: TimeZone = .current) -> String {
        guard let line = LidFormat.activeLine(lid, timeZone: timeZone) else { return "pigtail" }
        return "pigtail, \(line)"
    }

    /// The Travel Mode line, or nil (off, unknown or never read).
    public static func tooltip(lid: LidDisplay, timeZone: TimeZone = .current) -> String? {
        LidFormat.activeLine(lid, timeZone: timeZone)
    }
}
