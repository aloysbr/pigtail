import Combine
import Foundation

/// What the bar knows about the Claude usage — and, as loudly, what it does not.
public struct UsageDisplay: Equatable, Sendable {
    public enum State: Equatable, Sendable {
        case loading      // no read has landed yet and none has failed
        case unreachable  // nothing ever landed and the read failed
        case ready        // an overview landed (possibly frozen: see readFailed)
    }

    public let state: State
    /// The last overview read.
    public let overview: UsageOverview?
    /// The last poll threw; `overview` is the previous read's.
    public let readFailed: Bool

    public init(state: State, overview: UsageOverview?, readFailed: Bool) {
        self.state = state; self.overview = overview; self.readFailed = readFailed
    }

    public static let loading = UsageDisplay(state: .loading, overview: nil, readFailed: false)
}

/// Polls GET /usage/overview while the popover is open. Freeze, don't clear:
/// a failed read keeps the last overview (its age keeps growing on screen).
@MainActor
public final class UsageMonitor: ObservableObject {
    @Published public private(set) var display: UsageDisplay = .loading
    /// I9: kept until the bar restarts.
    @Published public var detailExpanded = false

    private let client: UsageReading
    private var last: UsageOverview?
    private var pollingTask: Task<Void, Never>?
    private var isPolling = false

    public init(client: UsageReading) { self.client = client }

    public func poll() async {
        guard !isPolling else { return }
        isPolling = true
        defer { isPolling = false }
        do {
            let o = try await client.usageOverview()
            last = o
            display = UsageDisplay(state: .ready, overview: o, readFailed: false)
        } catch {
            if let last {
                display = UsageDisplay(state: .ready, overview: last, readFailed: true)
            } else {
                display = UsageDisplay(state: .unreachable, overview: nil, readFailed: true)
            }
        }
    }

    public func startPolling(interval: TimeInterval = 60) {
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
}
