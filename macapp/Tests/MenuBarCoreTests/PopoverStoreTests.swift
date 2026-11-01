import Combine
import XCTest
@testable import MenuBarCore

// MARK: - Test double
//
// A `DaemonClienting` whose `/servers` route the test opens by hand, one call
// at a time.
//
// An `actor`, same family as the other gated stubs and for
// the same reason: two `refresh()` calls are in flight at once here, so the
// call counter and the parked continuations are touched from more than one
// task.
//
// **Only `servers()` is gated.** `workspaces()` and `purgePolicy()` answer
// immediately, which is enough to hold a whole `refresh()` open: `refresh()`
// does a `try await` of the three together, so it cannot finish before
// `servers()` does. Gating the other two would add ceremony and no coverage.
//
// **Nothing here waits on a duration.** `awaitEntry(_:)` returns the instant a
// `servers()` call has entered — before it parks, and immediately if it
// entered already — so a test can state "A is in, now start B" as an observed
// fact rather than as a bet on a sleep being long enough.
// `respond(to:with:)` then releases the parked calls in whatever order the
// test chooses: the overtaking is *produced* by the test, never raced for. A
// test that flips A and B by making one stub slower than the other would be
// the exact flaky test this repo refuses.
private actor GatedDaemonClient: DaemonClienting {

    // MARK: Canned answers for the ungated routes

    private let workspacesResponse: [Workspace]
    private let purgeResponse: PurgePolicy
    private let setPurgeResponse: Result<PurgePolicy, Error>

    // MARK: `servers()` gating

    /// How many `servers()` calls have entered so far; also the index the next
    /// one gets. Indices are the test's handle on a specific call.
    private var enteredCount = 0
    /// Calls that have entered and are waiting for an answer.
    private var parked: [Int: CheckedContinuation<Result<ServersResponse, Error>, Never>] = [:]
    /// Answers handed over before their call arrived, so a test may seed a
    /// response for a call it does not intend to wait on.
    private var queued: [Int: Result<ServersResponse, Error>] = [:]
    /// Tests waiting for a given call to enter.
    private var entryWaiters: [Int: CheckedContinuation<Void, Never>] = [:]

    // MARK: `stop()` gating
    //
    // The POST is held open too, on request. Not decoration: a mutation's own
    // reconciliation only starts once its POST returns, so holding the POST is
    // the only way to reproduce the window in which an older refresh comes
    // back while the mutation's refresh has not left yet. Measured: with the
    // POST answering instantly, the resurrection test passes even against a
    // guard that only numbers refreshes — the window never opens.

    private let gateStop: Bool
    private var stopEntered = 0
    private var parkedStop: CheckedContinuation<Void, Never>?
    private var stopReleased = false
    private var stopEntryWaiter: CheckedContinuation<Void, Never>?

    // MARK: Recorded mutations

    private(set) var stopped: [String] = []
    private(set) var setPurgeCalls: [Int] = []

    init(workspaces: [Workspace] = [],
         purge: PurgePolicy = PurgePolicy(graceSeconds: 0),
         setPurge: Result<PurgePolicy, Error> = .success(PurgePolicy(graceSeconds: 0)),
         gateStop: Bool = false) {
        self.workspacesResponse = workspaces
        self.purgeResponse = purge
        self.setPurgeResponse = setPurge
        self.gateStop = gateStop
    }

    // MARK: Test controls

    /// How many `servers()` calls have reached the client. The tick guard is
    /// only observable here: a skipped tick leaves no trace on `state`, since
    /// the tick it skipped would have published the same thing.
    var serversCallCount: Int { enteredCount }

    /// Returns once the `servers()` call numbered `index` has entered.
    /// Returns immediately if it already has, so there is no window in which
    /// a test can miss the event it is waiting for.
    func awaitEntry(_ index: Int) async {
        guard enteredCount <= index else { return }
        await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
            entryWaiters[index] = c
        }
    }

    /// Answers the `servers()` call numbered `index`, whether it has entered
    /// yet or not.
    func respond(to index: Int, with result: Result<ServersResponse, Error>) {
        if let parkedCall = parked.removeValue(forKey: index) {
            parkedCall.resume(returning: result)
        } else {
            queued[index] = result
        }
    }

    /// Drains every still-parked call, so a test never ends leaving a
    /// continuation suspended forever.
    func releaseAllParked(with result: Result<ServersResponse, Error>) {
        for (_, call) in parked { call.resume(returning: result) }
        parked.removeAll()
    }

    /// Returns once a gated `stop()` POST has entered — same "already
    /// happened" shortcut as `awaitEntry(_:)`.
    func awaitStopEntry() async {
        guard stopEntered == 0 else { return }
        await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
            stopEntryWaiter = c
        }
    }

    /// Lets a gated `stop()` POST return, whether it has entered yet or not.
    func releaseStop() {
        stopReleased = true
        parkedStop?.resume()
        parkedStop = nil
    }

    // MARK: DaemonClienting

    func servers() async throws -> ServersResponse {
        let index = enteredCount
        enteredCount += 1
        entryWaiters.removeValue(forKey: index)?.resume()
        if let ready = queued.removeValue(forKey: index) {
            return try ready.get()
        }
        let result = await withCheckedContinuation {
            (c: CheckedContinuation<Result<ServersResponse, Error>, Never>) in
            parked[index] = c
        }
        return try result.get()
    }

    func workspaces() async throws -> [Workspace] { workspacesResponse }

    func purgePolicy() async throws -> PurgePolicy { purgeResponse }

    func setPurgePolicy(graceSeconds: Int) async throws -> PurgePolicy {
        setPurgeCalls.append(graceSeconds)
        return try setPurgeResponse.get()
    }

    func stop(workspace: String, server: String) async throws {
        stopped.append(workspace + "/" + server)
        stopEntered += 1
        stopEntryWaiter?.resume()
        stopEntryWaiter = nil
        guard gateStop, !stopReleased else { return }
        await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
            parkedStop = c
        }
    }

    func stopAll() async throws {}

    func pin(workspace: String, server: String, duration: PinDuration) async throws {}

    func unpin(workspace: String, server: String) async throws {}
}

