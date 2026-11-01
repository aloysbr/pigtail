import XCTest
@testable import MenuBarCore

final class MaintenanceChipDisplayTests: XCTestCase {
    private func at(_ e: Double) -> Date { Date(timeIntervalSince1970: e) }
    private func fold(_ w: MaintenanceWindow?, now: Double, readError: Bool = false, takeBackError: String? = nil) -> MaintenanceChipDisplay {
        MaintenanceChipDisplay.fold(window: w, readError: readError, takeBackError: takeBackError, now: at(now))
    }

    func testFormatMinutes() {
        XCTAssertEqual(MaintenanceChipDisplay.formatMinutes(42), "42 min")
        XCTAssertEqual(MaintenanceChipDisplay.formatMinutes(77), "1 h 17")
        XCTAssertEqual(MaintenanceChipDisplay.formatMinutes(65), "1 h 05")
        XCTAssertEqual(MaintenanceChipDisplay.formatMinutes(60), "1 h 00")
        XCTAssertEqual(MaintenanceChipDisplay.formatMinutes(0), "< 1 min")
    }

    func testFoldRunningLines() throws {
        let w = try MaintenanceFixtures.window("running")
        guard case .running(let c) = fold(w, now: 1792663260) else { return XCTFail("running expected") }
        XCTAssertEqual(c.lines[0], "Maintenance · Re-index the project · 42 min")
        XCTAssertEqual(c.lines[1], "Ensuite : Scheduled backups 20 min · Index sync 15 min — total 1 h 17")
        XCTAssertEqual(c.dot, .green)
        XCTAssertEqual(c.progress, 0, accuracy: 0.001)
        XCTAssertEqual(c.lines.count, 2)
    }

    func testFoldPassageFor10s() throws {
        let w = try MaintenanceFixtures.window("passage")
        guard case .passage(let c) = fold(w, now: 1792665645) else { return XCTFail("passage expected") }
        XCTAssertEqual(c.lines[0], "Maintenance · ✓ Re-index the project → Scheduled backups 20 min")
        XCTAssertEqual(c.lines[1], "Ensuite : Index sync 15 min — reste 35 min sur 1 h 17")
        XCTAssertEqual(c.dot, .amber)
        // G3: back to the plain running line 10 s after passage.at
        guard case .running(let after) = fold(w, now: 1792665640 + 10) else { return XCTFail("running expected at +10 s") }
        XCTAssertEqual(after.lines[0], "Maintenance · Scheduled backups · 20 min")
        XCTAssertEqual(after.dot, .green)
        guard case .passage = fold(w, now: 1792665640 + 9.9) else { return XCTFail("passage expected just before +10 s") }
    }

    func testFoldDoneSummary() throws {
        let w = try MaintenanceFixtures.window("done")
        guard case .done(let c) = fold(w, now: 1792667800) else { return XCTFail("done expected") }
        // The total is the sum of the minutes shown per session (40 + 20 + 16), never of the raw seconds.
        XCTAssertEqual(c.lines[0], "Maintenance terminée · 3 sessions servies · 1 h 16")
        XCTAssertEqual(c.lines[1], "Re-index the project 40 min · Scheduled backups 20 min · Index sync 16 min — demandé 1 h 17")
        XCTAssertEqual(c.dot, .blue)
        XCTAssertEqual(c.progress, 1)
    }

    func testFoldOverrunShowsDepasse() throws {
        // until = 1792665780 ; the heartbeat is refreshed so the stale rule does not hide the overrun
        let now = 1792665780.0 + 190
        let w = try MaintenanceFixtures.window("running", replacing: [("\"updated\": 1792664100", "\"updated\": \(Int(now))")])
        guard case .running(let c) = fold(w, now: now) else { return XCTFail("running expected") }
        XCTAssertEqual(c.lines[0], "Maintenance · Re-index the project · dépassé +3 min")
        XCTAssertEqual(c.dot, .amber)
    }

    func testFoldStaleHeartbeatIsFailed() throws {
        let w = try MaintenanceFixtures.window("running")
        guard case .running = fold(w, now: 1792664100 + 30) else { return XCTFail("30 s is still alive") }
        XCTAssertEqual(fold(w, now: 1792664100 + 31), .failed("la fenêtre ne répond plus", warnings: []))
        // done and failed files are never "stale"
        let d = try MaintenanceFixtures.window("done")
        guard case .done = fold(d, now: 1792667760 + 3600) else { return XCTFail("done stays done") }
    }

    func testFoldFailedFixtureShowsCause() throws {
        let w = try MaintenanceFixtures.window("failed")
        let d = fold(w, now: 1792664500)
        XCTAssertEqual(d, .failed("la fenêtre ne répond plus (son processus s'est arrêté)", served: "Servi : Re-index the project 19 min (tour interrompu)", warnings: []))
        XCTAssertEqual(d.lines, ["Maintenance — la fenêtre ne répond plus (son processus s'est arrêté)",
                                 "Servi : Re-index the project 19 min (tour interrompu)"])
        XCTAssertEqual(d.dot, .red)
    }

    func testFoldFailedWithoutServedHasNoSecondLine() throws {
        let w = try MaintenanceFixtures.window("running")
        XCTAssertEqual(fold(w, now: 1792664100 + 31).lines, ["Maintenance — la fenêtre ne répond plus"])
    }

    func testFoldWarningsThirdLine() throws {
        let w = try MaintenanceFixtures.window("running", replacing: [("\"warnings\": []", "\"warnings\": [\"pigtail injoignable : ordre manuel\"]")])
        guard case .running(let c) = fold(w, now: 1792663260) else { return XCTFail("running expected") }
        XCTAssertEqual(c.lines.count, 3)
        XCTAssertEqual(c.lines[2], "pigtail injoignable : ordre manuel")
        XCTAssertEqual(c.warnings, ["pigtail injoignable : ordre manuel"])
    }

    func testTakeBackErrorShownUntilNextClick() throws {
        let w = try MaintenanceFixtures.window("running")
        XCTAssertEqual(fold(w, now: 1792663260, takeBackError: "AutoRaise non rétabli"),
                       .failed("AutoRaise non rétabli", warnings: []))
        // and even if the file is gone: the failure must not vanish silently
        XCTAssertEqual(fold(nil, now: 1792663260, takeBackError: "AutoRaise non rétabli"),
                       .failed("AutoRaise non rétabli", warnings: []))
    }

    func testMissingFileIsHidden() {
        XCTAssertEqual(fold(nil, now: 1), .hidden)
    }

    func testUnreadableFileIsFailed() {
        XCTAssertEqual(fold(nil, now: 1, readError: true), .failed("état de la fenêtre illisible", warnings: []))
    }
}
