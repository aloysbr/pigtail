import Foundation

// MARK: - View types

public struct RunningRow: Equatable, Identifiable, Sendable {
    public var id: String { workspaceID + "/" + server }
    public let workspaceID: String
    public let server: String
    public let port: Int
    public let branch: String       // "" when none
    public let idleLabel: String    // "" unless idle >= 5 min
    public let pinned: Bool
    public let pinLabel: String     // "12m" when pinned, else ""
    /// "Extend" for a timed pin, "Change" for a pin until unpinned (picking a
    /// duration replaces it: the last choice wins), "Pin" when not pinned.
    public let pinActionLabel: String
    public let rssLabel: String     // e.g. "402 Mo" or "1.5 Go"
    public let pending: Bool        // true for an optimistic "starting…" row
    public let ownerLabel: String   // "" when unclaimed; this machine's host trimmed
    public let ownerUnconfirmed: Bool // an owner exists but is not confirmed alive

    public init(workspaceID: String, server: String, port: Int, branch: String,
                idleLabel: String, pinned: Bool, pinLabel: String,
                pinActionLabel: String, rssLabel: String, pending: Bool = false,
                ownerLabel: String = "", ownerUnconfirmed: Bool = false) {
        self.workspaceID = workspaceID
        self.server = server
        self.port = port
        self.branch = branch
        self.idleLabel = idleLabel
        self.pinned = pinned
        self.pinLabel = pinLabel
        self.pinActionLabel = pinActionLabel
        self.rssLabel = rssLabel
        self.pending = pending
        self.ownerLabel = ownerLabel
        self.ownerUnconfirmed = ownerUnconfirmed
    }
}

/// One dev server that died on its own, as rendered in the popover. The menu
/// bar polls `GET /servers` rather than staying subscribed to `/events`, so
/// this list is the only place it can learn a server crashed.
public struct DeathRow: Equatable, Identifiable, Sendable {
    public var id: String { workspaceID + "/" + server + "/" + String(port) }
    public let workspaceID: String
    public let server: String
    public let port: Int
    public let agoLabel: String     // "45s", "4m"

    public init(workspaceID: String, server: String, port: Int, agoLabel: String) {
        self.workspaceID = workspaceID
        self.server = server
        self.port = port
        self.agoLabel = agoLabel
    }
}

public struct StartItem: Equatable, Identifiable, Sendable {
    public var id: String { workspaceID + ":" + name }
    public let workspaceID: String
    public let worktree: String     // basename(path), used for `pigtail start <worktree>:<server>`
    public let name: String
    public let port: Int
}

public struct StartGroup: Equatable, Identifiable, Sendable {
    public var id: String { workspaceID }
    public let workspaceID: String
    public let worktree: String
    public let items: [StartItem]   // sorted by name
}

public struct PopoverState: Equatable, Sendable {
    public let daemonDown: Bool
    public let activeCount: Int     // running rows count
    public let runningRows: [RunningRow]
    public let startGroups: [StartGroup]
    public let totalRSSLabel: String // "" when no servers or daemonDown
    public let idlePurgeGraceSeconds: Int
    /// Servers that died on their own, newest state from the last poll. Every
    /// optimistic transform below carries this through untouched: a Stop is
    /// not evidence about an unrelated crash, and dropping it would make the
    /// cross blink out until the next refresh.
    public let deathRows: [DeathRow]

    public init(daemonDown: Bool, activeCount: Int,
                runningRows: [RunningRow], startGroups: [StartGroup],
                totalRSSLabel: String, idlePurgeGraceSeconds: Int,
                deathRows: [DeathRow] = []) {
        self.daemonDown = daemonDown
        self.activeCount = activeCount
        self.runningRows = runningRows
        self.startGroups = startGroups
        self.totalRSSLabel = totalRSSLabel
        self.idlePurgeGraceSeconds = idlePurgeGraceSeconds
        self.deathRows = deathRows
    }

