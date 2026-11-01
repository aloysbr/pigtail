import Foundation

/// The text the status item shows next to the head while Travel Mode is on (D23):
/// `Travel Mode 3 h`, `Travel Mode 45 min`, or `Travel Mode` without a timer; nil otherwise,
/// and the head stands alone. The popover and the tooltip keep their D13 texts (`LidFormat`).
public enum LidStatusTitle {
    /// The visible name, also the title without a timer.
    static let name = "Travel Mode"
    /// U+00A0 between the number and its unit: the bar never breaks "3 h" apart.
    static let noBreakSpace = "\u{00A0}"

    /// nil unless the last known state is on — never read, unknown or off shows no text.
    public static func title(for d: LidDisplay, now: Date) -> String? {
        guard case .on(let deadline) = d.state else { return nil }
        guard let deadline else { return name }
        return "\(name) \(remaining(deadline.timeIntervalSince(now)))"
    }

    /// Under 60 minutes (rounded up): "N min"; otherwise hours rounded up: "N h".
    /// A deadline already passed while the read still says on shows "1 min", never 0 or less.
    static func remaining(_ seconds: TimeInterval) -> String {
        let minutes = max(1, Int((seconds / 60).rounded(.up)))
        if minutes < 60 { return "\(minutes)\(noBreakSpace)min" }
        let hours = Int((seconds / 3600).rounded(.up))
        return "\(hours)\(noBreakSpace)h"
    }

    /// The next instant `title` may change: the remaining time is rounded up to the minute, so the
    /// text moves when the remaining time crosses a whole minute — at most 60 s away. nil without a
    /// deadline or once it has passed (the next /lid read settles it). The status item re-renders
    /// at that instant from its local timer, without a request.
    public static func nextRefresh(for d: LidDisplay, now: Date) -> Date? {
        guard case .on(let deadline?) = d.state else { return nil }
        let left = deadline.timeIntervalSince(now)
        guard left > 0 else { return nil }
        let past = left.truncatingRemainder(dividingBy: 60)
        return now.addingTimeInterval(past > 0 ? past : 60)
    }
}
