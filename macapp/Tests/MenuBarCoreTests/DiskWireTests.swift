import XCTest
@testable import MenuBarCore

final class DiskWireTests: XCTestCase {
    func testPicksTheDiskAlertOutOfGETAlerts() throws {
        let json = #"""
        {"data":{"alerts":[
          {"id":"r1","kind":"respawn","state":"failed","scope":"x","detail":"d","created_at":"2026-10-31T10:00:00Z","age_seconds":5},
          {"id":"disk-low:1","kind":"disk","state":"low","detail":"31 GB free on disk","free_bytes":33285996544,"floor_bytes":21474836480,"created_at":"2026-10-31T10:00:00Z","age_seconds":5}
        ],"read_at":"2026-10-31T10:00:05Z"}}
        """#
        XCTAssertEqual(try DiskAlert.firstDiskAlert(inAlertsBody: Data(json.utf8)),
                       DiskAlert(freeBytes: 33285996544, floorBytes: 21474836480))
    }

    func testNoDiskAlertIsNil() throws {
        let json = #"{"data":{"alerts":[],"read_at":"2026-10-31T10:00:05Z"}}"#
        XCTAssertNil(try DiskAlert.firstDiskAlert(inAlertsBody: Data(json.utf8)))
    }

    func testADiskAlertWithoutNumbersStillCounts() throws {
        let json = #"{"data":{"alerts":[{"id":"disk-low:1","kind":"disk","state":"low","detail":"low disk space"}]}}"#
        XCTAssertEqual(try DiskAlert.firstDiskAlert(inAlertsBody: Data(json.utf8)), DiskAlert(freeBytes: 0, floorBytes: 0))
    }
}
