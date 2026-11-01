import XCTest
@testable import MenuBarCore

final class LidCutNoticeTests: XCTestCase {
    private let paris = TimeZone(identifier: "Europe/Paris")!
    /// 2026-10-24 14:32 Europe/Paris = 12:32Z.
    private let at1432 = ISO8601DateFormatter().date(from: "2026-10-24T12:32:00Z")!
    private let dayBefore = ISO8601DateFormatter().date(from: "2026-10-23T07:00:00Z")!
    private let tail = " — le Mac peut de nouveau dormir capot fermé."

    private func r(off: LidOff? = nil, error: String? = nil, floor: Int = 25) -> LidResponse {
        LidResponse(sleepDisabled: off == nil, batteryFloor: floor, lastOff: off, lastError: error)
    }

    private func notice(_ prev: LidResponse?, _ cur: LidResponse) -> LidCutNotice? {
        LidCutNotices.notice(previous: prev, current: cur, timeZone: paris)
    }

    private func cutNotice(_ reason: String) -> LidCutNotice? {
        notice(r(off: LidOff(at: dayBefore, reason: "deadline")), r(off: LidOff(at: at1432, reason: reason)))
    }

    func testNoNoticeAtBaseline() {
        XCTAssertNil(notice(nil, r(off: LidOff(at: at1432, reason: "thermal critical"), error: "auto-off (deadline) failed: x")))
    }

    func testDeadline() {
        XCTAssertEqual(cutNotice("deadline"),
                       LidCutNotice(id: "2026-10-24T12:32:00Z", title: "Travel Mode coupé à 14:32",
                                    body: "Fin du minuteur — le Mac peut de nouveau dormir capot fermé."))
    }

    func testBattery() {
        let n = cutNotice("battery 24%")
        XCTAssertEqual(n?.title, "Travel Mode coupé à 14:32")
        XCTAssertEqual(n?.body, "Batterie à 24 % (seuil 25 %) — le Mac peut de nouveau dormir capot fermé.")
    }

    func testThermalSerious() {
        let n = cutNotice("thermal serious")
        XCTAssertEqual(n?.title, "Travel Mode coupé à 14:32")
        XCTAssertEqual(n?.body, "Mac chaud capot fermé depuis 10 min (état thermique serious) — le Mac peut de nouveau dormir capot fermé.")
    }

    func testThermalCritical() {
        let n = cutNotice("thermal critical")
        XCTAssertEqual(n?.title, "Travel Mode coupé à 14:32")
        XCTAssertEqual(n?.body, "Mac en surchauffe capot fermé (état thermique critical) — le Mac peut de nouveau dormir capot fermé.")
    }

    func testManualAndExternalSilent() {
        XCTAssertNil(cutNotice("manual"))
        XCTAssertNil(cutNotice("external"))
    }

    func testSameCutNotifiedOnce() {
        let same = r(off: LidOff(at: at1432, reason: "thermal critical"))
        XCTAssertNil(notice(same, same))
    }

    func testFirstCutNotified() {
        let n = notice(r(), r(off: LidOff(at: at1432, reason: "thermal critical")))
        XCTAssertEqual(n?.id, "2026-10-24T12:32:00Z")
    }

    func testLateCutNotifiedWithTime() {
        // Read at wake-up, hours after the cut: notified, with the cut's own time.
        let n = notice(r(off: LidOff(at: dayBefore, reason: "deadline")),
                       r(off: LidOff(at: at1432, reason: "thermal serious")))
        XCTAssertEqual(n?.title, "Travel Mode coupé à 14:32")
    }

    func testAutoOffFailure() {
        let e = "auto-off (thermal critical) failed: x"
        let want = LidCutNotice(id: "lid-auto-off-failed", title: "Travel Mode : coupure échouée", body: e)
        XCTAssertEqual(notice(r(), r(error: e)), want)
        XCTAssertNil(notice(r(error: e), r(error: e)), "the same failure twice: one notice")
        XCTAssertEqual(notice(r(error: ""), r(error: e)), want, "\"\" is empty")
    }

    func testFailureReasonDriftNotifiedOnce() {
        let e24 = "auto-off (battery 24%) failed: x", e23 = "auto-off (battery 23%) failed: x"
        XCTAssertNotNil(notice(r(), r(error: e24)))
        XCTAssertNil(notice(r(error: e24), r(error: e23)))
        XCTAssertNil(notice(r(error: e23), r()))
        XCTAssertEqual(notice(r(), r(error: "auto-off (battery 22%) failed: y"))?.id, "lid-auto-off-failed")
    }

    func testUnknownReasonSilent() {
        XCTAssertNil(cutNotice("something new"))
        XCTAssertNil(cutNotice("battery x%"))
    }
}
