import Foundation

// Pure value models for the usage view: every text of the popover's usage zone
// comes from here (copy and rounding in Swift, the simulation in Go). The copy
// is a port of the mockup's `viewer.js`.

/// A reading older than this is stale (`claudeusage.StaleAfter`).
public let usageStaleAfterMinutes = 60

private let nb = FrenchDuration.nb
private let apostrophe = FrenchDuration.apostrophe

private func age(of date: Date?, now: Date) -> Int? {
    guard let date else { return nil }
    let m = FrenchDuration.minutes(from: date, to: now)
    return m > usageStaleAfterMinutes ? m : nil
}

// MARK: - Verdict

public struct VerdictModel: Equatable, Sendable {
    public enum Tone: Equatable, Sendable { case ok, warn, alarm, muted }
    public struct Note: Equatable, Sendable {
        public let text: String
        public let orange: Bool
    }

    public let tone: Tone
    public let phrase: String
    public let sub: String?
    public let notes: [Note]
    /// The phrase is the daemon-unavailable message (G12), not a verdict.
    public let isDaemonMessage: Bool

    public init(display: UsageDisplay, now: Date, timeZone: TimeZone = .current) {
        switch display.state {
        case .loading:
            self.init(.muted, "Lecture des relevés…")
        case .unreachable:
            self.init(.muted, "Le démon pigtail est indisponible.", "La conso et la rotation reviennent avec lui.", daemon: true)
        case .ready:
            guard let v = display.overview?.verdict else {
                self.init(.muted, "Lecture des relevés…")
                return
            }
            self = Self.fold(v, now: now, timeZone: timeZone)
        }
    }

    private init(_ tone: Tone, _ phrase: String, _ sub: String? = nil, notes: [Note] = [], daemon: Bool = false) {
        self.tone = tone; self.phrase = phrase; self.sub = sub; self.notes = notes; self.isDaemonMessage = daemon
    }

    private static func fold(_ v: UsageVerdict, now: Date, timeZone: TimeZone) -> VerdictModel {
        var tone: Tone
        var phrase: String
        var sub: String?
        switch v.state {
        case "ok", "tight":
            tone = v.state == "ok" ? .ok : .warn
            phrase = v.state == "ok" ? "Tu tiens jusqu\(apostrophe)aux renouvellements" : "Tu tiens, de justesse"
            if let r = v.nextRenewal {   // G14: no renewal known, no sub-line
                let at = FrenchDuration.advancedWeek(r.at, now: now)
                sub = "Prochain renouvellement\(nb): \(r.label) dans " + FrenchDuration.dur(minutes: FrenchDuration.minutes(from: now, to: at))
            }
        case "now":
            tone = .alarm
            phrase = "Bloqué maintenant"
            if let f = v.frees {
                sub = "Libre dans \(FrenchDuration.dur(minutes: FrenchDuration.minutes(from: now, to: f.at)))\(nb)(compte\(nb)\(f.label))"
            } else { sub = "Heure de déblocage inconnue" }   // G13
        case "later":
            tone = .warn
            if let b = v.blockedFrom {
                let when = "bloqué " + FrenchDuration.around(b, now: now, timeZone: timeZone)
                phrase = "Au rythme actuel, " + when.replacingOccurrences(of: " ", with: nb)
            } else {
                phrase = "Au rythme actuel, bloqué plus tard"
            }
            if let f = v.frees, let b = v.blockedFrom {
                sub = FrenchDuration.durRound(minutes: FrenchDuration.minutes(from: b, to: f.at)) + " avant le renouvellement de \(f.label)"
            } else { sub = "Heure de déblocage inconnue" }   // G13
        case "none":
            tone = .muted
            phrase = "Aucun compte lisible"   // G2: no sub-line, muted
        default:
            tone = .muted
            phrase = "Verdict indisponible"
        }
        var notes: [Note] = []
        if v.provisional { notes.append(Note(text: "estimation provisoire, peu d\(apostrophe)historique", orange: false)) }
        if let a = age(of: v.oldestReadingAt, now: now) {
            notes.append(Note(text: "relevé il y a " + FrenchDuration.dur(minutes: a), orange: true))
            tone = .muted   // a stale reading mutes the sentence: it may no longer be true
        }
        return VerdictModel(tone, phrase, sub, notes: notes)
    }
}

// MARK: - Account row

public struct AccountRowModel: Equatable, Sendable {
    public let letter: String
    /// « aucun relevé »: the row is reduced to the letter and this. A credit cap is not one:
    /// it stops the overage beyond the plan, not the plan, so the row keeps its reading.
    public let unusableReason: String?
    public let weekLine: String
    public let fast: Bool
    public let barFraction: Double
    public let tickFraction: Double?
    public let barOrange: Bool
    /// « 5 h 31 % dispo » | « 5 h saturé » (orange, bold); nil without a 5 h window.
    public let fiveHour: String?
    public let fiveHourOrange: Bool
    public let renewals: String?
    /// « relevé il y a … · actualisation en échec · <modèle> saturé »; nil when none.
    public let notices: String?
    /// The first reason this account asks for attention (priority of `viewer.js:79`); nil when none.
    public let attention: String?
    /// The reason lights the detail header's dot (a capped credit account is normal, « rapide » never counts).
    public let lightsDot: Bool

    static let creditsCapped = "crédits hors plan épuisés"

