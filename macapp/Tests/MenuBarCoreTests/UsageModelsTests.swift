import XCTest
@testable import MenuBarCore

// Fixtures: the mockup's scenarios (UsageFixtures), now = 2026-10-20T21:00Z, UTC.
private let utc = UsageFixtures.utc
private let nb = UsageFixtures.nb
private let now = UsageFixtures.now
private func at(_ s: String) -> Date { UsageFixtures.at(s) }
private func acct(_ uuid: String, _ label: String, d7: Int, d7r: String, h5: Int, h5r: String?, left: Double? = nil,
                  fast: Bool = false, ageMin: Int = 4, failed: Bool = false, unusable: String = "",
                  o5: Bool = false, o7: Bool = false, scoped: [UsageScoped] = []) -> UsageAccount {
    UsageFixtures.acct(uuid, label, d7: d7, d7r: d7r, h5: h5, h5r: h5r, left: left, fast: fast, ageMin: ageMin,
                       failed: failed, unusable: unusable, o5: o5, o7: o7, scoped: scoped)
}
private let m = UsageFixtures.m
private let i = UsageFixtures.i
private let j = UsageFixtures.j
private let t = UsageFixtures.t
private func overview(_ accounts: [UsageAccount], verdict: UsageVerdict?, picks: [UsageScopePick] = [],
                      unreadable: Int = 0) -> UsageOverview {
    UsageFixtures.overview(accounts, verdict: verdict, picks: picks, unreadable: unreadable)
}
private func ready(_ o: UsageOverview, failed: Bool = false) -> UsageDisplay { UsageFixtures.ready(o, failed: failed) }
private let normalVerdict = UsageFixtures.normalVerdict

private func verdictModel(_ v: UsageVerdict, at clock: Date = now) -> VerdictModel {
    VerdictModel(display: ready(overview([m, i, j], verdict: v)), now: clock, timeZone: utc)
}

final class UsageModelsTests: XCTestCase {
    // MARK: verdict

    func testVerdictMockupNormal() {
        let v = verdictModel(normalVerdict)
        XCTAssertEqual(v.tone, .ok)
        XCTAssertEqual(v.phrase, "Tu tiens jusqu\u{2019}aux renouvellements")
        XCTAssertEqual(v.sub, "Prochain renouvellement\(nb): i dans 4\(nb)j\(nb)12\(nb)h")
        XCTAssertTrue(v.notes.isEmpty)
        XCTAssertFalse(v.isDaemonMessage)
    }

    func testVerdictMockupTooFast() {
        let v = verdictModel(UsageVerdict(state: "later", oldestReadingAt: now.addingTimeInterval(-300),
                                          blockedFrom: at("22/10 14:40"), frees: UsageLabelAt(label: "i", at: at("23/10 08:59"))))
        XCTAssertEqual(v.tone, .warn)
        XCTAssertEqual(v.phrase, "Au rythme actuel, bloqué\(nb)jeu.\(nb)vers\(nb)15\(nb)h")
        XCTAssertEqual(v.sub, "18\(nb)h avant le renouvellement de i")
    }

    func testVerdictMockupWeekStart() {
        let v = verdictModel(UsageVerdict(state: "later", provisional: true, oldestReadingAt: now.addingTimeInterval(-300),
                                          blockedFrom: at("25/10 03:20"), frees: UsageLabelAt(label: "i", at: at("26/10 20:00"))))
        XCTAssertEqual(v.phrase, "Au rythme actuel, bloqué\(nb)dim.\(nb)vers\(nb)3\(nb)h")
        XCTAssertEqual(v.sub, "1\(nb)j\(nb)17\(nb)h avant le renouvellement de i")
        XCTAssertEqual(v.notes, [VerdictModel.Note(text: "estimation provisoire, peu d\u{2019}historique", orange: false)])
    }

    func testVerdictMockupBlockedNow() {
        let v = verdictModel(UsageVerdict(state: "now", oldestReadingAt: now.addingTimeInterval(-300),
                                          frees: UsageLabelAt(label: "j", at: at("20/10 21:52"))))
        XCTAssertEqual(v.tone, .alarm)
        XCTAssertEqual(v.phrase, "Bloqué maintenant")
        XCTAssertEqual(v.sub, "Libre dans 52\(nb)min\(nb)(compte\(nb)j)")
    }

    func testVerdictStale() {
        let v = verdictModel(UsageVerdict(state: "ok", oldestReadingAt: now.addingTimeInterval(-125 * 60),
                                          nextRenewal: UsageLabelAt(label: "i", at: at("25/10 08:59"))))
        XCTAssertEqual(v.notes, [VerdictModel.Note(text: "relevé il y a 2\(nb)h\(nb)05", orange: true)])
        XCTAssertEqual(v.tone, .muted, "a stale reading mutes the sentence")
        XCTAssertEqual(verdictModel(UsageVerdict(state: "ok", oldestReadingAt: now.addingTimeInterval(-60 * 60))).tone, .ok,
                       "exactly 60 min is still fresh")
    }

