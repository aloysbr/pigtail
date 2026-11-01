import Foundation
@testable import MenuBarCore

/// The mockup's scenarios (t3 `viewer.js`, Task 5 table) as wire values, shared by the model, view
/// and capture tests. now = 2026-10-20T21:00Z, UTC.
enum UsageFixtures {
    static let utc = TimeZone(identifier: "UTC")!
    static let nb = "\u{00A0}"
    static let now = ISO8601DateFormatter().date(from: "2026-10-20T21:00:00Z")!

    /// « 25/10 17:59 » → that instant in 2026 (UTC).
    static func at(_ s: String) -> Date {
        let p = s.split(separator: " ")
        let d = p[0].split(separator: "/").map { Int($0)! }
        let t = p[1].split(separator: ":").map { Int($0)! }
        var c = DateComponents(); c.year = 2026; c.month = d[1]; c.day = d[0]; c.hour = t[0]; c.minute = t[1]
        var cal = Calendar(identifier: .gregorian); cal.timeZone = utc
        return cal.date(from: c)!
    }

    static func win(_ used: Int, _ reset: String?, full: Bool = false) -> UsageWindow {
        UsageWindow(percent: used, resetsAt: reset.map(at), full: full)
    }

    static func acct(_ uuid: String, _ label: String, d7: Int, d7r: String, h5: Int, h5r: String?, left: Double? = nil,
                     fast: Bool = false, ageMin: Int = 4, failed: Bool = false, unusable: String = "",
                     o5: Bool = false, o7: Bool = false, scoped: [UsageScoped] = []) -> UsageAccount {
        UsageAccount(accountUUID: uuid, label: label, inVerdict: unusable.isEmpty, unusable: unusable,
                     fetchedAt: now.addingTimeInterval(-Double(ageMin) * 60),
                     sevenDay: win(d7, d7r, full: o7), fiveHour: win(h5, h5r, full: o5), scoped: scoped,
                     windowLeft: left, fast: fast, refreshFailed: failed)
    }

    static let m = acct("u-m", "m", d7: 20, d7r: "25/10 17:59", h5: 16, h5r: "20/10 23:19", left: 0.707)
    static let i = acct("u-i", "i", d7: 64, d7r: "25/10 08:59", h5: 69, h5r: "20/10 23:29", left: 0.643, fast: true, ageMin: 5)
    static let j = acct("u-j", "j", d7: 2, d7r: "27/10 02:59", h5: 0, h5r: nil, left: 0.9, ageMin: 6)
    static let t = acct("u-t", "t", d7: 3, d7r: "24/10 20:59", h5: 0, h5r: nil, ageMin: 8, unusable: "cap")

    static func overview(_ accounts: [UsageAccount], verdict: UsageVerdict?, picks: [UsageScopePick] = [],
                         unreadable: Int = 0) -> UsageOverview {
        UsageOverview(generatedAt: now, accounts: accounts, verdict: verdict, nextSession: picks, unreadableCount: unreadable)
    }
    static func ready(_ o: UsageOverview, failed: Bool = false) -> UsageDisplay {
        UsageDisplay(state: .ready, overview: o, readFailed: failed)
    }

    static let normalVerdict = UsageVerdict(state: "ok", provisional: false, oldestReadingAt: now.addingTimeInterval(-6 * 60),
                                            nextRenewal: UsageLabelAt(label: "i", at: at("25/10 08:59")))
    static let tightVerdict = UsageVerdict(state: "tight", oldestReadingAt: now.addingTimeInterval(-6 * 60),
                                           nextRenewal: UsageLabelAt(label: "i", at: at("25/10 08:59")))
    /// For overviews that hold only m and j (the renewal names a present account).
    static let mjVerdict = UsageVerdict(state: "ok", provisional: false, oldestReadingAt: now.addingTimeInterval(-6 * 60),
                                        nextRenewal: UsageLabelAt(label: "m", at: at("25/10 17:59")))
    static let tooFastVerdict = UsageVerdict(state: "later", oldestReadingAt: now.addingTimeInterval(-6 * 60),
                                             blockedFrom: at("22/10 14:40"), frees: UsageLabelAt(label: "i", at: at("23/10 08:59")))
    static let weekStartVerdict = UsageVerdict(state: "later", provisional: true, oldestReadingAt: now.addingTimeInterval(-6 * 60),
                                               blockedFrom: at("25/10 03:20"), frees: UsageLabelAt(label: "i", at: at("26/10 20:00")))
    static let blockedNowVerdict = UsageVerdict(state: "now", oldestReadingAt: now.addingTimeInterval(-6 * 60),
                                                frees: UsageLabelAt(label: "j", at: at("20/10 21:52")))

