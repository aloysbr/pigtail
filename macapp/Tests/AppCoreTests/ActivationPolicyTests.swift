import XCTest
@testable import AppCore

final class ActivationPolicyTests: XCTestCase {
    func testNoWindowIsAccessory() {
        XCTAssertEqual(ActivationPolicyDecider.policy(mainWindowOpen: false, mainWindowMiniaturized: false), .accessory)
    }
    func testVisibleWindowIsRegular() {
        XCTAssertEqual(ActivationPolicyDecider.policy(mainWindowOpen: true, mainWindowMiniaturized: false), .regular)
    }
    func testMiniaturizedWindowKeepsTheDockIcon() {
        XCTAssertEqual(ActivationPolicyDecider.policy(mainWindowOpen: true, mainWindowMiniaturized: true), .regular)
    }
}
