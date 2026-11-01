import Foundation
import os

/// Follows `/events?types=disk.*` for the app's lifetime: each connection
/// re-reads GET /alerts (an alert already open when the app starts is
/// notified, and one closed meanwhile is withdrawn), each `disk.low` and
/// `disk.ok` is handed to the target; a lost stream is retried after 1, 2, 5,
/// 10, then 30 s, back to 1 s once a connection succeeds. A twin of
/// LidEventWatcher (which stays untouched).
@MainActor
public final class DiskEventWatcher {
    public static let backoff: [TimeInterval] = [1, 2, 5, 10, 30]

    private let source: DiskEventSource
    private let target: DiskEventHandling
    private let sleep: @Sendable (TimeInterval) async -> Void
    private let log: (String) -> Void
    private(set) var runTask: Task<Void, Never>?

    public init(source: DiskEventSource, target: DiskEventHandling,
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
                for try await item in source.diskEvents() {
                    switch item {
                    case .connected:
                        i = 0
                        log("disk events stream connected")
                        await target.reconcile()
                    case .event(let e) where e.type == "disk.low" || e.type == "disk.ok":
                        log("disk event (\(e.type))")
                        target.handle(e)
                    case .event:
                        break
                    }
                }
            } catch {
                reason = error.localizedDescription
            }
            if Task.isCancelled { return } // stop() is not a loss
            let n = Self.backoff[i]
            log("disk events stream lost: \(reason ?? "closed by daemon") — retry in \(Int(n)) s")
            await sleep(n)
            i = min(i + 1, Self.backoff.count - 1)
        }
    }
}
