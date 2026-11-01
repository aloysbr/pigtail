import XCTest
@testable import WindowCore

final class DaemonAddressTests: XCTestCase {
    func testLoopbackAddress() throws {
        let a = try XCTUnwrap(DaemonAddress(httpAddr: "127.0.0.1:7800"))
        XCTAssertEqual(a.httpAddr, "127.0.0.1:7800")
        XCTAssertEqual(a.origin.absoluteString, "http://127.0.0.1:7800")
        XCTAssertEqual(a.url(path: "/assets/_probe").absoluteString, "http://127.0.0.1:7800/assets/_probe")
        XCTAssertEqual(a.loginURL(code: "abc").absoluteString, "http://127.0.0.1:7800/web/login?code=abc")
    }

    func testLoginCodeIsPercentEncoded() throws {
        let a = try XCTUnwrap(DaemonAddress(httpAddr: "127.0.0.1:7800"))
        XCTAssertEqual(a.loginURL(code: "a+b/c=d&e f").absoluteString,
                       "http://127.0.0.1:7800/web/login?code=a%2Bb%2Fc%3Dd%26e%20f")
    }

    func testLocalhostKeptAsIs() throws {
        // The daemon's host is kept: no rewrite to 127.0.0.1 (the cookie is host-only).
        let a = try XCTUnwrap(DaemonAddress(httpAddr: "localhost:7800"))
        XCTAssertEqual(a.origin.absoluteString, "http://localhost:7800")
    }

    func testRejected() {
        for bad in ["0.0.0.0:7800", "example.com:80", "127.0.0.1", "", "127.0.0.1:", "127.0.0.1:0", "127.0.0.1:99999", "127.0.0.1:78a0", "127.0.0.1:7800/x"] {
            XCTAssertNil(DaemonAddress(httpAddr: bad), bad)
        }
    }

    private func freshDefaults() -> UserDefaults {
        let name = "WindowCoreTests.\(UUID().uuidString)"
        let d = UserDefaults(suiteName: name)!
        d.removePersistentDomain(forName: name)
        addTeardownBlock { UserDefaults(suiteName: name)?.removePersistentDomain(forName: name) }
        return d
    }

    func testStoreDefaultsBeforeAnyCode() {
        let store = AddressStore(defaults: freshDefaults())
        XCTAssertEqual(store.load().httpAddr, "127.0.0.1:7800")
    }

    func testStoreSaveThenLoad() throws {
        let d = freshDefaults()
        try AddressStore(defaults: d).save(XCTUnwrap(DaemonAddress(httpAddr: "127.0.0.1:7811")))
        XCTAssertEqual(AddressStore(defaults: d).load().httpAddr, "127.0.0.1:7811")
    }
}
