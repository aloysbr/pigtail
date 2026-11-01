import XCTest
@testable import MenuBarCore

private let noticeNow = Date(timeIntervalSince1970: 1_900_000_000)

@MainActor
private final class NoticeSink {
    var notices: [LidCutNotice] = []
}

/// D3: the monitor turns each new `last_off` it reads (GET or POST answer)
/// into one notice, through `onNotice`.
extension LidMonitorTests {

    fileprivate func monitor(_ stub: StubLid, now: Date, sink: NoticeSink) -> LidMonitor {
        LidMonitor(client: stub, now: { now }, onNotice: { sink.notices.append($0) })
    }

    fileprivate var oldCut: LidResponse {
        LidResponse(sleepDisabled: false, lastOff: LidOff(at: noticeNow.addingTimeInterval(-86_400), reason: "deadline"))
    }

    fileprivate func freshCut(at: Date) -> LidResponse {
        LidResponse(sleepDisabled: false, lastOff: LidOff(at: at, reason: "thermal critical"))
    }

    func testCutNotifiedOnce() async {
        let sink = NoticeSink()
        let stub = StubLid(get: .success(oldCut))
        let m = monitor(stub, now: noticeNow, sink: sink)
        await m.poll()
        XCTAssertTrue(sink.notices.isEmpty, "baseline")
        await stub.setGet(.success(freshCut(at: noticeNow)))
        await m.poll()
        await m.poll()   // poll + lid.changed read the same last_off
        XCTAssertEqual(sink.notices.count, 1)
        XCTAssertEqual(sink.notices.first?.body.hasPrefix("Mac en surchauffe"), true)
    }

    func testLateCutNotified() async {
        let sink = NoticeSink()
        let cutAt = noticeNow
        let stub = StubLid(get: .success(oldCut))
        let m = monitor(stub, now: cutAt.addingTimeInterval(5 * 3600), sink: sink)   // read at wake-up
        await m.poll()
        await stub.setGet(.success(freshCut(at: cutAt)))
        await m.poll()
        XCTAssertEqual(sink.notices.count, 1)
    }

    func testCutSeenThroughActionResponse() async {
        let sink = NoticeSink()
        let fresh = freshCut(at: noticeNow)
        let stub = StubLid(get: .success(oldCut), post: .success(fresh))
        let m = monitor(stub, now: noticeNow, sink: sink)
        await m.poll()
        // The re-read after the POST fails: only the POST answer carries the cut.
        await stub.setGet(.failure(URLError(.cannotConnectToHost)))
        await m.perform(.activate(hours: 1))
        XCTAssertEqual(sink.notices.count, 1)
    }

    func testCutAfterUnavailableStillNotified() async {
        let sink = NoticeSink()
        let stub = StubLid(get: .success(oldCut))
        let m = monitor(stub, now: noticeNow, sink: sink)
        await m.poll()
        await stub.setGet(.failure(DaemonHTTPError(statusCode: 501,
            body: Data(#"{"error":{"code":"LID_UNAVAILABLE","message":"x"}}"#.utf8))))
        await m.poll()
        XCTAssertEqual(m.display.state, .unavailable)
        await stub.setGet(.success(freshCut(at: noticeNow)))
        await m.poll()
        XCTAssertEqual(sink.notices.count, 1, "a 501 between two reads keeps the notice baseline")
    }

    func testGuardErrorReachesTheDisplay() async {
        let stub = StubLid(get: .success(LidResponse(sleepDisabled: true, guardError: "thermal reading failed: x")))
        let m = LidMonitor(client: stub)
        await m.poll()
        XCTAssertEqual(m.display.guardError, "thermal reading failed: x")
        await stub.setGet(.success(LidResponse(sleepDisabled: true, guardError: "")))
        await m.poll()
        XCTAssertNil(m.display.guardError, "empty → nil")
    }
}