    /// Returns a copy with a different idle-purge threshold. Used for optimistic
    /// UI: the store can reflect a freshly-picked value before the daemon
    /// round-trip completes; the next `refresh()` reconciles with server truth.
    public func withIdlePurgeGraceSeconds(_ seconds: Int) -> PopoverState {
        PopoverState(daemonDown: daemonDown, activeCount: activeCount,
                     runningRows: runningRows, startGroups: startGroups,
                     totalRSSLabel: totalRSSLabel, idlePurgeGraceSeconds: seconds,
                     deathRows: deathRows)
    }

    /// Returns a copy with the matching running row removed and `activeCount`
    /// recomputed. Optimistic transform for a Stop action.
    public func removingRunningRow(id: String) -> PopoverState {
        let rows = runningRows.filter { $0.id != id }
        return PopoverState(daemonDown: daemonDown, activeCount: rows.count,
                            runningRows: rows, startGroups: startGroups,
                            totalRSSLabel: totalRSSLabel,
                            idlePurgeGraceSeconds: idlePurgeGraceSeconds,
                            deathRows: deathRows)
    }

    /// Returns a copy with every running row cleared. Optimistic transform for
    /// a Stop-all action.
    public func clearingRunningRows() -> PopoverState {
        PopoverState(daemonDown: daemonDown, activeCount: 0,
                     runningRows: [], startGroups: startGroups,
                     totalRSSLabel: "", idlePurgeGraceSeconds: idlePurgeGraceSeconds,
                     deathRows: deathRows)
    }

    /// Returns a copy with `transform` applied to the matching running row.
    /// Optimistic transform for Pin / Unpin.
    public func updatingRunningRow(id: String, _ transform: (RunningRow) -> RunningRow) -> PopoverState {
        let rows = runningRows.map { $0.id == id ? transform($0) : $0 }
        return PopoverState(daemonDown: daemonDown, activeCount: activeCount,
                            runningRows: rows, startGroups: startGroups,
                            totalRSSLabel: totalRSSLabel,
                            idlePurgeGraceSeconds: idlePurgeGraceSeconds,
                            deathRows: deathRows)
    }

    /// Optimistic transform for Start: appends a `pending` running row for the
    /// item (matching the eventual real row's id so `refresh()` replaces it
    /// seamlessly) and removes the item from the Start list, dropping its group
    /// if it becomes empty. If a row with that id already exists, it is left
    /// untouched (no duplicate).
    public func startingServer(_ item: StartItem) -> PopoverState {
        let pendingID = item.workspaceID + "/" + item.name
        var rows = runningRows
        if !rows.contains(where: { $0.id == pendingID }) {
            rows.append(RunningRow(
                workspaceID: item.workspaceID, server: item.name, port: item.port,
                branch: "", idleLabel: "", pinned: false, pinLabel: "",
                pinActionLabel: "Pin", rssLabel: "", pending: true))
        }
        let groups: [StartGroup] = startGroups.compactMap { g in
            let items = g.items.filter { $0.id != item.id }
            guard !items.isEmpty else { return nil }
            return StartGroup(workspaceID: g.workspaceID, worktree: g.worktree, items: items)
        }
        return PopoverState(daemonDown: daemonDown, activeCount: rows.count,
                            runningRows: rows, startGroups: groups,
                            totalRSSLabel: totalRSSLabel,
                            idlePurgeGraceSeconds: idlePurgeGraceSeconds,
                            deathRows: deathRows)
    }
}

// MARK: - RunningRow optimistic copies

extension RunningRow {
    /// Returns a pinned copy carrying `label` (the optimistic pin badge text);
    /// `durable` for a pin until unpinned, whose action reads "Change".
    public func pinnedCopy(label: String, durable: Bool = false) -> RunningRow {
        RunningRow(workspaceID: workspaceID, server: server, port: port, branch: branch,
                   idleLabel: idleLabel, pinned: true, pinLabel: label,
                   pinActionLabel: durable ? "Change" : "Extend", rssLabel: rssLabel, pending: pending,
                   ownerLabel: ownerLabel, ownerUnconfirmed: ownerUnconfirmed)
    }

