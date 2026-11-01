import XCTest
@testable import MenuBarCore

/// The base-URL seam. The resolution is a pure function of an environment
/// dictionary, so these tests never touch the process's own environment —
/// `ProcessInfo.processInfo.environment` is a snapshot shared by the whole
/// test bundle, and a test that mutates it leaks into every other one.
final class DaemonClientBaseURLTests: XCTestCase {

    private let loopback = URL(string: "http://127.0.0.1:7800")!

    /// The property that matters most: with the variable unset, nothing
    /// whatsoever changes.
    func testAnEmptyEnvironmentResolvesToTheHardcodedLoopback() {
        XCTAssertEqual(DaemonClient.resolveBaseURL(environment: [:]), loopback)
    }

    func testTheVariableOverridesTheLoopback() {
        let resolved = DaemonClient.resolveBaseURL(
            environment: ["PIGTAIL_APP_BASE_URL": "http://127.0.0.1:7899"])
        XCTAssertEqual(resolved, URL(string: "http://127.0.0.1:7899")!)
    }

    /// A malformed value falls back rather than crashing or pointing the app
    /// at nothing: the seam is a debugging aid, and a debugging aid that can
    /// brick the real bar on a typo is not one.
    func testAMalformedValueFallsBackToTheLoopback() {
        for raw in ["", "   ", "not a url", "/just/a/path", "7899"] {
            XCTAssertEqual(DaemonClient.resolveBaseURL(environment: ["PIGTAIL_APP_BASE_URL": raw]),
                           loopback, "\(raw.debugDescription) must not become the base URL")
        }
    }

    /// The default argument really is the resolver, so an override reaches a
    /// client built the way the app builds it.
    func testTheClientPicksUpAResolvedOverride() {
        let client = DaemonClient(baseURL: DaemonClient.resolveBaseURL(
            environment: ["PIGTAIL_APP_BASE_URL": "http://127.0.0.1:7899"]), token: nil)
        XCTAssertEqual(client.baseURL, URL(string: "http://127.0.0.1:7899")!)
    }
}
