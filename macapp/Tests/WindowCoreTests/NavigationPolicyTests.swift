import XCTest
@testable import WindowCore

final class NavigationPolicyTests: XCTestCase {
    private let policy = NavigationPolicy(origin: DaemonAddress(httpAddr: "127.0.0.1:7800")!)

    private func decide(_ s: String?, main: Bool = true, newWindow: Bool = false, kind: NavigationKind = .other) -> NavigationDecision {
        policy.decide(NavigationRequest(url: s.flatMap(URL.init(string:)), isMainFrame: main, opensNewWindow: newWindow, kind: kind))
    }

    func testRootLoadAllowedReloadGated() {
        XCTAssertEqual(decide("http://127.0.0.1:7800/"), .allow)
        XCTAssertEqual(decide("http://127.0.0.1:7800/", kind: .reload), .gateSession)
        XCTAssertEqual(decide("http://127.0.0.1:7800", kind: .reload), .gateSession)
    }

    func testLoginAllowed() {
        XCTAssertEqual(decide("http://127.0.0.1:7800/web/login?code=x"), .allow)
    }

    func testForeignOpensOutside() {
        for s in ["https://example.com/x", "http://127.0.0.1:9999/", "http://localhost:7800/", "obsidian://open?path=a", "mailto:a@b.c"] {
            XCTAssertEqual(decide(s, kind: .linkActivated), .openExternally(URL(string: s)!), s)
        }
        XCTAssertEqual(decide("https://example.com/x", newWindow: true), .openExternally(URL(string: "https://example.com/x")!))
    }

    func testSameOriginStays() {
        XCTAssertEqual(decide("http://127.0.0.1:7800/assets/x.js", newWindow: true), .allow)
        for k in [NavigationKind.linkActivated, .backForward, .other] {
            XCTAssertEqual(decide("http://127.0.0.1:7800/?subject=x", kind: k), .allow, "\(k)")
        }
        XCTAssertEqual(decide("http://127.0.0.1:7800/assets/a.css", kind: .reload), .allow)
    }

    func testPolicyFollowsNewOrigin() {
        let p = NavigationPolicy(origin: DaemonAddress(httpAddr: "127.0.0.1:7811")!)
        XCTAssertEqual(p.decide(NavigationRequest(url: URL(string: "http://127.0.0.1:7811/"))), .allow)
        XCTAssertEqual(p.decide(NavigationRequest(url: URL(string: "http://127.0.0.1:7800/"), kind: .linkActivated)),
                       .openExternally(URL(string: "http://127.0.0.1:7800/")!))
    }

    func testSpecialSchemes() {
        XCTAssertEqual(decide("about:blank"), .allow)
        XCTAssertEqual(decide("file:///etc/passwd"), .cancel)
        XCTAssertEqual(decide("javascript:alert(1)"), .cancel)
        XCTAssertEqual(decide("data:text/html,hi"), .cancel)
        XCTAssertEqual(decide(nil), .cancel)
    }

    func testForeignSubframeIsCancelled() {
        XCTAssertEqual(decide("https://example.com/frame", main: false), .cancel)
    }

    func testResponsePolicy() {
        let o = DaemonAddress(httpAddr: "127.0.0.1:7800")!
        let login = URL(string: "http://127.0.0.1:7800/web/login?code=x")
        XCTAssertEqual(ResponsePolicy.decide(url: login, status: 410, isMainFrame: true, origin: o), .loginRefused)
        XCTAssertEqual(ResponsePolicy.decide(url: login, status: 410, isMainFrame: false, origin: o), .allow)
        XCTAssertEqual(ResponsePolicy.decide(url: login, status: 303, isMainFrame: true, origin: o), .allow)
        XCTAssertEqual(ResponsePolicy.decide(url: URL(string: "http://127.0.0.1:7800/"), status: 410, isMainFrame: true, origin: o), .allow)
    }
}