    func testStaleAgeGrowsWithClock() {
        let v = UsageVerdict(state: "ok", oldestReadingAt: now.addingTimeInterval(-30 * 60))
        XCTAssertTrue(verdictModel(v, at: now).notes.isEmpty)
        // the route failed since: the overview is frozen, only the clock moves
        let later = verdictModel(v, at: now.addingTimeInterval(95 * 60))
        XCTAssertEqual(later.notes.first?.text, "relevé il y a 2\(nb)h\(nb)05")
        XCTAssertEqual(later.tone, .muted)
        let display = ready(overview([m], verdict: v), failed: true)
        XCTAssertEqual(VerdictModel(display: display, now: now.addingTimeInterval(200 * 60), timeZone: utc).notes.first?.text,
                       "relevé il y a 3\(nb)h\(nb)50")
    }

    func testVerdictTight() {
        let v = verdictModel(UsageVerdict(state: "tight", nextRenewal: UsageLabelAt(label: "i", at: at("25/10 08:59"))))
        XCTAssertEqual(v.tone, .warn)
        XCTAssertEqual(v.phrase, "Tu tiens, de justesse")
        XCTAssertEqual(v.sub, "Prochain renouvellement\(nb): i dans 4\(nb)j\(nb)12\(nb)h")
    }

    func testVerdictNowUnknownFree() {
        let v = verdictModel(UsageVerdict(state: "now"))
        XCTAssertEqual(v.phrase, "Bloqué maintenant")
        XCTAssertEqual(v.sub, "Heure de déblocage inconnue")
    }

    func testVerdictLaterUnknownFree() {
        let v = verdictModel(UsageVerdict(state: "later", blockedFrom: at("22/10 14:40")))
        XCTAssertEqual(v.phrase, "Au rythme actuel, bloqué\(nb)jeu.\(nb)vers\(nb)15\(nb)h")
        XCTAssertEqual(v.sub, "Heure de déblocage inconnue", "G13")
    }

    func testVerdictOkNoRenewal() {
        XCTAssertNil(verdictModel(UsageVerdict(state: "ok")).sub, "G14")
        XCTAssertNil(verdictModel(UsageVerdict(state: "tight")).sub)
    }

    func testVerdictNone() {
        let v = verdictModel(UsageVerdict(state: "none"))
        XCTAssertEqual(v.phrase, "Aucun compte lisible")
        XCTAssertNil(v.sub)
        XCTAssertEqual(v.tone, .muted)
    }

    func testVerdictLoading() {
        let v = VerdictModel(display: .loading, now: now, timeZone: utc)
        XCTAssertEqual(v.phrase, "Lecture des relevés…")
        XCTAssertFalse(v.isDaemonMessage)
    }

    func testVerdictUnreachable() {
        let v = VerdictModel(display: UsageDisplay(state: .unreachable, overview: nil, readFailed: true), now: now, timeZone: utc)
        XCTAssertEqual(v.phrase, "Le démon pigtail est indisponible.")
        XCTAssertEqual(v.sub, "La conso et la rotation reviennent avec lui.")
        XCTAssertTrue(v.isDaemonMessage)
    }

    func testVerdictPassedRenewal() {
        let v = verdictModel(UsageVerdict(state: "ok", nextRenewal: UsageLabelAt(label: "i", at: now.addingTimeInterval(-600))))
        XCTAssertEqual(v.sub, "Prochain renouvellement\(nb): i dans \(FrenchDuration.dur(minutes: 10_070))")
    }

    // MARK: account rows

    func testRowFast() {
        let r = AccountRowModel(account: i, now: now)
        XCTAssertEqual(r.letter, "i")
        XCTAssertEqual(r.weekLine, "36\(nb)% dispo semaine")
        XCTAssertTrue(r.fast)
        XCTAssertTrue(r.barOrange)
        XCTAssertEqual(r.barFraction, 0.36, accuracy: 1e-9)
        XCTAssertEqual(try XCTUnwrap(r.tickFraction), 0.643, accuracy: 1e-9)
        XCTAssertFalse(r.lightsDot, "« rapide » never lights the dot")
        XCTAssertNil(r.attention)
    }

    func testRowFiveHourFull() {
        let a = acct("u-m", "m", d7: 20, d7r: "25/10 17:59", h5: 92, h5r: "20/10 23:19", o5: true)
        let r = AccountRowModel(account: a, now: now)
        XCTAssertEqual(r.fiveHour, "5\(nb)h saturé")
        XCTAssertTrue(r.fiveHourOrange)
        XCTAssertEqual(r.attention, "5\(nb)h saturé")
        XCTAssertTrue(r.lightsDot)
        XCTAssertEqual(AccountRowModel(account: m, now: now).fiveHour, "5\(nb)h 84\(nb)% dispo")
    }

