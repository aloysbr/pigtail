import XCTest
@testable import MenuBarCore

final class DiskNotificationPolicyTests: XCTestCase {
    private let gib: UInt64 = 1 << 30

    private func post(_ free: UInt64, _ floor: UInt64) -> DiskNotificationAction {
        .post(id: DiskNotificationPolicy.notificationID, title: DiskNotificationPolicy.title,
              body: DiskNotificationPolicy.body(freeBytes: free, floorBytes: floor))
    }

    func testBodyQuotesTheFreeSpaceAndTheFloor() {
        XCTAssertEqual(DiskNotificationPolicy.body(freeBytes: 31 * gib, floorBytes: 20 * gib),
                       "Disque presque plein : 31 Go libres. pigtail fait attendre les tests sous 20 Go.")
    }

    /// The title must not be the first words of the body.
    func testTitleDoesNotRepeatTheBody() {
        XCTAssertEqual(DiskNotificationPolicy.title, "pigtail · disque")
        XCTAssertFalse(DiskNotificationPolicy.body(freeBytes: 31 * gib, floorBytes: 20 * gib)
            .hasPrefix(DiskNotificationPolicy.title))
    }

    func testBodyWithoutAFloorDoesNotPromiseAWait() {
        XCTAssertEqual(DiskNotificationPolicy.body(freeBytes: 31 * gib, floorBytes: 0),
                       "Disque presque plein : 31 Go libres.")
    }

    /// One notification per crossing: a second disk.low while low posts nothing.
    func testOneNotificationPerCrossing() {
        var p = DiskNotificationPolicy()
        XCTAssertEqual(p.low(freeBytes: 31 * gib, floorBytes: 20 * gib), post(31 * gib, 20 * gib))
        XCTAssertNil(p.low(freeBytes: 30 * gib, floorBytes: 20 * gib))
        XCTAssertEqual(p.ok(), .remove(id: DiskNotificationPolicy.notificationID))
        XCTAssertEqual(p.low(freeBytes: 25 * gib, floorBytes: 20 * gib), post(25 * gib, 20 * gib),
                       "a new crossing notifies again")
    }

    func testOKWithoutALowRemovesNothing() {
        var p = DiskNotificationPolicy()
        XCTAssertNil(p.ok())
    }

    /// App start or reconnection: an open alert notifies (once), a closed one
    /// withdraws a notification the app still believes shown.
    func testReconcileNotifiesAnAlertAlreadyOpenAtStart() {
        var p = DiskNotificationPolicy()
        XCTAssertEqual(p.reconcile(DiskAlert(freeBytes: 12 * gib, floorBytes: 20 * gib)), post(12 * gib, 20 * gib))
        XCTAssertNil(p.reconcile(DiskAlert(freeBytes: 12 * gib, floorBytes: 20 * gib)), "a reconnection does not repeat it")
        XCTAssertEqual(p.reconcile(nil), .remove(id: DiskNotificationPolicy.notificationID))
        XCTAssertNil(p.reconcile(nil))
    }
}
