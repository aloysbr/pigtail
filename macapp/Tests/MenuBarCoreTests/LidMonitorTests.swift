import XCTest
@testable import MenuBarCore

private func httpError(_ status: Int, code: String, message: String) -> DaemonHTTPError {
    DaemonHTTPError(statusCode: status,
                    body: Data(#"{"error":{"code":"\#(code)","message":"\#(message)"}}"#.utf8))
}

@MainActor
final class LidMonitorTests: XCTestCase {
    private let fixedNow = Date(timeIntervalSince1970: 1_900_000_000)

    // MARK: - Waiting helpers (event-based; the timeout is a deadlock net, never a race)

    private func waitForGetEntry(_ stub: StubLid, timeout: TimeInterval = 10) async {
        let entered = expectation(description: "the held GET /lid has entered")
        Task { await stub.awaitGetEntry(); entered.fulfill() }
        await fulfillment(of: [entered], timeout: timeout)
    }

    private func waitForPostEntry(_ stub: StubLid, timeout: TimeInterval = 10) async {
        let entered = expectation(description: "the held POST /lid has entered")
        Task { await stub.awaitPostEntry(); entered.fulfill() }
        await fulfillment(of: [entered], timeout: timeout)
    }

    private struct WaitTimeout: Error {}

    private func waitUntil(timeout: TimeInterval, _ condition: () async -> Bool) async throws {
        let deadline = Date().addingTimeInterval(timeout)
        while !(await condition()) {
            if Date() > deadline {
                XCTFail("condition not met within \(timeout) s")
                throw WaitTimeout()
            }
            try await Task.sleep(for: .milliseconds(10))
        }
    }

    // MARK: - fold

    func testFoldStates() {
        func f(_ r: LidResponse?, _ rf: LidReadFailure? = nil, unavailable: Bool = false) -> LidDisplay {
            LidDisplay.fold(response: r, readFailure: rf, unavailable: unavailable, actionError: nil, isActing: false)
        }
        let d = Date(timeIntervalSince1970: 2_000_000_000)
        XCTAssertEqual(f(nil).state, .loading)
        XCTAssertEqual(f(nil, unavailable: true).state, .unavailable)
        XCTAssertEqual(f(nil, .unreachable).state, .unreachable)                              // URLError, jamais lu
        XCTAssertEqual(f(nil, .failed("pmset -g failed")).state, .failed("pmset -g failed"))  // 500 du daemon, jamais lu (D19)
        XCTAssertNotEqual(f(nil, .failed("pmset -g failed")).state, .unreachable, "une réponse d'erreur du daemon n'est pas « injoignable »")
        XCTAssertFalse(f(nil, .failed("pmset -g failed")).isActive)
        XCTAssertEqual(f(LidResponse(sleepDisabled: true, deadline: d)).state, .on(deadline: d))
        XCTAssertEqual(f(LidResponse(sleepDisabled: true)).state, .on(deadline: nil))
        XCTAssertEqual(f(LidResponse(sleepDisabled: false)).state, .off)
        let frozen = f(LidResponse(sleepDisabled: true, deadline: d), .failed("x"))            // lecture ratée après une réussie
        XCTAssertEqual(frozen.state, .on(deadline: d)); XCTAssertTrue(frozen.readFailed); XCTAssertEqual(frozen.readFailure, .failed("x"))
        XCTAssertNil(f(LidResponse(sleepDisabled: false, lastError: "")).lastError)
    }

    // MARK: - Reads

    func testNeverReadNeverShowsTravel() async {
        let down = StubLid(get: .failure(URLError(.cannotConnectToHost)))
        let m1 = LidMonitor(client: down)
        await m1.poll()
        XCTAssertEqual(m1.display.state, .unreachable)
        XCTAssertFalse(m1.display.isActive)

        let broken = StubLid(get: .failure(httpError(500, code: "INTERNAL", message: "pmset -g failed")))
        let m2 = LidMonitor(client: broken)
        await m2.poll()
        XCTAssertEqual(m2.display.state, .failed("pmset -g failed"))
        XCTAssertFalse(m2.display.isActive)
    }

    func testReadFailureFreezesLastState() async {
        let d = Date(timeIntervalSince1970: 2_000_000_000)
        let stub = StubLid(get: .success(LidResponse(sleepDisabled: true, deadline: d)))
        let m = LidMonitor(client: stub)
        await m.poll()
        await stub.setGet(.failure(URLError(.cannotConnectToHost)))
        await m.poll()
        XCTAssertEqual(m.display.state, .on(deadline: d))
        XCTAssertTrue(m.display.readFailed)
        XCTAssertEqual(m.display.readFailure, .unreachable)

        await stub.setGet(.failure(httpError(500, code: "INTERNAL", message: "pmset -g failed")))
        await m.poll()
        XCTAssertEqual(m.display.state, .on(deadline: d))
        XCTAssertEqual(m.display.readFailure, .failed("pmset -g failed"))
    }

    func testUnavailableIsVisible() async {
        let stub = StubLid(get: .failure(httpError(501, code: "LID_UNAVAILABLE", message: "lid: sleep control is only available on macOS")))
        let m = LidMonitor(client: stub)
        await m.poll()
        XCTAssertEqual(m.display.state, .unavailable)
        XCTAssertFalse(m.display.isActive)
    }

    // MARK: - Actions

    func testActivateIsOptimisticThenReconciled() async {
        let d = Date(timeIntervalSince1970: 2_000_000_000)
        let stub = StubLid(get: .success(LidResponse(sleepDisabled: false)))
        let now = fixedNow
        let m = LidMonitor(client: stub, now: { now })
        await m.poll()
        await stub.holdNextPost()
        let action = Task { await m.perform(.activate(hours: 8)) }
        await waitForPostEntry(stub)
        XCTAssertEqual(m.display.state, .on(deadline: fixedNow.addingTimeInterval(8 * 3600)))
        XCTAssertTrue(m.display.isActing)
        await stub.setGet(.success(LidResponse(sleepDisabled: true, deadline: d)))
        await stub.releasePost(.success(LidResponse(sleepDisabled: true, deadline: d)))
        await action.value
        XCTAssertEqual(m.display.state, .on(deadline: d))
        XCTAssertFalse(m.display.isActing)
        let calls = await stub.setCalls
        XCTAssertEqual(calls, [.init(on: true, hours: 8)])
        let gets = await stub.getCalls
        XCTAssertEqual(gets, 2, "une relecture après l'action")
    }

    func testExtendSendsHoursAndDeactivateSendsOff() async {
        let stub = StubLid(get: .success(LidResponse(sleepDisabled: false)))
        let m = LidMonitor(client: stub)
        await m.perform(.activate(hours: 1))
        await m.perform(.extend(hours: 4))
        await m.perform(.deactivate)
        let calls = await stub.setCalls
        XCTAssertEqual(calls, [.init(on: true, hours: 1), .init(on: true, hours: 4), .init(on: false, hours: nil)])
    }

    func testFailureRestoresPreviousStateAndShowsError() async {
        let stub = StubLid(get: .success(LidResponse(sleepDisabled: false)),
                           post: .failure(httpError(500, code: "INTERNAL", message: "boom")))
        let m = LidMonitor(client: stub)
        await m.poll()
        await m.perform(.activate(hours: 8))
        XCTAssertEqual(m.display.state, .off)
        XCTAssertEqual(m.display.actionError, .failed("boom"))
        XCTAssertFalse(m.display.isActing)
    }

    func testSecondActionWhileOneRunsIsDropped() async {
        let stub = StubLid(get: .success(LidResponse(sleepDisabled: false)))
        let m = LidMonitor(client: stub)
        await m.poll()
        await stub.holdNextPost()
        let first = Task { await m.perform(.activate(hours: 8)) }
        await waitForPostEntry(stub)
        await m.perform(.activate(hours: 1))
        await m.perform(.deactivate)
        await stub.releasePost(.success(LidResponse(sleepDisabled: true)))
        await first.value
        let calls = await stub.setCalls
        XCTAssertEqual(calls.count, 1, "un seul POST pendant une action en cours")
    }

    func testNotSetUpStaysUntilASuccessfulAction() async {
        let stub = StubLid(get: .success(LidResponse(sleepDisabled: false)),
                           post: .failure(httpError(412, code: "LID_NOT_SET_UP", message: "lid: not set up")))
        let m = LidMonitor(client: stub)
        await m.poll()
        await m.perform(.activate(hours: 1))
        XCTAssertEqual(m.display.actionError, .notSetUp)
        await m.poll()
        XCTAssertEqual(m.display.actionError, .notSetUp, "un GET ne sait pas si le setup existe")
        await stub.setPost(.success(LidResponse(sleepDisabled: true)))
        await m.perform(.activate(hours: 1))
        XCTAssertNil(m.display.actionError)
    }

    func testStaleReadNeverOverwritesTheActionResult() async {
        let d = Date(timeIntervalSince1970: 2_000_000_000)
        let stub = StubLid(get: .success(LidResponse(sleepDisabled: false)))
        let m = LidMonitor(client: stub)
        await m.poll()                                                    // off, getCalls == 1
        await stub.holdNextGet(); await stub.holdNextPost()
        let oldRead = Task { await m.poll() }                             // lecture planifiée, partie avant l'action
        await waitForGetEntry(stub)
        let action = Task { await m.perform(.activate(hours: 8)) }
        await waitForPostEntry(stub)
        XCTAssertTrue(m.display.isActing)
        await stub.setGet(.success(LidResponse(sleepDisabled: true, deadline: d)))   // ce que rendra la relecture de fin d'action
        await stub.releasePost(.success(LidResponse(sleepDisabled: true, deadline: d)))
        await action.value                                                // POST rendu + relecture de fin d'action
        let callsAfterAction = await stub.getCalls
        XCTAssertEqual(callsAfterAction, 3, "la relecture de fin d'action n'est pas sautée par la lecture ancienne en vol")
        await stub.releaseGet(.success(LidResponse(sleepDisabled: false)))  // la vieille lecture atterrit en dernier, avec l'état d'avant
        await oldRead.value
        XCTAssertEqual(m.display.state, .on(deadline: d), "une lecture partie avant la fin de l'action est jetée")
        XCTAssertFalse(m.display.readFailed)
        XCTAssertFalse(m.display.isActing)
    }

    // MARK: - D18 cadence

    func testPollingRepeatsAtTheDefault60sAndOnlyStopPollingStopsIt() async throws {
        XCTAssertEqual(LidMonitor.defaultPollInterval, 60)               // D18 : la valeur que prend startPolling() sans argument
        let stub = StubLid(get: .success(LidResponse(sleepDisabled: false)))
        let m = LidMonitor(client: stub)
        m.startPolling(interval: 0.05)
        try await waitUntil(timeout: 5) { await stub.getCalls >= 2 }     // la boucle relit seule
        await stub.setGet(.failure(URLError(.cannotConnectToHost)))      // une lecture en échec n'arrête pas la boucle
        await m.perform(.deactivate)                                      // une action non plus
        await stub.setGet(.success(LidResponse(sleepDisabled: false)))
        let afterAction = await stub.getCalls
        try await waitUntil(timeout: 5) { await stub.getCalls >= afterAction + 2 }
        m.stopPolling()
        try await Task.sleep(for: .milliseconds(150))                     // une lecture déjà partie peut encore atterrir
        let frozen = await stub.getCalls
        try await Task.sleep(for: .milliseconds(400))                     // 8 intervalles
        let later = await stub.getCalls
        XCTAssertEqual(later, frozen, "seule stopPolling arrête les lectures")
    }

    // MARK: - D26: re-read on lid.changed, coalesced

    private var dOn: Date { Date(timeIntervalSince1970: 2_000_000_000) }
    private var on: LidResponse { LidResponse(sleepDisabled: true, deadline: dOn) }
    private var off: LidResponse { LidResponse(sleepDisabled: false) }

    func testEventRefreshReadsOnce() async {
        let stub = StubLid(get: .success(on))
        let m = LidMonitor(client: stub)
        m.refreshFromEvent()
        XCTAssertNotNil(m.eventRefreshTask, "nothing in flight: the event starts its own read")
        await m.eventRefreshTask?.value
        let gets = await stub.getCalls
        XCTAssertEqual(gets, 1)
        XCTAssertEqual(m.display.state, .on(deadline: dOn))
        XCTAssertNil(m.eventRefreshTask)
    }

    func testEventsDuringAReadCoalesceIntoOneMoreRead() async {
        let stub = StubLid(get: .success(off))
        let m = LidMonitor(client: stub)
        await stub.holdNextGet()
        let p = Task { await m.poll() }
        await waitForGetEntry(stub)                                      // GET 1 held, read in flight
        for _ in 0..<5 { m.refreshFromEvent() }
        XCTAssertNil(m.eventRefreshTask, "a read in flight: the events only set the flag")
        await stub.setGet(.success(on))
        await stub.releaseGet(.success(off))
        await p.value
        let gets = await stub.getCalls
        XCTAssertEqual(gets, 2, "five events during a read cost exactly one more read")
        XCTAssertEqual(m.display.state, .on(deadline: dOn), "the extra read ran after the one in flight")
    }

    func testEventBurstWithNoReadInFlightReadsAtMostTwice() async {
        let stub = StubLid(get: .success(on))
        let m = LidMonitor(client: stub)
        await stub.holdNextGet()
        for _ in 0..<50 { m.refreshFromEvent() }
        await waitForGetEntry(stub)
        await stub.releaseGet(.success(off))
        await m.eventRefreshTask?.value
        let gets = await stub.getCalls
        XCTAssertEqual(gets, 2)
        XCTAssertEqual(m.display.state, .on(deadline: dOn))
        XCTAssertNil(m.eventRefreshTask)
    }

    func testEventDuringActionNeverOverwritesTheActionResult() async {
        let stub = StubLid(get: .success(off))
        let m = LidMonitor(client: stub)
        await stub.holdNextPost()
        let a = Task { await m.perform(.activate(hours: 1)) }
        await waitForPostEntry(stub)
        XCTAssertEqual(m.generation, 1)
        XCTAssertTrue(m.display.isActing)
        m.refreshFromEvent()
        XCTAssertNil(m.eventRefreshTask, "an action running: the event only sets the flag")
        await stub.setGet(.success(on))
        await stub.releasePost(.success(on))
        await a.value
        let gets = await stub.getCalls
        let sets = await stub.setCalls
        XCTAssertEqual(gets, 2, "the action's closing read, then the event's")
        XCTAssertEqual(sets, [StubLid.SetCall(on: true, hours: 1)])
        XCTAssertEqual(m.display.state, .on(deadline: dOn))
        XCTAssertFalse(m.display.isActing)
    }

    func testPollingStillRuns() async throws {
        let stub = StubLid(get: .success(off))
        let m = LidMonitor(client: stub)
        m.startPolling(interval: 0.05)
        for _ in 0..<3 {
            m.refreshFromEvent()
            await Task.yield()
        }
        // A lower bound, deliberately: the cadence depends on the clock (D18 guard).
        try await waitUntil(timeout: 2) { await stub.getCalls >= 6 }
        m.stopPolling()
        await m.eventRefreshTask?.value
    }
}
