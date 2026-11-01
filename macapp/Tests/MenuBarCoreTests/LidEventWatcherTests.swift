import Foundation
import XCTest
@testable import MenuBarCore

/// A `LidEventSource` that plays scripted connections, each fully buffered at
/// the `lidEvents()` call (end included). Scripts exhausted → a mute stream
/// that never ends until its consumer is cancelled: the watcher then waits
/// there instead of spinning through reconnections.
final class ScriptedSource: LidEventSource, @unchecked Sendable {
    enum End { case finish, fail(Error) }
    struct Connection { let items: [DaemonStreamItem]; let end: End }

    private let lock = NSLock()
    private var scripts: [Connection]
    private var calls = 0
    // Mute streams keep their continuation alive: never finished, only cancelled.
    private var mute: [AsyncThrowingStream<DaemonStreamItem, Error>.Continuation] = []

    init(_ scripts: [Connection]) { self.scripts = scripts }

    var connections: Int { lock.withLock { calls } }

    func lidEvents() -> AsyncThrowingStream<DaemonStreamItem, Error> {
        let next: Connection? = lock.withLock {
            calls += 1
            return scripts.isEmpty ? nil : scripts.removeFirst()
        }
        guard let next else {
            return AsyncThrowingStream { c in self.lock.withLock { self.mute.append(c) } }
        }
        return AsyncThrowingStream { c in
            for item in next.items { c.yield(item) }
            switch next.end {
            case .finish: c.finish()
            case .fail(let e): c.finish(throwing: e)
            }
        }
    }

    /// Returns once `lidEvents()` has been called n times (XCTFail after 2 s).
    func awaitConnections(_ n: Int, file: StaticString = #filePath, line: UInt = #line) async {
        let deadline = Date().addingTimeInterval(2)
        while connections < n {
            if Date() > deadline {
                XCTFail("lidEvents() called \(connections) times within 2 s, want \(n)", file: file, line: line)
                return
            }
            try? await Task.sleep(for: .milliseconds(1))
        }
    }
}

@MainActor
final class RefreshCounter: LidRefreshing {
    private(set) var count = 0
    func refreshFromEvent() { count += 1 }
}

final class DelayRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var recorded: [TimeInterval] = []
    var delays: [TimeInterval] { lock.withLock { recorded } }
    func record(_ d: TimeInterval) { lock.withLock { recorded.append(d) } }
}

@MainActor
final class LidEventWatcherTests: XCTestCase {
    private var logs: [String] = []
    private let delays = DelayRecorder()

    override func setUp() async throws {
        logs = []
    }

    private func makeWatcher(_ source: ScriptedSource, target: LidRefreshing) -> LidEventWatcher {
        let delays = self.delays
        return LidEventWatcher(source: source, target: target,
                               sleep: { delays.record($0) },
                               log: { [weak self] in self?.logs.append($0) })
    }

    private func stop(_ w: LidEventWatcher) async {
        let t = w.runTask
        w.stop()
        await t?.value
    }

