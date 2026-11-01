import XCTest
@testable import AppCore

final class PanelActivationTests: XCTestCase {
    func testActivatesWhenNoWindowIsVisible() { XCTAssertTrue(PanelActivation.shouldActivateApp(mainWindowVisible: false)) }
    func testDoesNotRaiseAVisibleWindow() { XCTAssertFalse(PanelActivation.shouldActivateApp(mainWindowVisible: true)) }
}