private struct StubDaemonError: Error {}
private struct StubPostError: Error {}

// MARK: - Tests

@MainActor
final class PopoverStoreTests: XCTestCase {

    // MARK: - Fixtures

    private func serversResponse(_ names: [String]) -> ServersResponse {
        ServersResponse(
            availableBytes: 0, floorBytes: 0,
            servers: names.enumerated().map { offset, name in
                ServerView(workspaceID: "ws1", server: name, branch: nil,
                           port: 6010 + offset, rssBytes: 0, idleSeconds: 0,
                           pinnedUntil: nil)
            })
    }

    // MARK: - Waiting helpers (event-based, timeouts only as a deadlock net)

    /// Waits for the `servers()` call numbered `index` to enter. The timeout
    /// can only fire if the call never happens at all — it is a deadlock net,
    /// not a race the machine's load could lose.
    private func waitForServersEntry(_ index: Int, on client: GatedDaemonClient,
                                     timeout: TimeInterval = 10) async {
        let entered = expectation(description: "servers() call #\(index) has entered")
        Task { await client.awaitEntry(index); entered.fulfill() }
        await fulfillment(of: [entered], timeout: timeout)
    }

    /// Same, for the gated `stop()` POST.
    private func waitForStopEntry(on client: GatedDaemonClient,
                                  timeout: TimeInterval = 10) async {
        let entered = expectation(description: "the stop() POST has entered")
        Task { await client.awaitStopEntry(); entered.fulfill() }
        await fulfillment(of: [entered], timeout: timeout)
    }

    /// Fires a poll tick and waits for it to return. Same deadlock-net rule as
    /// the helpers above: a tick the guard skips returns without suspending at
    /// all, so the timeout can only fire if the tick went to the client
    /// instead — which is exactly the regression this helper's callers pin.
    /// Awaiting the tick directly would hang there rather than fail.
    private func waitForTickReturn(_ store: PopoverStore,
                                   timeout: TimeInterval = 10) async {
        let returned = expectation(description: "the poll tick returned")
        Task { @MainActor in await store.pollTick(); returned.fulfill() }
        await fulfillment(of: [returned], timeout: timeout)
    }

