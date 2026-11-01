import Foundation
import os

/// A button of the Travel Mode section (D19): every duration is explicit.
public enum LidAction: Equatable, Sendable {
    /// POST on:true hours:n (1, 4 or 8) — mode off.
    case activate(hours: Int)
    /// POST on:true hours:n (1, 4 or 8) — mode on, re-arms to now + n (D5).
    case extend(hours: Int)
    /// POST on:false.
    case deactivate
}

/// The nature of a failed read (D19): it picks the notice while no read has
/// succeeded yet.
public enum LidReadFailure: Equatable, Sendable {
    /// URLError: the daemon is not there → « Démon injoignable » (G15).
    case unreachable
    /// The daemon's error answer (e.g. 500 « pmset -g failed ») or a failed decode → « Échec : <message> ».
    case failed(String)

    /// .unreachable → .unreachable; .failed(m) → .failed(m); .unavailable → nil
    /// (carried by State.unavailable); .notSetUp → .failed("LID_NOT_SET_UP")
    /// (GetLid does not return it at a104028: it only reads pmset -g).
    public static func from(_ e: LidActionError) -> LidReadFailure? {
        switch e {
        case .unreachable: return .unreachable
        case .failed(let m): return .failed(m)
        case .unavailable: return nil
        case .notSetUp: return .failed("LID_NOT_SET_UP")
        }
    }
}

public struct LidDisplay: Equatable, Sendable {
    public enum State: Equatable, Sendable {
        case loading, unreachable, failed(String), unavailable, on(deadline: Date?), off
    }

    public let state: State
    /// The last GET failed, and how; `state` is then the previous read, if any.
    public let readFailure: LidReadFailure?
    public var readFailed: Bool { readFailure != nil }
    /// The daemon's last_error, nil when empty.
    public let lastError: String?
    /// The daemon's guard_error (a guard that cannot read), nil when empty.
    public let guardError: String?
    public let actionError: LidActionError?
    /// A POST is running: the buttons are disabled.
    public let isActing: Bool
    public var isActive: Bool {
        if case .on = state { return true }
        return false
    }

    public static let loading = LidDisplay(state: .loading, readFailure: nil, lastError: nil,
                                           guardError: nil, actionError: nil, isActing: false)

    public static func fold(response: LidResponse?, readFailure: LidReadFailure?, unavailable: Bool,
                            actionError: LidActionError?, isActing: Bool) -> LidDisplay {
        let state: State
        if let r = response {
            state = r.sleepDisabled ? .on(deadline: r.deadline) : .off
        } else if unavailable {
            state = .unavailable
        } else {
            switch readFailure {
            case .unreachable?: state = .unreachable
            case .failed(let m)?: state = .failed(m)
            case nil: state = .loading
            }
        }
        let lastError = response?.lastError.flatMap { $0.isEmpty ? nil : $0 }
        let guardError = response?.guardError.flatMap { $0.isEmpty ? nil : $0 }
        return LidDisplay(state: state, readFailure: readFailure, lastError: lastError,
                          guardError: guardError, actionError: actionError, isActing: isActing)
    }
}

/// Reads GET /lid every 60 s for the app's lifetime, popover open or not (D18),
/// and posts the section's actions optimistically. Same freeze-don't-clear
/// model as the other monitors, with a generation instead of an `isPolling` flag:
/// a read that left before an action's POST came back is dropped, so it can
/// never bring back the state from before the action (`PopoverStore.refresh()`
/// uses the same versioning).
@MainActor
public final class LidMonitor: ObservableObject {
    @Published public private(set) var display: LidDisplay = .loading

    /// D18 — the single source of the cadence.
    public static let defaultPollInterval: TimeInterval = 60

    private let client: LidReading
    private let now: @Sendable () -> Date
    private var last: LidResponse?
    /// The last answer a notice was computed against (D3). Separate from
    /// `last`: a 501 clears `last` but never this base, or the next read would
    /// be taken for the first one and a cut in between would be lost.
    private var noticeBase: LidResponse?
    private let onNotice: (@MainActor (LidCutNotice) -> Void)?
    private var readFailure: LidReadFailure?
    private var unavailable = false
    private var actionError: LidActionError?
    private var pollingTask: Task<Void, Never>?

    /// +1 when an accepted `perform` starts and +1 when its POST returns
    /// (success or failure).
    private(set) var generation: UInt64 = 0
    /// Generation of the read in flight; one read in flight per generation.
    private var readInFlight: UInt64?

    /// D26: a lid.changed (or a stream (re)connection) asked for a re-read
    /// while one could not start; whoever holds the read in flight services it.
    private var eventReadPending = false
    /// The read `refreshFromEvent` started, if any; internal, awaited by tests.
    private(set) var eventRefreshTask: Task<Void, Never>?

