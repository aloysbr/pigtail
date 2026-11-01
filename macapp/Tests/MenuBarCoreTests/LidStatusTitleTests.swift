import XCTest
@testable import MenuBarCore

/// D23: the text next to the head in the menu bar — "Travel Mode 3 h", "Travel Mode 45 min",
/// "Travel Mode" — and when it next changes.
final class LidStatusTitleTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 2_000_000_000)
    private let nbsp = "\u{00A0}"

    private func on(in seconds: TimeInterval?) -> LidDisplay {
        .fold(response: LidResponse(sleepDisabled: true, deadline: seconds.map { now.addingTimeInterval($0) }),
              readFailure: nil, unavailable: false, actionError: nil, isActing: false)
    }

    private func title(in seconds: TimeInterval?) -> String? {
        LidStatusTitle.title(for: on(in: seconds), now: now)
    }

    func testHoursRoundUp() {
        XCTAssertEqual(title(in: 2 * 3600 + 41 * 60), "Travel Mode 3\(nbsp)h")
        XCTAssertEqual(title(in: 7 * 3600 + 59 * 60), "Travel Mode 8\(nbsp)h")
        XCTAssertEqual(title(in: 8 * 3600), "Travel Mode 8\(nbsp)h", "8 h pile reste 8 h")
        XCTAssertEqual(title(in: 3600 + 1), "Travel Mode 2\(nbsp)h")
    }

    func testUnderAnHourShowsMinutesRoundedUp() {
        XCTAssertEqual(title(in: 45 * 60), "Travel Mode 45\(nbsp)min")
        XCTAssertEqual(title(in: 44 * 60 + 10), "Travel Mode 45\(nbsp)min")
        XCTAssertEqual(title(in: 30), "Travel Mode 1\(nbsp)min")
    }

    func testSixtyMinutesIsOneHour() {
        XCTAssertEqual(title(in: 3600), "Travel Mode 1\(nbsp)h")
        XCTAssertEqual(title(in: 59 * 60 + 30), "Travel Mode 1\(nbsp)h", "59 min 30 s arrondies = 60 min → 1 h")
        XCTAssertEqual(title(in: 59 * 60), "Travel Mode 59\(nbsp)min")
    }

    func testPastDeadlineStillOnShowsOneMinute() {
        // Le daemon coupe à l'échéance ; tant que la lecture dit actif, jamais « 0 min » ni négatif.
        XCTAssertEqual(title(in: 0), "Travel Mode 1\(nbsp)min")
        XCTAssertEqual(title(in: -120), "Travel Mode 1\(nbsp)min")
    }

    func testNoTimerIsTheNameAlone() {
        XCTAssertEqual(title(in: nil), "Travel Mode")
    }

    func testInactiveOrUnknownIsNil() {
        let off = LidDisplay.fold(response: LidResponse(sleepDisabled: false), readFailure: nil,
                                  unavailable: false, actionError: nil, isActing: false)
        XCTAssertNil(LidStatusTitle.title(for: off, now: now))
        XCTAssertNil(LidStatusTitle.title(for: .loading, now: now))
        for failure in [LidReadFailure.unreachable, .failed("pmset -g failed")] {
            let never = LidDisplay.fold(response: nil, readFailure: failure, unavailable: false,
                                        actionError: nil, isActing: false)
            XCTAssertNil(LidStatusTitle.title(for: never, now: now), "jamais lu : aucun texte")
        }
        let unavailable = LidDisplay.fold(response: nil, readFailure: nil, unavailable: true,
                                          actionError: nil, isActing: false)
        XCTAssertNil(LidStatusTitle.title(for: unavailable, now: now))
    }

    func testFrozenOnAfterAFailedReadStillShows() {
        let frozen = LidDisplay.fold(response: LidResponse(sleepDisabled: true, deadline: now.addingTimeInterval(45 * 60)),
                                     readFailure: .unreachable, unavailable: false, actionError: nil, isActing: false)
        XCTAssertEqual(LidStatusTitle.title(for: frozen, now: now), "Travel Mode 45\(nbsp)min",
                       "lecture en échec avec un état connu : l'état connu reste affiché")
    }

    func testSeparatorIsANoBreakSpace() throws {
        for t in [try XCTUnwrap(title(in: 2 * 3600 + 41 * 60)), try XCTUnwrap(title(in: 45 * 60))] {
            let tail = try XCTUnwrap(t.split(separator: " ").last, "« Travel Mode » garde son espace normale")
            XCTAssertTrue(tail.unicodeScalars.contains("\u{00A0}"), "\(t) : U+00A0 entre nombre et unité")
            XCTAssertEqual(t.unicodeScalars.filter { $0 == " " }.count, 2,
                           "\(t) : deux espaces U+0020 seulement (Travel␣Mode␣N), aucune entre nombre et unité")
        }
    }

    func testNextRefreshIsTheNextMinuteBoundary() throws {
        // 2h41m20s restantes : le texte change quand il reste un multiple de 60 s, soit dans 20 s.
        let d = on(in: 2 * 3600 + 41 * 60 + 20)
        let next = try XCTUnwrap(LidStatusTitle.nextRefresh(for: d, now: now))
        XCTAssertEqual(next.timeIntervalSince(now), 20, accuracy: 0.5)
        // Sur une frontière exacte : la suivante, une minute plus tard.
        let exact = try XCTUnwrap(LidStatusTitle.nextRefresh(for: on(in: 45 * 60), now: now))
        XCTAssertEqual(exact.timeIntervalSince(now), 60, accuracy: 0.5)
        // Toujours entre 0 et 60 s : jamais plus d'une minute sans relire l'heure.
        for r in stride(from: 1.0, through: 9 * 3600, by: 437) {
            let n = try XCTUnwrap(LidStatusTitle.nextRefresh(for: on(in: r), now: now))
            let delay = n.timeIntervalSince(now)
            XCTAssertGreaterThan(delay, 0); XCTAssertLessThanOrEqual(delay, 60.5)
        }
        // Le texte a bien changé juste après l'instant donné (minute 46 → 45).
        let at = on(in: 45 * 60 + 20)
        let tick = try XCTUnwrap(LidStatusTitle.nextRefresh(for: at, now: now))
        XCTAssertEqual(LidStatusTitle.title(for: at, now: now), "Travel Mode 46\(nbsp)min")
        XCTAssertEqual(LidStatusTitle.title(for: at, now: tick), "Travel Mode 45\(nbsp)min")
    }

    func testNoRefreshWithoutADeadline() {
        XCTAssertNil(LidStatusTitle.nextRefresh(for: on(in: nil), now: now))
        XCTAssertNil(LidStatusTitle.nextRefresh(for: .loading, now: now))
        XCTAssertNil(LidStatusTitle.nextRefresh(for: on(in: -5), now: now), "échéance passée : on attend la lecture")
    }
}
