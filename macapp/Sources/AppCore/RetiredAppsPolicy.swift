import Foundation

/// Which running apps Pigtail terminates: the retired apps it replaces, and only
/// from the release bundle. A verify build (another bundle id) or an executable
/// outside a bundle (no id) never terminates anything, so the real menu bar app
/// keeps running while a test build is tried on the same Mac. The retired ids are
/// injected by the caller (`LegacyDefaultsMigration.retiredBundleIDs`).
public enum RetiredAppsPolicy {
    public static func isActive(ownBundleID: String?) -> Bool {
        AppIdentity.isReleaseBundle(ownBundleID)
    }

    public static func shouldTerminate(runningBundleID: String?, ownBundleID: String?, retired: [String]) -> Bool {
        guard isActive(ownBundleID: ownBundleID), let running = runningBundleID else { return false }
        return retired.contains(running)
    }
}

/// Remembers which retired-app pids were already judged, so the workspace KVO
/// (which reports the whole running list each time) acts once per process.
/// Only retired pids are stored, and a pid that left the running list is
/// forgotten: the set stays small and a reused pid is judged afresh.
public struct RetiredPidTracker {
    public private(set) var seen = Set<pid_t>()

    public init() {}

    /// Records a retired pid the caller is about to act on (initial sweep).
    public mutating func markJudged(_ pid: pid_t) { seen.insert(pid) }

    /// Returns the retired pids in `running` not judged yet, and remembers them.
    public mutating func judge(running: [(pid: pid_t, id: String?)], retired: [String]) -> [pid_t] {
        seen.formIntersection(running.map { $0.pid })
        var fresh: [pid_t] = []
        for app in running where !seen.contains(app.pid) {
            guard let id = app.id, retired.contains(id) else { continue }
            seen.insert(app.pid)
            fresh.append(app.pid)
        }
        return fresh
    }
}
