import OSLog
import XCTest
@testable import MenuBarCore

/// D28: the bar's log lines are readable in the unified log under the
/// subsystem "sh.pigtail.app" — NSLog's interpolated text came out <private>
/// and had no subsystem to filter on.
@MainActor
final class BarLogTests: XCTestCase {
    /// Entries of this process since `since` under sh.pigtail.app, as
    /// "category|message".
    private func barEntries(since: Date) throws -> [String] {
        let store = try OSLogStore(scope: .currentProcessIdentifier)
        let entries = try store.getEntries(at: store.position(date: since),
                                           matching: NSPredicate(format: "subsystem == %@", "sh.pigtail.app"))
        return entries.compactMap { $0 as? OSLogEntryLog }.map { "\($0.category)|\($0.composedMessage)" }
    }

    func testLidReadFailureIsReadableUnderTheBarSubsystem() async throws {
        let since = Date().addingTimeInterval(-1)
        let marker = UUID().uuidString
        let m = LidMonitor(client: StubLid(get: .failure(DaemonHTTPError(statusCode: 500, body: Data(marker.utf8)))))
        await m.poll()
        let lines = try barEntries(since: since)
        XCTAssertTrue(lines.contains { $0.hasPrefix("lid|[Pigtail] lid read failed: ") },
                      "no readable lid line under sh.pigtail.app: \(lines)")
    }

    func testWatcherDefaultLogIsReadableUnderTheBarSubsystem() async throws {
        let since = Date().addingTimeInterval(-1)
        let src = ScriptedSource([.init(items: [.connected, .event(SSEEvent(
            id: nil, type: "lid.changed", data: #"{"data":{"on":true,"reason":"manual"}}"#))], end: .finish)])
        let w = LidEventWatcher(source: src, target: RefreshCounter(), sleep: { _ in })   // default log
        w.start()
        await src.awaitConnections(2)
        let t = w.runTask
        w.stop()
        await t?.value
        let lines = try barEntries(since: since)
        XCTAssertTrue(lines.contains("events|[Pigtail] events stream connected"), "\(lines)")
        XCTAssertTrue(lines.contains("events|[Pigtail] lid event (manual) → re-read"), "\(lines)")
        XCTAssertTrue(lines.contains("events|[Pigtail] events stream lost: closed by daemon — retry in 1 s (60 s poll still running)"),
                      "\(lines)")
    }
}
