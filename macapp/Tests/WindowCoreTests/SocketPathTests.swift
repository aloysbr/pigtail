import XCTest
@testable import WindowCore

final class SocketPathTests: XCTestCase {
    func testEnvironmentWins() {
        XCTAssertEqual(SocketPath.resolve(env: ["PIGTAIL_SOCKET": "/tmp/x.sock"], home: "/Users/me"), "/tmp/x.sock")
    }

    func testDefaultWhenAbsent() {
        XCTAssertEqual(SocketPath.resolve(env: [:], home: "/Users/me"), "/Users/me/.config/pigtail/pigtail.sock")
    }

    func testEmptyIsAbsent() {
        // The CLI treats an empty PIGTAIL_SOCKET as absent (cmd/pigtail/main.go socketPath).
        XCTAssertEqual(SocketPath.resolve(env: ["PIGTAIL_SOCKET": ""], home: "/Users/me"), "/Users/me/.config/pigtail/pigtail.sock")
    }
}
