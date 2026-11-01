import XCTest
@testable import MenuBarCore

private actor StubUsage: UsageReading {
    private var next: Result<UsageOverview, Error>
    init(_ r: Result<UsageOverview, Error>) { next = r }
    func set(_ r: Result<UsageOverview, Error>) { next = r }
    func usageOverview() async throws -> UsageOverview { try next.get() }
}

private func overview(_ label: String = "m") -> UsageOverview {
    UsageOverview(generatedAt: Date(timeIntervalSince1970: 1_800_000_000),
                  accounts: [UsageAccount(accountUUID: "u-\(label)", label: label)],
                  verdict: UsageVerdict(state: "ok"), nextSession: [])
}

@MainActor
final class UsageMonitorTests: XCTestCase {
    func testLoadingThenReady() async {
        let stub = StubUsage(.success(overview()))
        let m = UsageMonitor(client: stub)
        XCTAssertEqual(m.display.state, .loading)
        XCTAssertNil(m.display.overview)
        await m.poll()
        XCTAssertEqual(m.display.state, .ready)
        XCTAssertEqual(m.display.overview, overview())
        XCTAssertFalse(m.display.readFailed)
    }

    func testFailureKeepsLastOverview() async {
        let stub = StubUsage(.success(overview("i")))
        let m = UsageMonitor(client: stub)
        await m.poll()
        await stub.set(.failure(URLError(.cannotConnectToHost)))
        await m.poll()
        XCTAssertEqual(m.display.state, .ready, "a failure after a success freezes, it never empties")
        XCTAssertEqual(m.display.overview, overview("i"))
        XCTAssertTrue(m.display.readFailed)
        await stub.set(.success(overview("j")))
        await m.poll()
        XCTAssertFalse(m.display.readFailed, "the next success clears the flag")
        XCTAssertEqual(m.display.overview, overview("j"))
    }

    func testNeverReadFailureIsUnreachable() async {
        let m = UsageMonitor(client: StubUsage(.failure(URLError(.cannotConnectToHost))))
        await m.poll()
        XCTAssertEqual(m.display.state, .unreachable)
        XCTAssertNil(m.display.overview)
        XCTAssertTrue(m.display.readFailed)
    }

    func testDetailExpandedSurvivesPolls() async {
        let stub = StubUsage(.success(overview()))
        let m = UsageMonitor(client: stub)
        XCTAssertFalse(m.detailExpanded)
        m.detailExpanded = true
        await m.poll()
        await stub.set(.failure(URLError(.timedOut)))
        await m.poll()
        m.stopPolling()
        XCTAssertTrue(m.detailExpanded, "I9: kept across polls, failures and stopPolling")
    }
}