    /// Waits until `store.state` satisfies `predicate`. `@Published` replays
    /// its current value to every new subscriber, so a state that already
    /// satisfies the predicate is caught rather than missed: this cannot hang
    /// on a change that landed a moment too early.
    private func waitForState(_ store: PopoverStore, _ description: String,
                              timeout: TimeInterval = 10,
                              satisfying predicate: @escaping (PopoverState) -> Bool) async {
        let reached = expectation(description: description)
        let watcher = Task { @MainActor in
            for await state in store.$state.values where predicate(state) {
                reached.fulfill()
                return
            }
        }
        await fulfillment(of: [reached], timeout: timeout)
        watcher.cancel()
    }

    // MARK: - 1. The overwrite: the slowest refresh must not be the last word

    /// Two refreshes overlap; the one that left FIRST comes back LAST. It is
    /// carrying what the daemon said one round-trip ago, so publishing it
    /// would roll the popover back in time.
    ///
    /// The inversion is produced here, by answering B before A, not by making
    /// one stub slower than the other.
    func testASlowRefreshDoesNotOverwriteAFresherOne() async {
        let client = GatedDaemonClient()
        let store = PopoverStore(client: client)

        let slow = Task { await store.refresh() }
        await waitForServersEntry(0, on: client)
        let fresh = Task { await store.refresh() }
        await waitForServersEntry(1, on: client)

        // The second refresh answers first and lands.
        await client.respond(to: 1, with: .success(serversResponse(["fresh"])))
        await fresh.value
        XCTAssertEqual(store.state.runningRows.map(\.server), ["fresh"])

        // Then the first one finally returns, with older truth.
        await client.respond(to: 0, with: .success(serversResponse(["stale"])))
        await slow.value

        XCTAssertEqual(store.state.runningRows.map(\.server), ["fresh"],
                       "the refresh that left first came back last; it must not win")
    }

    // MARK: - 2. The resurrection: the symptom the whole guard exists for

    /// The scenario a single click during a poll tick produces:
    ///
    /// - `t0` the 3-second timer fires a `refresh()`; it is now in flight.
    /// - `t1` the user clicks Stop. The optimistic write lands immediately and
    ///   the row disappears.
    /// - `t2` the in-flight refresh returns carrying the row — it was read
    ///   from the daemon before the POST even existed. The row must NOT
    ///   come back.
    ///
    /// This is also the test that rules out the obvious-but-wrong fix. A guard
    /// that gives each *refresh* a sequence number and keeps only the most
    /// recent refresh does not catch this: at `t2` the in-flight refresh IS
    /// still the most recent refresh — the write that invalidated it was a
    /// mutation, which took no number. The counter has to be a version of the
    /// STATE.
    ///
    /// **Why the POST is held open here.** That last paragraph is only true
    /// while `stop`'s own `refresh()` has not started, and it starts the
    /// instant the POST returns. With an instant POST the window never opens
    /// and this test passes against the wrong fix too — measured, not
    /// assumed. So the test holds the POST across `t2`, which is also what
    /// the real thing does: a `stop` POST is a round-trip to the daemon,
    /// while the refresh already in flight left up to 3 seconds earlier.
    func testAnInFlightRefreshDoesNotResurrectAnOptimisticallyStoppedRow() async {
        let client = GatedDaemonClient(gateStop: true)
        let store = PopoverStore(client: client)

        // Seed: one running row.
        await client.respond(to: 0, with: .success(serversResponse(["dev"])))
        await store.refresh()
        XCTAssertEqual(store.state.runningRows.map(\.server), ["dev"])

        // t0 — the poll tick.
        let ticking = Task { await store.refresh() }
        await waitForServersEntry(1, on: client)

        // t1 — the click. The optimistic write lands at once; the POST behind
        // it is held, so the mutation's own refresh has not left yet.
        let row = store.state.runningRows[0]
        store.stop(row: row)
        XCTAssertTrue(store.state.runningRows.isEmpty,
                      "the optimistic write is synchronous, before any await")
        await waitForStopEntry(on: client)

        // t2 — the tick returns, still carrying the stopped row.
        await client.respond(to: 1, with: .success(serversResponse(["dev"])))
        await ticking.value

        XCTAssertTrue(store.state.runningRows.isEmpty,
                      "a refresh that left before the Stop must not put the row back")

        // And the abandoned refresh loses nothing: `stop` runs its own
        // reconciliation once its POST returns, so if the daemon still reports
        // the server, THAT refresh is the one entitled to bring it back.
        await client.releaseStop()
        await waitForServersEntry(2, on: client)
        await client.respond(to: 2, with: .success(serversResponse(["dev"])))
        await waitForState(store, "stop's own refresh reconciles with the daemon") {
            $0.runningRows.map(\.server) == ["dev"]
        }
        let stopped = await client.stopped
        XCTAssertEqual(stopped, ["ws1/dev"])
    }

