import Foundation
import os

/// Whatever re-reads GET /lid on demand. Synchronous: the caller never awaits
/// the GET (D26).
@MainActor public protocol LidRefreshing: AnyObject {
    func refreshFromEvent()
}

extension LidMonitor: LidRefreshing {}

/// Follows `/events?types=lid.*` for the app's lifetime (D26): each
/// connection and each `lid.changed` asks the target for a re-read of
/// GET /lid; a lost stream is logged and retried after 1, 2, 5, 10, then 30 s,
/// back to 1 s once a connection succeeds. The 60 s poll (D18) keeps running
/// beside it.
@MainActor
public final class LidEventWatcher {
    public static let backoff: [TimeInterval] = [1, 2, 5, 10, 30]

    private let source: LidEventSource
    private let target: LidRefreshing
    private let sleep: @Sendable (TimeInterval) async -> Void
    private let log: (String) -> Void

    /// The loop's task; internal, read by @testable tests to await the end after stop().
    private(set) var runTask: Task<Void, Never>?

    public init(source: LidEventSource, target: LidRefreshing,
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
                for try await item in source.lidEvents() {
                    switch item {
                    case .connected:
                        i = 0
                        log("events stream connected")
                        target.refreshFromEvent()
                    case .event(let e) where e.type == "lid.changed":
                        log("lid event (\(e.lidReason ?? "?")) → re-read")
                        // Never awaited: a replayed burst merges into the
                        // monitor's coalescing (at most two reads).
                        target.refreshFromEvent()
                    case .event:
                        break
                    }
                }
            } catch {
                reason = error.localizedDescription
            }
            if Task.isCancelled { return } // stop() is not a loss
            let n = Self.backoff[i]
            log("events stream lost: \(reason ?? "closed by daemon") — retry in \(Int(n)) s (60 s poll still running)")
            await sleep(n)
            i = min(i + 1, Self.backoff.count - 1)
        }
    }
}
