import XCTest
@testable import AppCore

final class WindowKeyRequestTests: XCTestCase {
    func testShowAsksForKey() {
        var r = WindowKeyRequest()
        XCTAssertEqual(r.handle(.show), .makeKey)
    }

    func testAppBecameActiveAfterShowAsksAgain() {
        var r = WindowKeyRequest()
        _ = r.handle(.show)
        XCTAssertEqual(r.handle(.appBecameActive), .makeKey)
    }

    func testDeminiaturizedAfterShowAsksAgain() {
        var r = WindowKeyRequest()
        _ = r.handle(.show)
        XCTAssertEqual(r.handle(.deminiaturized), .makeKey)
    }

    func testNothingDueOnceKey() {
        var r = WindowKeyRequest()
        _ = r.handle(.show)
        _ = r.handle(.becameKey)
        XCTAssertEqual(r.handle(.appBecameActive), .none)
    }

    func testNothingDueAfterClose() {
        var r = WindowKeyRequest()
        _ = r.handle(.show)
        _ = r.handle(.closed)
        XCTAssertEqual(r.handle(.deminiaturized), .none)
    }

    func testNothingDueWithoutShow() {
        var r = WindowKeyRequest()
        XCTAssertEqual(r.handle(.appBecameActive), .none)
    }
}