    public init(account a: UsageAccount, now: Date) {
        letter = a.label
        switch a.unusable {
        case "": unusableReason = nil
        case "no-reading": unusableReason = "aucun relevé"
        case "cap": unusableReason = nil
        default: unusableReason = a.unusable
        }
        let free = a.sevenDay.map { 100 - $0.percent }
        weekLine = free.map { "\($0)\(nb)% dispo semaine" } ?? "aucun relevé"
        fast = a.fast
        barFraction = Double(free ?? 0) / 100
        tickFraction = a.windowLeft
        let weekFull = a.sevenDay?.full ?? false
        barOrange = a.fast || weekFull
        let fiveFull = a.fiveHour?.full ?? false
        fiveHourOrange = fiveFull
        if let f = a.fiveHour {
            fiveHour = f.full ? "5\(nb)h saturé" : "5\(nb)h \(100 - f.percent)\(nb)% dispo"
        } else { fiveHour = nil }

        // renewals: a passed 5 h reset drops « 5 h dans … », a passed weekly reset counts as reset + 7 j.
        let week = a.sevenDay?.resetsAt.map { FrenchDuration.advancedWeek($0, now: now) }
        let five = a.fiveHour?.resetsAt.flatMap { $0 > now ? $0 : nil }
        let weekText = week.map { "sem. dans " + FrenchDuration.dur(minutes: FrenchDuration.minutes(from: now, to: $0)) }
        let fiveText = five.map { "5\(nb)h dans " + FrenchDuration.dur(minutes: FrenchDuration.minutes(from: now, to: $0)) }
        if let w = week, let f = five, !weekFull, f <= w {
            renewals = [fiveText, weekText].compactMap { $0 }.joined(separator: " · ")
        } else {
            renewals = weekText ?? fiveText
        }

        let staleAge = age(of: a.fetchedAt, now: now)
        var parts: [String] = []
        if let s = staleAge { parts.append("relevé il y a " + FrenchDuration.dur(minutes: s)) }
        if a.refreshFailed { parts.append("actualisation en échec") }
        let saturatedModel = a.scoped.first { $0.full }?.model
        if let m = saturatedModel { parts.append("\(m) saturé") }   // G3
        if a.unusable == "cap" { parts.append(Self.creditsCapped) }
        notices = parts.isEmpty ? nil : parts.joined(separator: " · ")

        if let r = unusableReason { attention = r }
        else if fiveFull { attention = "5\(nb)h saturé" }
        else if weekFull { attention = "semaine saturée" }
        else if let m = saturatedModel { attention = "\(m) saturé" }
        else if a.refreshFailed { attention = "actualisation en échec" }
        else if staleAge != nil { attention = "relevé ancien" }
        else if a.unusable == "cap" { attention = Self.creditsCapped }
        else { attention = nil }
        // The detail dot (viewer.js `anyWarn`): saturated, stale, failing or unread — never « rapide »;
        // a credit cap alone is normal, but a capped account that is also one of these still lights it.
        let otherUnusable = !a.unusable.isEmpty && a.unusable != "cap"
        lightsDot = otherUnusable || fiveFull || weekFull || saturatedModel != nil || a.refreshFailed || staleAge != nil
    }
}

// MARK: - Next session

public struct NextSessionModel: Equatable, Sendable {
    public struct Entry: Equatable, Sendable {
        public let scope: String
        /// The scope's name as the route shows it, never written in Swift.
        public let display: String
        /// The account's letter, or « — » without a candidate (G4).
        public let letter: String
        public let attention: String?
        public var accessibilityLetter: String { letter == "—" ? "aucun compte" : letter }
        /// One spoken label for the entry, the attention reason included (« gx t, attention : … »).
        public var accessibilityLabel: String {
            let base = "\(display) \(accessibilityLetter)"
            return attention.map { "\(base), \(AttentionGlyph.label(for: $0))" } ?? base
        }
    }

    public let entries: [Entry]

    public init(overview: UsageOverview, now: Date) {
        // master first, the table's order kept inside each side
        let picks = overview.nextSession.filter(\.master) + overview.nextSession.filter { !$0.master }
        entries = picks.map { p in
            // I11: the account is found by uuid, never by label
            guard p.error == nil, let uuid = p.accountUUID,
                  let account = overview.accounts.first(where: { $0.accountUUID == uuid }) else {
                return Entry(scope: p.scope, display: p.display, letter: "—", attention: nil)
            }
            return Entry(scope: p.scope, display: p.display, letter: account.label,
                         attention: AccountRowModel(account: account, now: now).attention)
        }
    }
}

// MARK: - Detail header

public struct DetailHeaderModel: Equatable, Sendable {
    public let showsDot: Bool
    public let dotLabel: String
    /// G16: shown in orange at the top of the OPEN detail.
    public let unreadableCaption: String?

    public init(overview: UsageOverview, now: Date) {
        let n = overview.unreadableCount
        unreadableCaption = n <= 0 ? nil : (n == 1 ? "Un compte est illisible" : "\(n) comptes illisibles")
        let accountsAsk = overview.accounts.contains { AccountRowModel(account: $0, now: now).lightsDot }
        showsDot = accountsAsk || n > 0
        dotLabel = (!accountsAsk && n > 0) ? (unreadableCaption ?? "") : "un compte demande attention"
    }
}
