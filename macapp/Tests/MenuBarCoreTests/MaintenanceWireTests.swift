import XCTest
@testable import MenuBarCore

/// Fixtures are byte copies of the maintenance-window tool fixtures (contract C1). They are read
/// through `#filePath`, not declared as resources: the Makefile's app target copies no SwiftPM bundle.
enum MaintenanceFixtures {
    static func url(_ name: String) -> URL {
        URL(fileURLWithPath: #filePath).deletingLastPathComponent()
            .appendingPathComponent("Fixtures/window-\(name).json")
    }
    static func data(_ name: String) throws -> Data { try Data(contentsOf: url(name)) }
    static func text(_ name: String) throws -> String { String(decoding: try data(name), as: UTF8.self) }
    static func window(_ name: String, replacing pairs: [(String, String)] = []) throws -> MaintenanceWindow {
        var t = try text(name)
        for (a, b) in pairs {
            precondition(t.contains(a), "fixture \(name) lacks \(a)")
            t = t.replacingOccurrences(of: a, with: b)
        }
        return try MaintenanceWindow.decode(Data(t.utf8))
    }
}

final class MaintenanceWireTests: XCTestCase {
    func testDecodesEachFixture() throws {
        let running = try MaintenanceWindow.decode(MaintenanceFixtures.data("running"))
        XCTAssertEqual(running.state, "running")
        XCTAssertEqual(running.serving?.label, "Re-index the project")
        XCTAssertEqual(running.serving?.needMin, 42)
        XCTAssertEqual(running.queue.map(\.label), ["Scheduled backups", "Index sync"])
        XCTAssertEqual(running.totalMin, 77)
        XCTAssertNil(running.passage)

        let passage = try MaintenanceWindow.decode(MaintenanceFixtures.data("passage"))
        XCTAssertEqual(passage.passage?.from, "Re-index the project")
        XCTAssertEqual(passage.passage?.to, "Scheduled backups")
        XCTAssertEqual(passage.served.first?.tookS, 2380)

        let done = try MaintenanceWindow.decode(MaintenanceFixtures.data("done"))
        XCTAssertEqual(done.state, "done")
        XCTAssertEqual(done.endedBy, "queue-empty")
        XCTAssertEqual(done.served.count, 3)
        XCTAssertNil(done.serving)

        let failed = try MaintenanceWindow.decode(MaintenanceFixtures.data("failed"))
        XCTAssertEqual(failed.state, "failed")
        XCTAssertEqual(failed.failure, "la fenêtre ne répond plus (son processus s'est arrêté)")
        XCTAssertEqual(failed.served.first?.interrupted, true)
    }

    func testRejectsUnknownVersion() throws {
        let t = try MaintenanceFixtures.text("running").replacingOccurrences(of: "\"version\": 1", with: "\"version\": 2")
        XCTAssertThrowsError(try MaintenanceWindow.decode(Data(t.utf8)))
        XCTAssertThrowsError(try MaintenanceWindow.decode(Data("not json".utf8)))
    }
}
