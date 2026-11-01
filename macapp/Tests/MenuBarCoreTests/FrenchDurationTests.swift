import XCTest
@testable import MenuBarCore

final class FrenchDurationTests: XCTestCase {
    private let utc = TimeZone(identifier: "UTC")!
    private let nb = "\u{00A0}"
    private let now = ISO8601DateFormatter().date(from: "2026-10-20T21:00:00Z")!

    func testDurFormats() {
        XCTAssertEqual(FrenchDuration.dur(minutes: 45), "45\(nb)min")
        XCTAssertEqual(FrenchDuration.dur(minutes: 139), "2\(nb)h\(nb)19")
        XCTAssertEqual(FrenchDuration.dur(minutes: 125), "2\(nb)h\(nb)05")
        XCTAssertEqual(FrenchDuration.dur(minutes: 120), "2\(nb)h")
        XCTAssertEqual(FrenchDuration.dur(minutes: 5040), "3\(nb)j\(nb)12\(nb)h")
        XCTAssertEqual(FrenchDuration.dur(minutes: 5760), "4\(nb)j")
        XCTAssertEqual(FrenchDuration.dur(minutes: 6479), "4\(nb)j\(nb)12\(nb)h")
    }

    func testDurRound() {
        XCTAssertEqual(FrenchDuration.durRound(minutes: 1099), "18\(nb)h")
        XCTAssertEqual(FrenchDuration.durRound(minutes: 2440), "1\(nb)j\(nb)17\(nb)h")
        XCTAssertEqual(FrenchDuration.durRound(minutes: 100), "1\(nb)h\(nb)40", "under 3 h it stays exact")
    }

    func testAroundRoundsBeforeDay() {
        let t = ISO8601DateFormatter().date(from: "2026-10-20T23:40:00Z")!
        XCTAssertEqual(FrenchDuration.around(t, now: now, timeZone: utc), "demain vers 0\(nb)h",
                       "23:40 rounds to midnight and the day is taken after rounding")
        let thu = ISO8601DateFormatter().date(from: "2026-10-22T14:40:00Z")!
        XCTAssertEqual(FrenchDuration.around(thu, now: now, timeZone: utc), "jeu. vers 15\(nb)h")
    }

    func testDayWords() {
        let sameDay = ISO8601DateFormatter().date(from: "2026-10-20T22:30:00Z")!
        XCTAssertEqual(FrenchDuration.dayWord(sameDay, now: now, timeZone: utc), "aujourd\u{2019}hui")
        let sun = ISO8601DateFormatter().date(from: "2026-10-25T03:00:00Z")!
        XCTAssertEqual(FrenchDuration.dayWord(sun, now: now, timeZone: utc), "dim.")
    }

    func testMinutesNeverNegative() {
        let past = now.addingTimeInterval(-600)
        XCTAssertEqual(FrenchDuration.minutes(from: now, to: past), 0)
        XCTAssertEqual(FrenchDuration.minutes(from: now, to: now.addingTimeInterval(89)), 1)
    }

    func testAdvancedWeek() {
        let past = now.addingTimeInterval(-600)
        XCTAssertEqual(FrenchDuration.advancedWeek(past, now: now), past.addingTimeInterval(7 * 86400))
        let future = now.addingTimeInterval(600)
        XCTAssertEqual(FrenchDuration.advancedWeek(future, now: now), future)
        let veryPast = now.addingTimeInterval(-15 * 86400)
        XCTAssertGreaterThan(FrenchDuration.advancedWeek(veryPast, now: now), now)
    }
}
