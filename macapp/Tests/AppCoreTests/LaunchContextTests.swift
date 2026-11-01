import XCTest
@testable import AppCore

final class LaunchContextTests: XCTestCase {
    func testUserLaunchOpensTheWindow() {
        let c = LaunchContext.decide(launchedAsLoginItem: false, arguments: ["/Applications/Pigtail.app/Contents/MacOS/Pigtail"])
        XCTAssertEqual(c, .user); XCTAssertTrue(c.opensWindow)
    }
    func testLoginItemLaunchIsBarOnly() {
        let c = LaunchContext.decide(launchedAsLoginItem: true, arguments: ["Pigtail"])
        XCTAssertEqual(c, .loginItem); XCTAssertFalse(c.opensWindow)
    }
    func testBackgroundArgumentIsBarOnly() {
        let c = LaunchContext.decide(launchedAsLoginItem: false, arguments: ["Pigtail", "--background"])
        XCTAssertEqual(c, .background); XCTAssertFalse(c.opensWindow)
    }
    func testLogLabels() {
        XCTAssertEqual(LaunchContext.loginItem.rawValue, "login-item")
    }
}