    private static let F = ScriptedSource.End.fail(URLError(.cannotConnectToHost))
    private static func lidEvent(_ reason: String) -> DaemonStreamItem {
        .event(SSEEvent(id: nil, type: "lid.changed", data: #"{"data":{"on":true,"reason":"\#(reason)"}}"#))
    }
    private static func lost(_ n: Int) -> String { " — retry in \(n) s (60 s poll still running)" }
    private static let closedByDaemon = { (n: Int) in "events stream lost: closed by daemon" + lost(n) }

    private func assertLost(_ line: String, retry n: Int, file: StaticString = #filePath, line l: UInt = #line) {
        XCTAssertTrue(line.hasPrefix("events stream lost: "), line, file: file, line: l)
        XCTAssertTrue(line.hasSuffix(Self.lost(n)), line, file: file, line: l)
    }

    func testConnectedAndLidEventEachTriggerARefresh() async {
        let src = ScriptedSource([.init(items: [.connected, Self.lidEvent("on")], end: .finish)])
        let target = RefreshCounter()
        let w = makeWatcher(src, target: target)
        w.start()
        await src.awaitConnections(2)
        XCTAssertEqual(target.count, 2)
        XCTAssertEqual(logs, ["events stream connected", "lid event (on) → re-read", Self.closedByDaemon(1)])
        XCTAssertEqual(delays.delays, [1])
        await stop(w)
    }

    func testNonLidEventIsIgnored() async {
        let src = ScriptedSource([.init(items: [.connected, .event(SSEEvent(id: nil, type: "daemon.started", data: "{}"))],
                                        end: .finish)])
        let target = RefreshCounter()
        let w = makeWatcher(src, target: target)
        w.start()
        await src.awaitConnections(2)
        XCTAssertEqual(target.count, 1)
        XCTAssertEqual(logs, ["events stream connected", Self.closedByDaemon(1)])
        XCTAssertEqual(delays.delays, [1])
        await stop(w)
    }

    func testStreamLossLogsAndRetriesWithBackoff() async {
        let src = ScriptedSource([.init(items: [], end: Self.F), .init(items: [], end: Self.F),
                                  .init(items: [], end: Self.F), .init(items: [.connected], end: .finish)])
        let target = RefreshCounter()
        let w = makeWatcher(src, target: target)
        w.start()
        await src.awaitConnections(5)
        XCTAssertEqual(delays.delays, [1, 2, 5, 1])
        XCTAssertEqual(logs.count, 5, "\(logs)")
        if logs.count == 5 {
            assertLost(logs[0], retry: 1)
            assertLost(logs[1], retry: 2)
            assertLost(logs[2], retry: 5)
            XCTAssertEqual(logs[3], "events stream connected")
            XCTAssertEqual(logs[4], Self.closedByDaemon(1))
        }
        XCTAssertEqual(target.count, 1)
        await stop(w)
    }

    func testBackoffCapsAt30AndResetsAfterAConnection() async {
        var scripts = Array(repeating: ScriptedSource.Connection(items: [], end: Self.F), count: 6)
        scripts.append(.init(items: [.connected], end: .finish))
        scripts.append(.init(items: [], end: Self.F))
        let src = ScriptedSource(scripts)
        let target = RefreshCounter()
        let w = makeWatcher(src, target: target)
        w.start()
        await src.awaitConnections(9)
        XCTAssertEqual(delays.delays, [1, 2, 5, 10, 30, 30, 1, 2])
        XCTAssertEqual(logs.count, 9, "\(logs)")
        if logs.count == 9 {
            for (i, n) in [1, 2, 5, 10, 30, 30].enumerated() { assertLost(logs[i], retry: n) }
            XCTAssertEqual(logs[6], "events stream connected")
            XCTAssertEqual(logs[7], Self.closedByDaemon(1))
            assertLost(logs[8], retry: 2)
        }
        XCTAssertEqual(target.count, 1)
        await stop(w)
    }

    func testNormalEndAlsoReconnects() async {
        let src = ScriptedSource([.init(items: [.connected], end: .finish)])
        let target = RefreshCounter()
        let w = makeWatcher(src, target: target)
        w.start()
        await src.awaitConnections(2)
        XCTAssertEqual(src.connections, 2)
        XCTAssertEqual(logs, ["events stream connected", Self.closedByDaemon(1)])
        XCTAssertEqual(delays.delays, [1])
        XCTAssertEqual(target.count, 1)
        await stop(w)
    }

    func testStopCancels() async {
        let src = ScriptedSource([.init(items: [.connected], end: .finish)])
        let target = RefreshCounter()
        let w = makeWatcher(src, target: target)
        w.start()
        await src.awaitConnections(2)
        let lostBefore = logs.filter { $0.hasPrefix("events stream lost") }.count
        let delaysBefore = delays.delays
        XCTAssertEqual(lostBefore, 1)
        XCTAssertEqual(delaysBefore, [1])
        await stop(w)
        XCTAssertEqual(logs.filter { $0.hasPrefix("events stream lost") }.count, lostBefore, "a stop is not a loss")
        XCTAssertEqual(delays.delays, delaysBefore)
        XCTAssertEqual(src.connections, 2)
        XCTAssertNil(w.runTask)
    }

    func testReplayBurstCostsAtMostTwoReads() async {
        let d = Date(timeIntervalSince1970: 2_000_000_000)
        let stub = StubLid(get: .success(LidResponse(sleepDisabled: true, deadline: d)))
        let monitor = LidMonitor(client: stub)            // poll not started
        await stub.holdNextGet()
        let src = ScriptedSource([.init(items: [.connected] + Array(repeating: Self.lidEvent("on"), count: 50),
                                        end: .finish)])
        let w = makeWatcher(src, target: monitor)
        w.start()
        let entered = expectation(description: "GET 1 held")
        Task { await stub.awaitGetEntry(); entered.fulfill() }
        await fulfillment(of: [entered], timeout: 2)
        await src.awaitConnections(2)
        XCTAssertEqual(logs.count, 52, "1 connected + 50 events + 1 loss")
        XCTAssertEqual(logs.first, "events stream connected")
        XCTAssertEqual(logs.filter { $0 == "lid event (on) → re-read" }.count, 50)
        XCTAssertEqual(logs.last, Self.closedByDaemon(1))
        var gets = await stub.getCalls
        XCTAssertEqual(gets, 1)
        await stub.releaseGet(.success(LidResponse(sleepDisabled: false)))
        await monitor.eventRefreshTask?.value
        gets = await stub.getCalls
        XCTAssertEqual(gets, 2, "a replayed burst costs at most two reads")
        XCTAssertEqual(monitor.display.state, .on(deadline: d))
        await stop(w)
    }
}
