import Foundation

/// One macOS notification Pigtail posts about the Travel Mode (D3): an
/// automatic cut, or an automatic cut that failed (Q1). Plain values, tested
/// here; LidNotifier (the Pigtail app) posts it.
public struct LidCutNotice: Equatable, Sendable {
    /// The notification identifier: the cut's `last_off.at` in ISO 8601 (one
    /// cut never stacks twice in Notification Centre), or
    /// `LidCutNotices.failureID` for a failed cut (a new failure replaces the old).
    public let id: String
    public let title: String
    public let body: String

    public init(id: String, title: String, body: String) {
        self.id = id; self.title = title; self.body = body
    }
}

/// Which Travel Mode change to notify, from two successive GET /lid answers.
public enum LidCutNotices {
    public static let failureID = "lid-auto-off-failed"
    static let tail = " — le Mac peut de nouveau dormir capot fermé."

    /// - `previous == nil` (the bar's first read) → nil: the baseline, never a notice.
    /// - `current.lastOff` present and ≠ `previous.lastOff` → the cut, if its
    ///   reason is automatic and known (deadline, battery N%, thermal serious,
    ///   thermal critical); never manual, external, or an unknown reason. No age
    ///   limit: a cut with the lid closed is read at wake-up, hours later — its
    ///   time is in the title.
    /// - else `previous.lastError` empty and `current.lastError` not → the
    ///   failure, once per appearance (its text drifts with the battery).
    public static func notice(previous: LidResponse?, current: LidResponse,
                              timeZone: TimeZone = .current) -> LidCutNotice? {
        guard let previous else { return nil }
        if let off = current.lastOff, off != previous.lastOff {
            return cut(off, batteryFloor: current.batteryFloor, timeZone: timeZone)
        }
        if isEmpty(previous.lastError), let e = current.lastError, !e.isEmpty {
            return LidCutNotice(id: failureID, title: "Travel Mode : coupure échouée", body: e)
        }
        return nil
    }

    private static func isEmpty(_ s: String?) -> Bool { s?.isEmpty ?? true }

    private static func cut(_ off: LidOff, batteryFloor: Int, timeZone: TimeZone) -> LidCutNotice? {
        let why: String
        switch off.reason {
        case "deadline":
            why = "Fin du minuteur"
        case "thermal serious":
            why = "Mac chaud capot fermé depuis 10 min (état thermique serious)"
        case "thermal critical":
            why = "Mac en surchauffe capot fermé (état thermique critical)"
        default:
            guard off.reason.hasPrefix("battery "),
                  let n = Int(off.reason.dropFirst("battery ".count).dropLast(off.reason.hasSuffix("%") ? 1 : 0))
            else { return nil }   // manual, external, or a reason this bar does not know
            why = "Batterie à \(n) % (seuil \(batteryFloor) %)"
        }
        let iso = ISO8601DateFormatter()
        return LidCutNotice(id: iso.string(from: off.at),
                            title: "Travel Mode coupé à \(LidFormat.hhmm(off.at, timeZone: timeZone))",
                            body: why + tail)
    }
}