    func testRowWeekOnlyRenewal() {
        let r = AccountRowModel(account: j, now: now)
        XCTAssertEqual(r.renewals, "sem. dans 6\(nb)j\(nb)6\(nb)h")
        XCTAssertFalse(r.renewals?.contains("5\(nb)h dans") ?? true)
        XCTAssertEqual(AccountRowModel(account: m, now: now).renewals, "5\(nb)h dans 2\(nb)h\(nb)19 · sem. dans 4\(nb)j\(nb)21\(nb)h")
    }

    func testRowPassedReset() {
        let a = acct("u-m", "m", d7: 0, d7r: "20/10 20:50", h5: 0, h5r: "20/10 20:50")
        let r = AccountRowModel(account: a, now: now)
        XCTAssertEqual(r.renewals, "sem. dans \(FrenchDuration.dur(minutes: 10_070))")
        XCTAssertFalse(r.renewals?.contains("5\(nb)h dans") ?? true)
    }

    func testRowStaleAndFailed() {
        let a = acct("u-i", "i", d7: 64, d7r: "25/10 08:59", h5: 69, h5r: "20/10 23:29", ageMin: 125, failed: true)
        let r = AccountRowModel(account: a, now: now)
        XCTAssertEqual(r.notices, "relevé il y a 2\(nb)h\(nb)05 · actualisation en échec")
        XCTAssertEqual(r.attention, "actualisation en échec")
        let stale = AccountRowModel(account: acct("u-m", "m", d7: 1, d7r: "25/10 08:59", h5: 1, h5r: nil, ageMin: 72), now: now)
        XCTAssertEqual(stale.attention, "relevé ancien")
    }

    func testRowScopedSaturated() {
        let fable = UsageScoped(model: "Fable", percent: 96, resetsAt: at("25/10 17:59"), full: true)
        let r = AccountRowModel(account: acct("u-m", "m", d7: 20, d7r: "25/10 17:59", h5: 10, h5r: nil, scoped: [fable]), now: now)
        XCTAssertEqual(r.notices, "Fable saturé")
        XCTAssertEqual(r.attention, "Fable saturé")
    }

    func testRowUnusableCap() {
        // The credit cap stops the overage beyond the plan, not the plan: the row keeps
        // its plan reading and only notes the spent credits.
        let r = AccountRowModel(account: t, now: now)
        XCTAssertNil(r.unusableReason)
        XCTAssertTrue(r.weekLine.hasSuffix("dispo semaine"), r.weekLine)
        XCTAssertEqual(r.notices, "crédits hors plan épuisés")
        XCTAssertEqual(r.attention, "crédits hors plan épuisés")
        XCTAssertFalse(r.lightsDot, "a capped credit account is normal, not a worry")
    }

    func testCappedPlusOtherWorryLightsDot() {
        let fable = UsageScoped(model: "Fable", percent: 96, resetsAt: at("25/10 17:59"), full: true)
        let cases: [(String, UsageAccount)] = [
            ("stale", acct("u-c", "c", d7: 3, d7r: "24/10 20:59", h5: 0, h5r: nil, ageMin: 125, unusable: "cap")),
            ("refresh failed", acct("u-c", "c", d7: 3, d7r: "24/10 20:59", h5: 0, h5r: nil, failed: true, unusable: "cap")),
            ("5 h full", acct("u-c", "c", d7: 3, d7r: "24/10 20:59", h5: 95, h5r: nil, unusable: "cap", o5: true)),
            ("week full", acct("u-c", "c", d7: 99, d7r: "24/10 20:59", h5: 0, h5r: nil, unusable: "cap", o7: true)),
            ("model full", acct("u-c", "c", d7: 3, d7r: "24/10 20:59", h5: 0, h5r: nil, unusable: "cap", scoped: [fable])),
        ]
        for (name, a) in cases {
            XCTAssertTrue(AccountRowModel(account: a, now: now).lightsDot, "cap + \(name) must light the dot")
        }
        XCTAssertFalse(AccountRowModel(account: t, now: now).lightsDot, "cap alone stays quiet")
        XCTAssertTrue(DetailHeaderModel(overview: overview([cases[0].1], verdict: nil), now: now).showsDot)
    }

    func testNextSessionLabelCarriesTheReason() {
        let gx = UsageScopePick(scope: "globex", display: "gx", master: false, accountUUID: "u-t")
        let n = NextSessionModel(overview: overview([m, t], verdict: nil, picks: [gx, UsageScopePick(scope: "perso", display: "perso", master: true, accountUUID: "u-m")]), now: now)
        XCTAssertEqual(n.entries[0].accessibilityLabel, "perso m")
        XCTAssertEqual(n.entries[1].accessibilityLabel, "gx t, attention : crédits hors plan épuisés")
        let none = UsageScopePick(scope: "globex", display: "gx", master: false, error: "no-candidate")
        XCTAssertEqual(NextSessionModel(overview: overview([m], verdict: nil, picks: [none]), now: now).entries[0].accessibilityLabel,
                       "gx aucun compte")
    }