    /// Returns an unpinned copy (clears the badge, resets the action label).
    public func unpinnedCopy() -> RunningRow {
        RunningRow(workspaceID: workspaceID, server: server, port: port, branch: branch,
                   idleLabel: idleLabel, pinned: false, pinLabel: "",
                   pinActionLabel: "Pin", rssLabel: rssLabel, pending: pending,
                   ownerLabel: ownerLabel, ownerUnconfirmed: ownerUnconfirmed)
    }
}

// MARK: - PinDuration seconds

extension PinDuration {
    /// The duration in seconds; nil for a pin until unpinned.
    public var seconds: Int? {
        switch self {
        case .m30: return 1800
        case .h1: return 3600
        case .h4: return 14400
        case .untilUnpinned: return nil
        }
    }

    /// The pin badge text: the time left for a timed pin, ∞ for a durable one.
    public var badgeLabel: String {
        guard let seconds else { return "∞" }
        return ServerListViewModel.shortDur(seconds)
    }
}

// MARK: - ViewModel

public enum ServerListViewModel {

    /// Build a `PopoverState` from raw daemon responses and a fixed point in time.
    /// - Parameters:
    ///   - servers: The last `/servers` response; `nil` means the daemon is unreachable.
    ///   - workspaces: The last `/workspaces` response (may be nil or empty).
    ///   - now: Injected "current time" for deterministic unit tests.
    ///   - hostName: This machine's host, used to trim it out of a derived
    ///     owner identity. Injected for the same reason as `now`; the default
    ///     is what the app passes.
    public static func build(
        servers: ServersResponse?,
        workspaces: [Workspace]?,
        now: Date,
        idlePurgeGraceSeconds: Int,
        hostName: String = ProcessInfo.processInfo.hostName
    ) -> PopoverState {
        guard let servers else {
            // A daemon that stopped answering says nothing about what died, so
            // the death list clears rather than showing a stale cross forever.
            return PopoverState(daemonDown: true, activeCount: 0,
                                runningRows: [], startGroups: [],
                                totalRSSLabel: "", idlePurgeGraceSeconds: 0,
                                deathRows: [])
        }

        // MARK: Running rows
        let nowEpoch = Int64(now.timeIntervalSince1970)

        let runningRows: [RunningRow] = servers.servers
            .sorted { lhs, rhs in
                if lhs.workspaceID != rhs.workspaceID {
                    return lhs.workspaceID < rhs.workspaceID
                }
                return lhs.server < rhs.server
            }
            .map { sv in
                let branch = sv.branch ?? ""

                let idleLabel: String
                if sv.idleSeconds >= 300 {
                    idleLabel = shortDur(sv.idleSeconds)
                } else {
                    idleLabel = ""
                }

                let pinned: Bool
                let pinLabel: String
                let pinActionLabel: String

                if sv.pinnedDurable {
                    pinned = true
                    pinLabel = PinDuration.untilUnpinned.badgeLabel
                    // A duration picked now replaces the durable pin rather
                    // than extending it: the last choice wins.
                    pinActionLabel = "Change"
                } else if let pinnedUntil = sv.pinnedUntil, pinnedUntil > 0 {
                    pinned = true
                    let remaining = max(0, Int(pinnedUntil - nowEpoch))
                    pinLabel = shortDur(remaining)
                    pinActionLabel = "Extend"
                } else {
                    pinned = false
                    pinLabel = ""
                    pinActionLabel = "Pin"
                }

                let ownerState = sv.ownerState
                let ownerUnconfirmed: Bool
                if case .unconfirmed = ownerState { ownerUnconfirmed = true } else { ownerUnconfirmed = false }

                return RunningRow(
                    workspaceID: sv.workspaceID,
                    server: sv.server,
                    port: sv.port,
                    branch: branch,
                    idleLabel: idleLabel,
                    pinned: pinned,
                    pinLabel: pinLabel,
                    pinActionLabel: pinActionLabel,
                    rssLabel: ServerListViewModel.formatRSS(sv.rssBytes),
                    ownerLabel: shortOwner(ownerState.name, host: hostName),
                    ownerUnconfirmed: ownerUnconfirmed
                )
            }

        // MARK: Start groups
        // Build a set of (workspaceID, serverName) pairs that are already running.
        let runningSet = Set(servers.servers.map { "\($0.workspaceID)/\($0.server)" })

        let startGroups: [StartGroup] = (workspaces ?? [])
            .sorted { $0.lastActivityAt > $1.lastActivityAt }
            .compactMap { ws in
                let worktree = URL(fileURLWithPath: ws.path).lastPathComponent
                let ports = ws.ports ?? [:]
                let items: [StartItem] = ports
                    .filter { name, _ in !runningSet.contains("\(ws.id)/\(name)") }
                    .map { name, port in
                        StartItem(workspaceID: ws.id, worktree: worktree, name: name, port: port)
                    }
                    .sorted { $0.name < $1.name }

                guard !items.isEmpty else { return nil }
                return StartGroup(workspaceID: ws.id, worktree: worktree, items: items)
            }

        let totalRSS: UInt64 = servers.servers.reduce(0) { $0 + $1.rssBytes }
        let totalRSSLabel = servers.servers.isEmpty ? "" : ServerListViewModel.formatRSS(totalRSS)

        // MARK: Deaths
        // Kept in the daemon's order (oldest first, per the wire contract)
        // rather than re-sorted: the ordering is part of what it reports.
        let deathRows: [DeathRow] = servers.recentDeaths.map { d in
            DeathRow(workspaceID: d.workspaceID, server: d.server, port: d.port,
                     agoLabel: formatAgo(d.agoSeconds))
        }

        return PopoverState(
            daemonDown: false,
            activeCount: runningRows.count,
            runningRows: runningRows,
            startGroups: startGroups,
            totalRSSLabel: totalRSSLabel,
            idlePurgeGraceSeconds: idlePurgeGraceSeconds,
            deathRows: deathRows
        )
    }

