import Combine
import Foundation

// MARK: - PopoverStore

@MainActor
public final class PopoverStore: ObservableObject {
    @Published public private(set) var state: PopoverState
    /// False until the first `refresh()` has ended, whatever its outcome (G9): the store starts at
    /// `daemonDown`, which the popover must not show as a real outage before any answer.
    @Published public private(set) var loaded = false

    private let client: DaemonClienting
    private var pollingTimer: Timer?

    /// Whether a poll tick's `refresh()` has not returned yet. Read and
    /// written only by `pollTick()` and `stopPolling()` — never by
    /// `refresh()`, and never by a mutation; see `pollTick()` for why that
    /// boundary is the whole point. Not a lock: this class is `@MainActor`
    /// and the flag only changes on that actor's own turns.
    private var isTickInFlight = false

    /// How many times `state` has been written. Every write goes through
    /// `publish(_:)`, which is what makes this number mean "the state has
    /// moved since you looked" rather than "a refresh has finished".
    ///
    /// **Why a state version and not a refresh sequence.** The obvious guard
    /// — hand each `refresh()` a number and let only the most recent one write
    /// — does not fix the symptom that motivates it. The timer fires a
    /// refresh; the user clicks Stop mid-flight; `stop` writes its optimistic
    /// state at once and only starts its own refresh after its POST returns.
    /// When the older refresh comes back during that POST it is STILL the most
    /// recent refresh, so a per-refresh guard lets it write and the stopped row
    /// reappears. The write that invalidated it was a mutation, which took no
    /// number. Counting writes of `state` instead counts all of them.
    /// `PopoverStoreTests` pins both halves, and the wrong fix was measured
    /// against them: it turns the overwrite test green and leaves the
    /// resurrection test red.
    ///
    /// `&+=` rather than `+=`: this is an identity, not a quantity, and an
    /// overflow trap on a counter that exists to prevent a display glitch
    /// would be a crash traded for a stale row. At one write per 3-second
    /// tick it would take ~1.7×10^12 years to reach, which is precisely why
    /// no one would ever test the trap path.
    private var stateVersion: UInt64 = 0

    public init(client: DaemonClienting) {
        self.client = client
        // Start with daemonDown until the first refresh. The one direct
        // assignment to `state` in this file, and unavoidable: Swift requires
        // every stored property initialised before `self` is usable, so this
        // cannot route through `publish(_:)`. It is also the only write that
        // needs no version bump — it IS version 0, and nothing can be in
        // flight yet.
        self.state = PopoverState(daemonDown: true, activeCount: 0,
                                  runningRows: [], startGroups: [],
                                  totalRSSLabel: "", idlePurgeGraceSeconds: 0)
    }

    // MARK: - The single write path

    /// The only place `state` is assigned after `init`. Everything below goes
    /// through it — the six optimistic mutations and both branches of
    /// `refresh()` — so that `stateVersion` counts every write without
    /// exception. A seventh mutation that assigns `state` directly would
    /// silently re-open the defect this file's tests exist to keep closed.
    private func publish(_ newState: PopoverState) {
        state = newState
        stateVersion &+= 1
    }

    // MARK: - Refresh

    /// Concurrently fetches servers + workspaces + purge policy; on any throw → daemonDown state.
    ///
    /// **A refresh only publishes if nothing wrote `state` while it was in
    /// flight.** Without that, the slowest writer wins: two refreshes overlap
    /// (a click lands on a poll tick — every mutation ends in a `refresh()`
    /// and the timer fires one every 3 s), the one that left first comes back
    /// last, and the popover rolls back to what the daemon said a round-trip
    /// ago.
    ///
    /// **Why dropping a refresh is safe — the question the next reader will
    /// ask.** An abandoned refresh is never the last word. Something newer
    /// wrote in its place: either a refresh that left later and published, or
    /// an optimistic mutation — and every mutation runs its own reconciliation
    /// right after its POST. That reconciliation starts AFTER its own
    /// optimistic write, so its reference version is current and it publishes.
    /// `setPurge` therefore keeps its ability to revert its optimistic value
    /// when the POST fails, which is the case that would have been lost if the
    /// guard were wrong (`testSetPurgeRevertsItsOptimisticValueWhenThePostFails`).
    ///
    /// **Why not "skip if a refresh is already in flight", the way
    /// a monitor's periodic poll does it.** That guard is right there and wrong
    /// here. A monitor's poll may be dropped because nothing depends on
    /// a particular poll landing — its loop comes back in 15 s and the banner
    /// it carries is a periodic reading. `refresh()` is the reconciliation step
    /// of six mutations: dropping one would strand an optimistic display that
    /// the daemon never confirmed, with nobody scheduled to correct it (the
    /// popover's own timer only runs while the popover is open). The two guards
    /// answer different questions and must stay different — please do not
    /// harmonise them.
    public func refresh() async {
        defer { loaded = true }
        let seen = stateVersion
        do {
            async let s = client.servers()
            async let w = client.workspaces()
            async let p = client.purgePolicy()
            let (servers, workspaces, purge) = try await (s, w, p)
            guard stateVersion == seen else { return }
            publish(ServerListViewModel.build(
                servers: servers, workspaces: workspaces, now: Date(),
                idlePurgeGraceSeconds: purge.graceSeconds))
        } catch {
            guard stateVersion == seen else { return }
            publish(PopoverState(daemonDown: true, activeCount: 0,
                                 runningRows: [], startGroups: [],
                                 totalRSSLabel: "", idlePurgeGraceSeconds: 0))
        }
    }

    // MARK: - Mutations (optimistic)

