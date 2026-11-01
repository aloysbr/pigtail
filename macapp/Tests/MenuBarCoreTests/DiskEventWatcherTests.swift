import Foundation
import XCTest
@testable import MenuBarCore

private final class ScriptedDiskSource: DiskEventSource, @unchecked Sendable {
    private let lock = NSLock()
    private var scripts: [[DaemonStreamItem]]
    private var calls = 0
    private var mute: [AsyncThrowingStream<DaemonStreamItem, Error>.Continuation] = []
    init(_ scripts: [[DaemonStreamItem]]) { self.scripts = scripts }
    var connections: Int { lock.withLock { calls } }

    func diskEvents() -> AsyncThrowingStream<DaemonStreamItem, Error> {
        let next: [DaemonStreamItem]? = lock.withLock { calls += 1; return scripts.isEmpty ? nil : scripts.removeFirst() }
        guard let next else { return AsyncThrowingStream { c in self.lock.withLock { self.mute.append(c) } } }
        return AsyncThrowingStream { c in next.forEach { c.yield($0) }; c.finish() }
    }

    func awaitConnections(_ n: Int, file: StaticString = #filePath, line: UInt = #line) async {
        let deadline = Date().addingTimeInterval(2)
        while connections < n {
            if Date() > deadline { XCTFail("diskEvents() called \(connections) times, want \(n)", file: file, line: line); return }
            try? await Task.sleep(for: .milliseconds(1))
        }
    }
}

@MainActor
private final class RecordingHandler: DiskEventHandling {
    private(set) var reconciles = 0
    private(set) var handled: [String] = []
    func reconcile() async { reconciles += 1 }
    func handle(_ event: SSEEvent) { handled.append(event.type) }
}

private final class Delays: @unchecked Sendable {
    private let lock = NSLock()
    private var d: [TimeInterval] = []
    var all: [TimeInterval] { lock.withLock { d } }
    func record(_ x: TimeInterval) { lock.withLock { d.append(x) } }
}

@MainActor
final class DiskEventWatcherTests: XCTestCase {
    private func event(_ type: String) -> DaemonStreamItem {
        .event(SSEEvent(id: nil, type: type, data: #"{"data":{"free_bytes":1,"floor_bytes":2}}"#))
    }

    func testConnectionReconcilesAndDiskEventsAreHandled() async {
        let src = ScriptedDiskSource([[.connected, event("disk.low"), event("disk.ok"), event("daemon.started")]])
        let target = RecordingHandler()
        let delays = Delays()
        let w = DiskEventWatcher(source: src, target: target, sleep: { delays.record($0) }, log: { _ in })
        w.start()
        await src.awaitConnections(2) // the closed stream is retried
        XCTAssertEqual(target.reconciles, 1, "connected re-reads GET /alerts")
        XCTAssertEqual(target.handled, ["disk.low", "disk.ok"], "other events are ignored")
        XCTAssertEqual(delays.all, [1])
        let t = w.runTask
        w.stop()
        await t?.value
    }

    func testLostStreamIsRetriedWithBackoffThenReconciles() async {
        let src = ScriptedDiskSource([[], [], [.connected]])
        let target = RecordingHandler()
        let delays = Delays()
        let w = DiskEventWatcher(source: src, target: target, sleep: { delays.record($0) }, log: { _ in })
        w.start()
        await src.awaitConnections(4)
        XCTAssertEqual(delays.all, [1, 2, 1])
        XCTAssertEqual(target.reconciles, 1)
        let t = w.runTask
        w.stop()
        await t?.value
    }
}

private final class FakeReader: DiskAlertReading, @unchecked Sendable {
    var answer: Result<DiskAlert?, Error>
    init(_ a: Result<DiskAlert?, Error>) { answer = a }
    func diskAlert() async throws -> DiskAlert? { try answer.get() }
}

@MainActor
final class DiskMonitorTests: XCTestCase {
    private let gib: UInt64 = 1 << 30

    func testStartupWithAnOpenAlertPostsAndAClosedOneRemoves() async {
        var actions: [DiskNotificationAction] = []
        let reader = FakeReader(.success(DiskAlert(freeBytes: 12 * gib, floorBytes: 20 * gib)))
        let m = DiskMonitor(reader: reader, onAction: { actions.append($0) })
        await m.reconcile()
        await m.reconcile()
        XCTAssertEqual(actions.count, 1)
        reader.answer = .success(nil)
        await m.reconcile()
        XCTAssertEqual(actions.last, .remove(id: DiskNotificationPolicy.notificationID))
    }

    func testAFailedReadChangesNothing() async {
        var actions: [DiskNotificationAction] = []
        let m = DiskMonitor(reader: FakeReader(.failure(URLError(.cannotConnectToHost))), onAction: { actions.append($0) })
        await m.reconcile()
        XCTAssertTrue(actions.isEmpty)
    }

    func testEventsDriveThePolicy() {
        var actions: [DiskNotificationAction] = []
        let m = DiskMonitor(reader: FakeReader(.success(nil)), onAction: { actions.append($0) })
        m.handle(SSEEvent(id: nil, type: "disk.low", data: #"{"type":"disk.low","data":{"free_bytes":33285996544,"threshold_bytes":42949672960,"floor_bytes":21474836480}}"#))
        m.handle(SSEEvent(id: nil, type: "disk.low", data: #"{"data":{"free_bytes":33285996544,"floor_bytes":21474836480}}"#))
        m.handle(SSEEvent(id: nil, type: "disk.ok", data: "{}"))
        XCTAssertEqual(actions, [
            .post(id: DiskNotificationPolicy.notificationID, title: DiskNotificationPolicy.title,
                  body: "Disque presque plein : 31 Go libres. pigtail fait attendre les tests sous 20 Go."),
            .remove(id: DiskNotificationPolicy.notificationID),
        ])
    }

    func testAnUnreadableLowEventStillNotifiesWithoutNumbers() {
        var actions: [DiskNotificationAction] = []
        let m = DiskMonitor(reader: FakeReader(.success(nil)), onAction: { actions.append($0) })
        m.handle(SSEEvent(id: nil, type: "disk.low", data: "not json"))
        XCTAssertEqual(actions.count, 1, "a crossing must be shown even when its payload cannot be read")
    }
}
