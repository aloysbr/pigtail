import XCTest
@testable import MenuBarCore

private final class FakeNoticeClient: CheckNoticeReading, @unchecked Sendable {
    var answers: [Result<CheckNoticeResponse, Error>]
    init(_ a: [Result<CheckNoticeResponse, Error>]) { answers = a }
    func cmuxCheckNotice() async throws -> CheckNoticeResponse {
        try answers.isEmpty ? CheckNoticeResponse(launchPID: 0, line: "") : answers.removeFirst().get()
    }
}

@MainActor
final class CheckNoticeMonitorTests: XCTestCase {
    /// D2: one notification per notice — none for an empty line, one for a
    /// line, never twice for the same launch, even after a bar restart.
    func testPostsOncePerLaunch() async {
        let ledger = InMemoryNoticeLedger()
        var posted: [String] = []
        let line = CheckNoticeResponse(launchPID: 42, line: "14 relancées · PLG attend ta réponse — pigtail cmux check --last")
        let m = CheckNoticeMonitor(client: FakeNoticeClient([.success(.init(launchPID: 41, line: "")), .success(line), .success(line)]),
                                   ledger: ledger, onNotice: { posted.append($0.body) })
        await m.poll(); await m.poll(); await m.poll()
        XCTAssertEqual(posted, [line.line])
        let restarted = CheckNoticeMonitor(client: FakeNoticeClient([.success(line)]), ledger: ledger, onNotice: { posted.append($0.body) })
        await restarted.poll()
        XCTAssertEqual(posted.count, 1, "a bar restart must not post the same launch again")
    }

    /// A failed read posts nothing and keeps going (freeze, don't clear).
    func testReadFailurePostsNothing() async {
        var posted = 0
        let m = CheckNoticeMonitor(client: FakeNoticeClient([.failure(URLError(.cannotConnectToHost))]),
                                   ledger: InMemoryNoticeLedger(), onNotice: { _ in posted += 1 })
        await m.poll()
        XCTAssertEqual(posted, 0)
    }
}