    /// Sets the idle-purge threshold with an optimistic UI update: the dropdown
    /// reflects `graceSeconds` immediately (so the click feels instant), then the
    /// POST fires and `refresh()` reconciles with the daemon's actual value —
    /// reverting the optimistic value if the POST failed.
    public func setPurge(graceSeconds: Int) {
        publish(state.withIdlePurgeGraceSeconds(graceSeconds))
        Task { @MainActor in
            _ = try? await client.setPurgePolicy(graceSeconds: graceSeconds)
            await refresh()
        }
    }

    /// Optimistically removes the row, then stops the server and reconciles.
    public func stop(row: RunningRow) {
        publish(state.removingRunningRow(id: row.id))
        Task { @MainActor in
            try? await client.stop(workspace: row.workspaceID, server: row.server)
            await refresh()
        }
    }

    /// Optimistically clears all rows, then stops everything and reconciles.
    public func stopAll() {
        publish(state.clearingRunningRows())
        Task { @MainActor in
            try? await client.stopAll()
            await refresh()
        }
    }

    /// Optimistically shows the pin badge, then pins on the daemon and reconciles.
    public func pin(row: RunningRow, duration: PinDuration) {
        let label = duration.badgeLabel
        publish(state.updatingRunningRow(id: row.id) { $0.pinnedCopy(label: label, durable: duration == .untilUnpinned) })
        Task { @MainActor in
            try? await client.pin(workspace: row.workspaceID, server: row.server, duration: duration)
            await refresh()
        }
    }

    /// Optimistically clears the pin badge, then unpins on the daemon and reconciles.
    public func unpin(row: RunningRow) {
        publish(state.updatingRunningRow(id: row.id) { $0.unpinnedCopy() })
        Task { @MainActor in
            try? await client.unpin(workspace: row.workspaceID, server: row.server)
            await refresh()
        }
    }

    /// Optimistically shows a "starting…" row and removes the item from the Start
    /// list, then shells out to `pigtail start` (off the main actor so the UI
    /// isn't blocked) and reconciles after a short delay. If the start failed,
    /// refresh drops the pending row and the item returns to the Start list.
    public func start(_ item: StartItem) {
        publish(state.startingServer(item))
        Task { @MainActor in
            await Task.detached(priority: .userInitiated) {
                try? PigtailLauncher.start(worktree: item.worktree, server: item.name)
            }.value
            try? await Task.sleep(for: .seconds(1))
            await refresh()
        }
    }

    // MARK: - Polling

    /// One turn of the poll loop: a `refresh()`, unless the previous turn's
    /// refresh has not come back yet — in which case this turn is skipped and
    /// no request is issued at all.
    ///
    /// **Why this is not the guard PR #112 declares wrong.** `refresh()`'s own
    /// comment rules out "skip if a refresh is already in flight" *inside*
    /// `refresh()`, because `refresh()` is the reconciliation step of six
    /// optimistic mutations: dropping one would strand an optimistic display
    /// that the daemon never confirmed, with nobody scheduled to correct it
    /// (`setPurge` reverting its own value when the POST fails is exactly that
    /// case). That argument is about *who calls* `refresh()`, not about
    /// `refresh()` itself — so the guard lives here, one level up, and the
    /// separation is structural rather than a matter of care:
    ///
    /// - `refresh()` is untouched, and #112's state-version guard with it;
    /// - the six mutations call `refresh()` **directly**, never this method,
    ///   so no reconciliation can ever be the thing that gets skipped;
    /// - what this method may skip is only ever a periodic tick, which is
    ///   precisely what a periodic poll's skip guard treats as
    ///   droppable: nothing depends on a *particular* tick landing, and the
    ///   timer comes back in 3 seconds.
    ///
    /// **What it buys.** The timer fires every 3 s while the popover is open,
    /// and `/servers` has been measured answering in 6–12 s (11.80 / 6.91 /
    /// 6.41 s). Without this guard four ticks are in flight before the first
    /// returns, so the client multiplies a slowness it is already the victim
    /// of — four times the work on a daemon that forks a process per port,
    /// until a request finally exceeds the `URLSession` timeout and the
    /// popover renders "daemon unavailable". The guard bounds the
    /// amplification at one in-flight tick; it does not hide the daemon's
    /// latency, which is a separate fix, and the 3-second interval is
    /// deliberately unchanged.
    ///
    /// **Three guards, three places, three questions — do not harmonise
    /// them**, which is the same request the monitors' poll guards
    /// already make of their own. `refresh()`'s state version answers "may this refresh still
    /// write?"; a monitor's `isPolling` answers "should a poll be
    /// issued?"; this one answers "should a tick be fired?". Collapsing any
    /// two of them re-opens the defect the third exists to close.
    ///
    /// The test-and-set needs no lock: everything here is `@MainActor`, and
    /// the flag is set before the only `await`, so a second caller can only
    /// ever observe `true`.
    func pollTick() async {
        guard !isTickInFlight else { return }
        isTickInFlight = true
        defer { isTickInFlight = false }
        await refresh()
    }

    /// Starts a 3-second repeating timer that triggers `pollTick()`.
    public func startPolling() {
        guard pollingTimer == nil else { return }
        pollingTimer = Timer.scheduledTimer(withTimeInterval: 3.0, repeats: true) { [weak self] _ in
            guard let self else { return }
            Task { @MainActor in
                await self.pollTick()
            }
        }
    }

    /// Stops the polling timer, and clears the in-flight marker so the next
    /// `startPolling()` is not blocked by a tick that was still out when the
    /// popover closed. A tick left in flight still clears the flag itself when
    /// it returns; this write only makes sure the loop cannot start off
    /// believing a tick from a previous session is still running.
    public func stopPolling() {
        pollingTimer?.invalidate()
        pollingTimer = nil
        isTickInFlight = false
    }
}
