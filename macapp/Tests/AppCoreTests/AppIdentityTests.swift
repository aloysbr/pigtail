import XCTest
@testable import AppCore

final class AppIdentityTests: XCTestCase {
    func testIdentity() {
        XCTAssertEqual(AppIdentity.bundleID, "sh.pigtail.app")
        XCTAssertEqual(AppIdentity.appName, "Pigtail")
        XCTAssertEqual(AppIdentity.windowAutosaveName, "PigtailMainWindow")
        XCTAssertEqual(AppIdentity.defaultHTTPAddr, "127.0.0.1:7800") // pkg/pigtail/client.go:23
        XCTAssertEqual(AppIdentity.initialContentSize.width, 1280) // D10
        XCTAssertEqual(AppIdentity.initialContentSize.height, 800)
        XCTAssertEqual(AppIdentity.minContentSize.width, 640) // D11
        XCTAssertEqual(AppIdentity.minContentSize.height, 480)
    }

    func testOnlyTheReleaseBundleAsksForNotifications() {
        XCTAssertTrue(AppIdentity.isReleaseBundle("sh.pigtail.app"))
        XCTAssertFalse(AppIdentity.isReleaseBundle("sh.pigtail.app.verify"))
        XCTAssertFalse(AppIdentity.isReleaseBundle(nil))
    }
}