    public init(client: LidReading, now: @escaping @Sendable () -> Date = Date.init,
                onNotice: (@MainActor (LidCutNotice) -> Void)? = nil) {
        self.client = client
        self.now = now
        self.onNotice = onNotice
    }

    /// The single entry of every answer read (GET) or returned (POST): a new
    /// automatic cut or a new failure becomes one notice, then the answer is kept.
    private func accept(_ r: LidResponse) {
        if let n = LidCutNotices.notice(previous: noticeBase, current: r) { onNotice?(n) }
        noticeBase = r
        last = r
    }

    /// One read. Freeze, don't clear: a failure keeps the last response and
    /// flags it with its nature.
    public func poll() async {
        await read(clearsActionError: false)
        await servicePendingEventRead()
    }

    /// lid.changed or a (re)connection: synchronous, never awaits the GET.
    /// A read already in flight, an action running or an event read already
    /// scheduled → flag only; whoever holds that read services the flag after it.
    public func refreshFromEvent() {
        if readInFlight != nil || display.isActing || eventRefreshTask != nil {
            eventReadPending = true
            return
        }
        eventRefreshTask = Task { [weak self] in
            guard let self else { return }
            await self.read(clearsActionError: false)
            await self.servicePendingEventRead()
            self.eventRefreshTask = nil   // MainActor, no suspension after the drain
        }
    }

    /// Called by every caller of `read` AFTER it returned: `read`'s defer has
    /// cleared `readInFlight` by now, so this read really issues a GET.
    /// Another read in flight (another caller) or an action running → leave
    /// the flag: that caller services it when its own read returns.
    private func servicePendingEventRead() async {
        while eventReadPending, readInFlight == nil, !display.isActing {
            eventReadPending = false
            await read(clearsActionError: false)
        }
    }

    /// `clearsActionError: false` is the re-read that closes an action: it
    /// must not wipe the error that very action just set, or the notice would
    /// vanish the instant it appears. The next read clears it.
    private func read(clearsActionError: Bool) async {
        guard readInFlight != generation else { return }
        let g = generation
        readInFlight = g
        defer { if readInFlight == g { readInFlight = nil } }
        let result: Result<LidResponse, Error>
        do { result = .success(try await client.lid()) } catch { result = .failure(error) }
        // Stale (an action started or returned meanwhile) or overtaken by an
        // action in progress: dropped whole, nothing touched.
        guard g == generation, !display.isActing else { return }
        switch result {
        case .success(let r):
            accept(r)
            readFailure = nil
            unavailable = false
            if clearsActionError, let e = actionError, e != .notSetUp { actionError = nil }
        case .failure(let error):
            BarLog.lid.error("[Pigtail] lid read failed: \(String(describing: error), privacy: .public)")
            let e = LidActionError.from(error)
            if e == .unavailable {
                last = nil
                unavailable = true
                readFailure = nil
            } else {
                readFailure = LidReadFailure.from(e)
            }
        }
        render(isActing: false)
    }

    public func startPolling(interval: TimeInterval = LidMonitor.defaultPollInterval) {
        guard pollingTask == nil else { return }
        pollingTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                await self.poll()
                try? await Task.sleep(for: .seconds(interval))
            }
        }
    }

    public func stopPolling() {
        pollingTask?.cancel()
        pollingTask = nil
    }

    public func perform(_ action: LidAction) async {
        guard !display.isActing else { return }
        generation &+= 1
        let on: Bool
        let hours: Int?
        let optimistic: LidResponse
        switch action {
        case .activate(let h), .extend(let h):
            on = true; hours = h
            optimistic = LidResponse(sleepDisabled: true, deadline: now().addingTimeInterval(TimeInterval(h) * 3600))
        case .deactivate:
            on = false; hours = nil
            optimistic = LidResponse(sleepDisabled: false)
        }
        display = .fold(response: optimistic, readFailure: nil, unavailable: false,
                        actionError: actionError, isActing: true)
        let result: Result<LidResponse, Error>
        do { result = .success(try await client.setLid(on: on, hours: hours)) } catch { result = .failure(error) }
        generation &+= 1
        switch result {
        case .success(let r):
            accept(r)
            readFailure = nil
            unavailable = false
            actionError = nil
        case .failure(let error):
            BarLog.lid.error("[Pigtail] lid action failed: \(String(describing: error), privacy: .public)")
            actionError = LidActionError.from(error)
        }
        render(isActing: false)
        await read(clearsActionError: false)
        await servicePendingEventRead()
    }

    private func render(isActing: Bool) {
        display = .fold(response: last, readFailure: readFailure, unavailable: unavailable,
                        actionError: actionError, isActing: isActing)
    }
}