    /// Trims this machine's host from a derived `sid:<n>@<host>` identity — in
    /// a list where every line would repeat it, the host is noise. Any other
    /// identity is shown as-is. Mirrors `shortOwner` in cmd/pigtail/servers.go,
    /// with one deliberate difference: an unclaimed server gets "" here rather
    /// than the CLI's dash, because a popover row is not a table column and an
    /// empty label simply drops out of the line.
    public static func shortOwner(_ owner: String, host: String) -> String {
        guard !owner.isEmpty else { return "" }
        let suffix = "@" + host
        if !host.isEmpty, owner.hasSuffix(suffix) {
            return String(owner.dropLast(suffix.count))
        }
        return owner
    }

    /// A death's age as a short human duration. Mirrors `formatAgo` in
    /// cmd/pigtail/servers.go: whole seconds under a minute, whole rounded
    /// minutes above — never a compound "4m12s", which would read as false
    /// precision for a value refreshed by a background poll.
    public static func formatAgo(_ seconds: Int) -> String {
        if seconds < 60 { return "\(seconds)s" }
        let minutes = Int((Double(seconds) / 60.0).rounded())
        return "\(minutes)m"
    }

    private static let bytesPerMiB: UInt64 = 1024 * 1024
    private static let bytesPerGiB: UInt64 = 1024 * 1024 * 1024

    /// Human-readable duration.
    /// - `< 60s`  → `"<1m"`
    /// - `< 1h`   → `"Nm"`
    /// - else     → `"HhMm"`
    public static func shortDur(_ seconds: Int) -> String {
        if seconds < 60 { return "<1m" }
        let minutes = seconds / 60
        if minutes < 60 { return "\(minutes)m" }
        let h = minutes / 60
        let m = minutes % 60
        return "\(h)h\(m)m"
    }

    /// Format a byte count as a human-readable RAM label.
    /// - `< 1 GiB` → `"\(mib) Mo"` (integer MiB)
    /// - `>= 1 GiB` → `"\(gib) Go"` (one decimal place GiB)
    public static func formatRSS(_ bytes: UInt64) -> String {
        let mib = bytes / bytesPerMiB
        if bytes < bytesPerGiB {
            return "\(mib) Mo"
        }
        let gib = Double(bytes) / Double(bytesPerGiB)
        return "\(String(format: "%.1f", gib)) Go"
    }
}
