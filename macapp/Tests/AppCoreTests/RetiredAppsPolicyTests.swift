import XCTest
@testable import AppCore

final class RetiredAppsPolicyTests: XCTestCase {
    private let retired = ["test.retired.one", "test.retired.two"]

    func testReleaseBundleTerminatesARetiredApp() {
        XCTAssertTrue(RetiredAppsPolicy.isActive(ownBundleID: "sh.pigtail.app"))
        XCTAssertTrue(RetiredAppsPolicy.shouldTerminate(runningBundleID: "test.retired.two", ownBundleID: "sh.pigtail.app", retired: retired))
    }
    func testVerifyBuildLeavesRetiredAppsAlone() {
        XCTAssertFalse(RetiredAppsPolicy.isActive(ownBundleID: "sh.pigtail.app.verify"))
        XCTAssertFalse(RetiredAppsPolicy.shouldTerminate(runningBundleID: "test.retired.one", ownBundleID: "sh.pigtail.app.verify", retired: retired))
    }
    func testUnbundledExecutableLeavesRetiredAppsAlone() {
        XCTAssertFalse(RetiredAppsPolicy.isActive(ownBundleID: nil))
        XCTAssertFalse(RetiredAppsPolicy.shouldTerminate(runningBundleID: "test.retired.one", ownBundleID: nil, retired: retired))
    }
    func testNeverTerminatesAnotherApp() {
        XCTAssertFalse(RetiredAppsPolicy.shouldTerminate(runningBundleID: "com.apple.Safari", ownBundleID: "sh.pigtail.app", retired: retired))
        XCTAssertFalse(RetiredAppsPolicy.shouldTerminate(runningBundleID: "sh.pigtail.app", ownBundleID: "sh.pigtail.app", retired: retired))
        XCTAssertFalse(RetiredAppsPolicy.shouldTerminate(runningBundleID: nil, ownBundleID: "sh.pigtail.app", retired: retired))
    }
}

final class RetiredPidTrackerTests: XCTestCase {
    private let retired = ["sh.pigtail.bar"]

    func testOnlyRetiredPidsAreRemembered() {
        var t = RetiredPidTracker()
        let fresh = t.judge(running: [(1, "com.apple.finder"), (2, "sh.pigtail.bar")], retired: retired)
        XCTAssertEqual(fresh, [2])
        XCTAssertEqual(t.seen, [2], "an unrelated app's pid is never stored")
    }

    func testRetiredPidJudgedOnce() {
        var t = RetiredPidTracker()
        _ = t.judge(running: [(2, "sh.pigtail.bar")], retired: retired)
        XCTAssertEqual(t.judge(running: [(2, "sh.pigtail.bar"), (3, "x")], retired: retired), [])
    }

    func testGonePidIsForgotten() {
        var t = RetiredPidTracker()
        _ = t.judge(running: [(2, "sh.pigtail.bar")], retired: retired)
        _ = t.judge(running: [(3, "x")], retired: retired)
        XCTAssertEqual(t.seen, [], "a terminated pid leaves the set (pid reuse)")
        XCTAssertEqual(t.judge(running: [(2, "sh.pigtail.bar")], retired: retired), [2])
    }
}