    static let persoI = UsageScopePick(scope: "perso", display: "perso", master: true, accountUUID: "u-i")
    static let workT = UsageScopePick(scope: "globex", display: "gx", master: false, accountUUID: "u-t")
    static let workJ = UsageScopePick(scope: "globex", display: "gx", master: false, accountUUID: "u-j")
    static let persoJ = UsageScopePick(scope: "perso", display: "perso", master: true, accountUUID: "u-j")

    /// Scenario 1, « Conso normale ».
    static var normal: UsageOverview { overview([m, i, j, t], verdict: normalVerdict, picks: [persoI, workT]) }
    /// Scenario 2, « Rythme trop fort ».
    static var tooFast: UsageOverview {
        overview([acct("u-m", "m", d7: 55, d7r: "25/10 17:59", h5: 30, h5r: "20/10 23:19", left: 0.707, fast: true),
                  acct("u-i", "i", d7: 78, d7r: "23/10 08:59", h5: 50, h5r: "20/10 23:29", left: 0.35, fast: true, ageMin: 5),
                  acct("u-j", "j", d7: 40, d7r: "27/10 02:59", h5: 12, h5r: "21/10 00:40", left: 0.9, fast: true, ageMin: 6), t],
                 verdict: tooFastVerdict, picks: [persoI, workT])
    }
    /// Scenario 3, « Début de semaine ».
    static var weekStart: UsageOverview {
        overview([acct("u-m", "m", d7: 9, d7r: "27/10 09:00", h5: 30, h5r: "20/10 23:40", left: 0.97),
                  acct("u-i", "i", d7: 30, d7r: "26/10 20:00", h5: 45, h5r: "20/10 22:10", left: 0.85, ageMin: 5),
                  acct("u-j", "j", d7: 3, d7r: "27/10 14:00", h5: 10, h5r: "21/10 01:30", left: 0.98, ageMin: 6), t],
                 verdict: weekStartVerdict, picks: [persoI, workT])
    }
    /// Scenario 4, « Bloqué maintenant ».
    static var blockedNow: UsageOverview {
        overview([acct("u-m", "m", d7: 20, d7r: "25/10 17:59", h5: 92, h5r: "20/10 23:19", left: 0.707, o5: true),
                  acct("u-i", "i", d7: 64, d7r: "25/10 08:59", h5: 95, h5r: "20/10 23:29", left: 0.643, fast: true, ageMin: 5, o5: true),
                  acct("u-j", "j", d7: 2, d7r: "27/10 02:59", h5: 91, h5r: "20/10 21:52", left: 0.9, ageMin: 6, o5: true), t],
                 verdict: blockedNowVerdict, picks: [persoJ, workT])
    }
    /// Scenario 5, « Relevés périmés » (i also failed to refresh).
    static var stale: UsageOverview {
        overview([acct("u-m", "m", d7: 20, d7r: "25/10 17:59", h5: 16, h5r: "20/10 23:19", left: 0.707, ageMin: 72),
                  acct("u-i", "i", d7: 64, d7r: "25/10 08:59", h5: 69, h5r: "20/10 23:29", left: 0.643, fast: true, ageMin: 125, failed: true),
                  acct("u-j", "j", d7: 2, d7r: "27/10 02:59", h5: 0, h5r: nil, left: 0.9, ageMin: 71),
                  acct("u-t", "t", d7: 3, d7r: "24/10 20:59", h5: 0, h5r: nil, ageMin: 74, unusable: "cap")],
                 verdict: UsageVerdict(state: "ok", oldestReadingAt: now.addingTimeInterval(-125 * 60),
                                       nextRenewal: UsageLabelAt(label: "i", at: at("25/10 08:59"))),
                 picks: [persoI, workT])
    }
    /// « Tu tiens, de justesse » (not among the six scenarios).
    static var tight: UsageOverview { overview([m, i, j, t], verdict: tightVerdict, picks: [persoI, workT]) }
}