    func testRowNoReading() {
        let a = UsageAccount(accountUUID: "u-x", label: "x", inVerdict: false, unusable: "no-reading")
        let r = AccountRowModel(account: a, now: now)
        XCTAssertEqual(r.unusableReason, "aucun relevé")
        XCTAssertNil(r.fiveHour)
        XCTAssertTrue(r.lightsDot)
    }

    // MARK: next session

    private let perso = UsageScopePick(scope: "perso", display: "perso", master: true, accountUUID: "u-i")

    func testNextSessionTriangle() {
        let gx = UsageScopePick(scope: "globex", display: "gx", master: false, accountUUID: "u-t")
        let o = overview([m, i, j, t], verdict: normalVerdict, picks: [gx, perso])
        let n = NextSessionModel(overview: o, now: now)
        XCTAssertEqual(n.entries.map(\.display), ["perso", "gx"], "master first; display comes from the route")
        XCTAssertEqual(n.entries[0].letter, "i")
        XCTAssertNil(n.entries[0].attention)
        XCTAssertEqual(n.entries[1].letter, "t")
        XCTAssertEqual(n.entries[1].attention, "crédits hors plan épuisés")
    }

    func testNextSessionLooksUpByUUID() {
        // labels swapped relative to the order: only the uuid decides
        let a = acct("u-1", "z", d7: 10, d7r: "25/10 08:59", h5: 1, h5r: nil)
        let b = acct("u-2", "a", d7: 10, d7r: "25/10 08:59", h5: 1, h5r: nil)
        let pick = UsageScopePick(scope: "perso", display: "perso", master: true, accountUUID: "u-2")
        let n = NextSessionModel(overview: overview([a, b], verdict: nil, picks: [pick]), now: now)
        XCTAssertEqual(n.entries[0].letter, "a")
        let other = UsageScopePick(scope: "perso", display: "perso", master: true, accountUUID: "u-1")
        XCTAssertEqual(NextSessionModel(overview: overview([a, b], verdict: nil, picks: [other]), now: now).entries[0].letter, "z")
    }

    func testNextSessionUnknownUUID() {
        let ghost = UsageScopePick(scope: "perso", display: "perso", master: true, accountUUID: "nope")
        let e = NextSessionModel(overview: overview([m], verdict: nil, picks: [ghost]), now: now).entries[0]
        XCTAssertEqual(e.letter, "—")
        XCTAssertEqual(e.accessibilityLetter, "aucun compte")
    }

    func testNextSessionNoCandidate() {
        let none = UsageScopePick(scope: "globex", display: "gx", master: false, error: "no-candidate")
        let e = NextSessionModel(overview: overview([m], verdict: nil, picks: [none]), now: now).entries[0]
        XCTAssertEqual(e.letter, "—")
        XCTAssertEqual(e.accessibilityLetter, "aucun compte")
        XCTAssertNil(e.attention)
    }

    // MARK: detail header

    func testDetailDotNeverForFastOnly() {
        let h = DetailHeaderModel(overview: overview([m, i, j, t], verdict: normalVerdict), now: now)
        XCTAssertFalse(h.showsDot)
        XCTAssertNil(h.unreadableCaption)
    }

    func testDetailDotForUnreadableCell() {
        let h = DetailHeaderModel(overview: overview([m, j], verdict: normalVerdict, unreadable: 1), now: now)
        XCTAssertTrue(h.showsDot)
        XCTAssertEqual(h.dotLabel, "Un compte est illisible")
        let sat = acct("u-s", "s", d7: 96, d7r: "25/10 08:59", h5: 1, h5r: nil, o7: true)
        let both = DetailHeaderModel(overview: overview([m, sat], verdict: normalVerdict, unreadable: 1), now: now)
        XCTAssertTrue(both.showsDot)
        XCTAssertEqual(both.dotLabel, "un compte demande attention")
        let only = DetailHeaderModel(overview: overview([m, sat], verdict: normalVerdict), now: now)
        XCTAssertEqual(only.dotLabel, "un compte demande attention")
    }

    func testUnreadableCaption() {
        func caption(_ n: Int) -> String? { DetailHeaderModel(overview: overview([m], verdict: nil, unreadable: n), now: now).unreadableCaption }
        XCTAssertEqual(caption(1), "Un compte est illisible")
        XCTAssertEqual(caption(3), "3 comptes illisibles")
        XCTAssertNil(caption(0))
    }
}
