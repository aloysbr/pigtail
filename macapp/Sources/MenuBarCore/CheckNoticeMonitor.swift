import Foundation
import os

/// Reads GET /cmux/check/notice at start, every 60 s and on each
/// cmux.check_notice event (R33: seconds, not the poll's minute), and hands
/// a non-empty line to onNotice once per launch (D2).
@MainActor
public final class CheckNoticeMonitor {
    public static let title = "Reprise cmux"
    private let client: CheckNoticeReading
    private let ledger: NoticeLedger
    private let onNotice: @MainActor (CheckNotice) -> Void
    private var pollingTask: Task<Void, Never>?

    public init(client: CheckNoticeReading, ledger: NoticeLedger, onNotice: @escaping @MainActor (CheckNotice) -> Void) {
        self.client = client
        self.ledger = ledger
        self.onNotice = onNotice
    }

    public func poll() async {
        do {
            let r = try await client.cmuxCheckNotice()
            guard !r.line.isEmpty else { return }
            let id = "cmux-check-\(r.launchPID)-\(Int(r.at?.timeIntervalSince1970 ?? 0))"
            guard !ledger.seen(id) else { return }
            ledger.mark(id)
            onNotice(CheckNotice(id: id, title: Self.title, body: r.line))
        } catch {
            BarLog.events.error("[Pigtail] cmux check notice read failed: \(String(describing: error), privacy: .public)")
        }
    }

    public func startPolling(interval: TimeInterval = LidMonitor.defaultPollInterval) {
        guard pollingTask == nil else { return }
        pollingTask = Task { [weak self] in
            while !Task.isCancelled {
                await self?.poll()
                try? await Task.sleep(for: .seconds(interval))
            }
        }
    }
}

extension CheckNoticeMonitor: LidRefreshing {
    public func refreshFromEvent() { Task { await poll() } }
}

/// Follows `/events?types=cmux.check_notice` for the app's lifetime: each
/// connection and each `cmux.check_notice` asks the monitor to re-read; a lost
/// stream is retried after 1, 2, 5, 10, then 30 s. A twin of LidEventWatcher
/// (Travel Mode's watcher is left untouched).
@MainActor
public final class CheckNoticeEventWatcher {
    public static let backoff: [TimeInterval] = [1, 2, 5, 10, 30]

    private let source: CheckNoticeEventSource
    private let target: LidRefreshing
    private let sleep: @Sendable (TimeInterval) async -> Void
    private let log: (String) -> Void
    private(set) var runTask: Task<Void, Never>?

    public init(source: CheckNoticeEventSource, target: LidRefreshing,
                sleep: @escaping @Sendable (TimeInterval) async -> Void = { try? await Task.sleep(for: .seconds($0)) },
                log: @escaping (String) -> Void = { BarLog.events.notice("[Pigtail] \($0, privacy: .public)") }) {
        self.source = source
        self.target = target
        self.sleep = sleep
        self.log = log
    }

    /// Idempotent: one loop for the app's lifetime.
    public func start() {
        guard runTask == nil else { return }
        runTask = Task { [weak self] in await self?.run() }
    }

    public func stop() {
        runTask?.cancel()
        runTask = nil
    }

    private func run() async {
        var i = 0
        while !Task.isCancelled {
            var reason: String?
            do {
                for try await item in source.checkNoticeEvents() {
                    switch item {
                    case .connected:
                        i = 0
                        log("cmux check notice stream connected")
                        target.refreshFromEvent()
                    case .event(let e) where e.type == "cmux.check_notice":
                        log("cmux check notice event → re-read")
                        target.refreshFromEvent()
                    case .event:
                        break
                    }
                }
            } catch {
                reason = error.localizedDescription
            }
            if Task.isCancelled { return }
            let n = Self.backoff[i]
            log("cmux check notice stream lost: \(reason ?? "closed by daemon") — retry in \(Int(n)) s (60 s poll still running)")
            await sleep(n)
            i = min(i + 1, Self.backoff.count - 1)
        }
    }
}
