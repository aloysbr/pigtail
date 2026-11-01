import Foundation

/// French relative durations and day words, a port of the mockup's `viewer.js:31-53`.
/// Non-breaking spaces (U+00A0) sit between a number and its unit and inside « 2 h 19 »;
/// the time zone is injected so a test never depends on the machine's.
public enum FrenchDuration {
    public static let nb = "\u{00A0}"
    public static let apostrophe = "\u{2019}"

    private static func pad(_ n: Int) -> String { n < 10 ? "0\(n)" : "\(n)" }

    /// JS `Math.round`: halves go up.
    private static func jsRound(_ x: Double) -> Int { Int((x + 0.5).rounded(.down)) }

    /// « 45 min », « 2 h 19 », « 3 j 12 h ».
    public static func dur(minutes min: Int) -> String {
        if min < 60 { return "\(min)\(nb)min" }
        if min < 1440 {
            let h = min / 60, mm = min % 60
            return "\(h)\(nb)h" + (mm != 0 ? nb + pad(mm) : "")
        }
        let th = jsRound(Double(min) / 60)
        let d = th / 24, hh = th % 24
        return "\(d)\(nb)j" + (hh != 0 ? "\(nb)\(hh)\(nb)h" : "")
    }

    /// A projection is approximate: whole hours past 3 h.
    public static func durRound(minutes min: Int) -> String {
        if min < 180 { return dur(minutes: min) }
        let h = jsRound(Double(min) / 60)
        return h < 24 ? "\(h)\(nb)h" : dur(minutes: h * 60)
    }

    private static let days = ["dim.", "lun.", "mar.", "mer.", "jeu.", "ven.", "sam."]

    private static func calendar(_ tz: TimeZone) -> Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = tz
        return c
    }

    /// « aujourd’hui », « demain », else the short weekday (« dim. » … « sam. »).
    public static func dayWord(_ t: Date, now: Date, timeZone: TimeZone) -> String {
        let cal = calendar(timeZone)
        let dd = cal.dateComponents([.day], from: cal.startOfDay(for: now), to: cal.startOfDay(for: t)).day ?? 0
        if dd == 0 { return "aujourd\(apostrophe)hui" }
        if dd == 1 { return "demain" }
        return days[cal.component(.weekday, from: t) - 1]
    }

    /// « jeu. vers 15 h » — rounded to the hour, the day taken after rounding.
    public static func around(_ t: Date, now: Date, timeZone: TimeZone) -> String {
        let r = Date(timeIntervalSince1970: (t.timeIntervalSince1970 / 3600).rounded(.toNearestOrAwayFromZero) * 3600)
        let hour = calendar(timeZone).component(.hour, from: r)
        return "\(dayWord(r, now: now, timeZone: timeZone)) vers \(hour)\(nb)h"
    }

    /// Whole minutes between two instants, rounded, never negative.
    public static func minutes(from: Date, to: Date) -> Int {
        max(0, jsRound(to.timeIntervalSince(from) / 60))
    }

    /// `at` as an instant not before `now`: a passed 7-day reset counts as reset + 7 j
    /// (the loop of `Simulate`'s step (a)), so a frozen route never yields a negative duration.
    public static func advancedWeek(_ at: Date, now: Date) -> Date {
        var r = at
        let week: TimeInterval = 7 * 24 * 3600
        if r <= now { r = r.addingTimeInterval(week * (((now.timeIntervalSince(r)) / week).rounded(.down) + 1)) }
        return r
    }
}
