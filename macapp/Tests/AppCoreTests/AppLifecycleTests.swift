import XCTest
@testable import AppCore

final class AppLifecycleTests: XCTestCase {
    func testClosingTheLastWindowKeepsTheAppRunning() {
        XCTAssertFalse(AppLifecycle.terminatesAfterLastWindowClosed)
    }
}