    // MARK: - 3. Anti-over-correction: a lone refresh still publishes

    /// The test that catches a guard tightened until the popover freezes.
    func testALoneRefreshPublishes() async {
        let client = GatedDaemonClient(purge: PurgePolicy(graceSeconds: 1800))
        let store = PopoverStore(client: client)
        XCTAssertTrue(store.state.daemonDown, "nothing fetched yet")

        await client.respond(to: 0, with: .success(serversResponse(["dev"])))
        await store.refresh()

        XCTAssertFalse(store.state.daemonDown)
        XCTAssertEqual(store.state.runningRows.map(\.server), ["dev"])
        XCTAssertEqual(store.state.idlePurgeGraceSeconds, 1800)
    }

    /// G9: the store starts at `daemonDown` before any answer; `loaded` tells that apart from a real
    /// outage. It flips after the first refresh, whatever its outcome.
    func testLoadedFlipsAfterFirstRefresh() async {
        let okClient = GatedDaemonClient()
        let ok = PopoverStore(client: okClient)
        XCTAssertFalse(ok.loaded, "nothing refreshed yet")
        await okClient.respond(to: 0, with: .success(serversResponse(["dev"])))
        await ok.refresh()
        XCTAssertTrue(ok.loaded)

        let failingClient = GatedDaemonClient()
        let failing = PopoverStore(client: failingClient)
        XCTAssertFalse(failing.loaded)
        await failingClient.respond(to: 0, with: .failure(StubDaemonError()))
        await failing.refresh()
        XCTAssertTrue(failing.loaded, "a failed first refresh is an answer: the outage is real now")
        XCTAssertTrue(failing.state.daemonDown)
    }

    /// Same, for the second half of `refresh()`: a failed fetch must still
    /// publish its `daemonDown` verdict.
    func testALoneFailedRefreshPublishesDaemonDown() async {
        let client = GatedDaemonClient()
        let store = PopoverStore(client: client)

        await client.respond(to: 0, with: .success(serversResponse(["dev"])))
        await store.refresh()
        XCTAssertFalse(store.state.daemonDown)

        await client.respond(to: 1, with: .failure(StubDaemonError()))
        await store.refresh()

        XCTAssertTrue(store.state.daemonDown)
        XCTAssertTrue(store.state.runningRows.isEmpty)
    }

    // MARK: - 4. `setPurge` keeps its ability to revert its own optimistic value

    /// The guard must not cost a mutation its reconciliation. `setPurge`
    /// shows the picked value immediately, then the POST fails and its own
    /// `refresh()` — which started AFTER its optimistic write, so its
    /// reference version is current — puts the daemon's real value back.
    func testSetPurgeRevertsItsOptimisticValueWhenThePostFails() async {
        let client = GatedDaemonClient(purge: PurgePolicy(graceSeconds: 300),
                                       setPurge: .failure(StubPostError()))
        let store = PopoverStore(client: client)

        await client.respond(to: 0, with: .success(serversResponse([])))
        await store.refresh()
        XCTAssertEqual(store.state.idlePurgeGraceSeconds, 300)

        store.setPurge(graceSeconds: 1800)
        XCTAssertEqual(store.state.idlePurgeGraceSeconds, 1800,
                       "the dropdown reflects the click immediately")

        await client.respond(to: 1, with: .success(serversResponse([])))
        await waitForState(store, "the optimistic 30 min reverts to the daemon's 5 min") {
            $0.idlePurgeGraceSeconds == 300
        }
        let calls = await client.setPurgeCalls
        XCTAssertEqual(calls, [1800])
    }

