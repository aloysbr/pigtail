import Foundation

/// What the maintenance chip shows, folded from the last read of `window.json`. Pure: the clock,
/// the read outcome and the last « Reprendre » failure are arguments.
public enum MaintenanceChipDisplay: Equatable, Sendable {
    public enum Dot: Equatable, Sendable { case green, amber, blue, red }

    /// The texts of a two-line chip (D4-B): line 1 = title + (lead) + label + time, line 2 = the queue.
    public struct Content: Equatable, Sendable {
        public var title: String
        public var lead: String?
        public var label: String
        public var time: String
        public var queue: String
        public var progress: Double
        public var dot: Dot
        public var warnings: [String]

        public var line1: String {
            if let lead { return "\(title) · \(lead) \(label) \(time)" }
            return "\(title) · \(label) · \(time)"
        }
        public var lines: [String] { [line1, queue] + (warnings.isEmpty ? [] : [warnings.joined(separator: " · ")]) }
    }

    case hidden
    case running(Content)
    case passage(Content)
    case done(Content)
    /// `served`: what was served before the failure (line 2), nil when nothing was.
    case failed(String, served: String? = nil, warnings: [String])

    public var dot: Dot? {
        switch self {
        case .hidden: return nil
        case .running(let c), .passage(let c), .done(let c): return c.dot
        case .failed: return .red
        }
    }

    public var lines: [String] {
        switch self {
        case .hidden: return []
        case .running(let c), .passage(let c), .done(let c): return c.lines
        case .failed(let cause, let served, let w):
            return ["Maintenance — \(cause)"] + (served.map { [$0] } ?? []) + (w.isEmpty ? [] : [w.joined(separator: " · ")])
        }
    }

    /// A heartbeat older than this is a dead runner (C1: `updated` every 2 s).
    static let staleAfter: TimeInterval = 30
    /// G3: the « X terminée ✓ → Y » line stays this long after `passage.at`.
    static let passageFor: TimeInterval = 10

    /// « 42 min », « 1 h 17 », « < 1 min ».
    public static func formatMinutes(_ m: Int) -> String {
        if m < 1 { return "< 1 min" }
        if m < 60 { return "\(m) min" }
        return "\(m / 60) h " + String(format: "%02d", m % 60)
    }

    public static func fold(window: MaintenanceWindow?, readError: Bool, takeBackError: String?, now: Date) -> MaintenanceChipDisplay {
        let warnings = window?.warnings ?? []
        let served = window.flatMap(servedLine)
        if let takeBackError { return .failed(takeBackError, served: served, warnings: warnings) }   // G5: until the next click
        if readError { return .failed("état de la fenêtre illisible", warnings: []) }
        guard let w = window else { return .hidden }
        let t = now.timeIntervalSince1970

        switch w.state {
        case "failed":
            return .failed(w.failure ?? "la fenêtre a échoué", served: served, warnings: w.warnings)
        case "done":
            return .done(doneContent(w))
        case "running":
            if t - w.updated > staleAfter { return .failed("la fenêtre ne répond plus", served: served, warnings: w.warnings) }
            return running(w, now: t)
        default:
            return .failed("état de la fenêtre inconnu", served: served, warnings: w.warnings)
        }
    }

    private static func running(_ w: MaintenanceWindow, now t: Double) -> MaintenanceChipDisplay {
        let servedSeconds = Double(w.served.reduce(0) { $0 + $1.tookS })
        let total = Double(max(1, w.totalMin)) * 60
        let queueNeed = w.queue.reduce(0) { $0 + $1.needMin }
        let queueNames = w.queue.map { "\($0.label) \($0.needMin) min" }.joined(separator: " · ")

        guard let s = w.serving else {
            // Between two turns: nobody is served for a moment.
            let c = Content(title: "Maintenance", lead: nil, label: "entre deux sessions", time: "…",
                            queue: w.queue.isEmpty ? "Aucune autre session en attente — total \(formatMinutes(w.totalMin))"
                                : "Ensuite : \(queueNames) — total \(formatMinutes(w.totalMin))",
                            progress: clamp(servedSeconds / total), dot: .green, warnings: w.warnings)
            return .running(c)
        }

        let left = s.until - t
        let overrun = left < 0
        let time = overrun ? "dépassé +\(max(1, Int(-left / 60))) min" : formatMinutes(Int((left / 60).rounded(.up)))
        let inPassage = w.passage.map { t >= $0.at && t - $0.at < passageFor } ?? false
        let progress = clamp((servedSeconds + max(0, t - s.since)) / total)

        if inPassage, let p = w.passage {
            let rest = max(0, Int((left / 60).rounded(.up))) + queueNeed
            let c = Content(title: "Maintenance", lead: "✓ \(p.from) →", label: s.label, time: time,
                            queue: w.queue.isEmpty ? "Dernière session — reste \(formatMinutes(rest)) sur \(formatMinutes(w.totalMin))"
                                : "Ensuite : \(queueNames) — reste \(formatMinutes(rest)) sur \(formatMinutes(w.totalMin))",
                            progress: progress, dot: .amber, warnings: w.warnings)
            return .passage(c)
        }
        let c = Content(title: "Maintenance", lead: nil, label: s.label, time: time,
                        queue: w.queue.isEmpty ? "Dernière session de la file — total \(formatMinutes(w.totalMin))"
                            : "Ensuite : \(queueNames) — total \(formatMinutes(w.totalMin))",
                        progress: progress, dot: overrun ? .amber : .green, warnings: w.warnings)
        return .running(c)
    }

    private static func doneContent(_ w: MaintenanceWindow) -> Content {
        let n = w.served.count
        // The total is the sum of the minutes shown per session, so the line always adds up.
        let took = w.served.reduce(0) { $0 + minutes($1.tookS) }
        let each = w.served.map { "\($0.label) \(minutes($0.tookS)) min" + ($0.interrupted ? " (tour interrompu)" : "") }
            .joined(separator: " · ")
        return Content(title: "Maintenance terminée", lead: nil,
                       label: n == 1 ? "1 session servie" : "\(n) sessions servies", time: formatMinutes(took),
                       queue: "\(each) — demandé \(formatMinutes(w.totalMin))",
                       progress: 1, dot: .blue, warnings: w.warnings)
    }

    private static func minutes(_ seconds: Int) -> Int { Int((Double(seconds) / 60).rounded()) }

    /// « Servi : Re-index the project 19 min (tour interrompu) » — nil when nothing was served.
    private static func servedLine(_ w: MaintenanceWindow) -> String? {
        guard !w.served.isEmpty else { return nil }
        return "Servi : " + w.served.map { "\($0.label) \(minutes($0.tookS)) min" + ($0.interrupted ? " (tour interrompu)" : "") }
            .joined(separator: " · ")
    }

    private static func clamp(_ x: Double) -> Double { min(1, max(0, x)) }
}