    // MARK: - 5. Characterisation: the optimistic writes themselves

    /// Every optimistic mutation must still land synchronously — the guard
    /// counts them, it does not delay or drop them.
    func testOptimisticMutationsStillLandImmediately() async {
        let client = GatedDaemonClient()
        let store = PopoverStore(client: client)

        await client.respond(to: 0, with: .success(serversResponse(["a", "b"])))
        await store.refresh()
        XCTAssertEqual(store.state.runningRows.map(\.server), ["a", "b"])

        store.pin(row: store.state.runningRows[0], duration: .m30)
        XCTAssertTrue(store.state.runningRows[0].pinned)
        XCTAssertEqual(store.state.runningRows[0].pinLabel, "30m")

        store.unpin(row: store.state.runningRows[0])
        XCTAssertFalse(store.state.runningRows[0].pinned)

        store.stopAll()
        XCTAssertTrue(store.state.runningRows.isEmpty)
        XCTAssertEqual(store.state.activeCount, 0)

        // Drain: every mutation above launched its own reconciliation, which
        // is parked on `servers()`. Answering them keeps no continuation
        // suspended past the end of the test.
        await client.releaseAllParked(with: .failure(StubDaemonError()))
    }

    // MARK: - 6. The poll tick does not stack on a slow daemon

    /// The symptom: `/servers` answering in 6–12 s against a 3-second timer,
    /// so four ticks are in flight before the first returns and the client
    /// multiplies the daemon's own slowness.
    ///
    /// The assertion is on the number of calls that actually reached the
    /// client, not on `state` — a skipped tick publishes what the tick it
    /// skipped would have published, so `state` cannot tell the two apart.
    ///
    /// Nothing here waits on a duration: the slow daemon is the gated double
    /// holding `servers()` open until this test answers it, and every step
    /// waits on an observed entry.
    func testAPollTickIsSkippedWhileThePreviousOneIsStillInFlight() async {
        let client = GatedDaemonClient()
        let store = PopoverStore(client: client)

        // Tick 1 reaches the client and parks there — the slow round-trip.
        let firstTick = Task { await store.pollTick() }
        await waitForServersEntry(0, on: client)

        // Tick 2 fires while tick 1 is still out: it must issue nothing, and
        // it must return rather than queue up behind tick 1.
        await waitForTickReturn(store)
        let duringFirst = await client.serversCallCount
        XCTAssertEqual(duringFirst, 1,
                       "a tick fired while the previous one is in flight must not reach the daemon")

        // Tick 1 comes back.
        await client.respond(to: 0, with: .success(serversResponse(["dev"])))
        await firstTick.value

        // Tick 3, now that nothing is in flight, goes through.
        let thirdTick = Task { await store.pollTick() }
        await waitForServersEntry(1, on: client)
        let afterFirstReturned = await client.serversCallCount
        XCTAssertEqual(afterFirstReturned, 2,
                       "once the previous tick has returned, the next one must reach the daemon")

        // Drain before joining: on a build where the guard is gone the calls
        // parked here are not the ones this test named, so releasing by index
        // would leave a continuation suspended and the test would hang instead
        // of reporting the failure it just recorded.
        await client.releaseAllParked(with: .failure(StubDaemonError()))
        await thirdTick.value
    }

    /// `stopPolling()` clears the marker, so a popover closed while a tick was
    /// still out does not come back with its poll loop permanently skipped.
    func testStopPollingClearsTheInFlightTickMarker() async {
        let client = GatedDaemonClient()
        let store = PopoverStore(client: client)

        let strandedTick = Task { await store.pollTick() }
        await waitForServersEntry(0, on: client)

        store.stopPolling()

        let tickAfterRestart = Task { await store.pollTick() }
        await waitForServersEntry(1, on: client)
        let calls = await client.serversCallCount
        XCTAssertEqual(calls, 2,
                       "after stopPolling the next tick must not be blocked by the stranded one")

        await client.releaseAllParked(with: .failure(StubDaemonError()))
        await strandedTick.value
        await tickAfterRestart.value
    }
}
