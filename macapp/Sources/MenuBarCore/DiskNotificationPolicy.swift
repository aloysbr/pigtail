import Foundation
import os

/// The machine's low-disk alert as the bar needs it: the free space when it
/// opened and the admission floor then (0 = the gate was off, or not reported).
public struct DiskAlert: Equatable, Sendable {
    public let freeBytes: UInt64
    public let floorBytes: UInt64
    public init(freeBytes: UInt64, floorBytes: UInt64) {
        self.freeBytes = freeBytes
        self.floorBytes = floorBytes
    }

    /// The first `kind == "disk"` entry of a GET /alerts body, nil when none.
    /// Machine-wide: the daemon shows it to every scope and names none.
    public static func firstDiskAlert(inAlertsBody body: Data) throws -> DiskAlert? {
        struct Body: Decodable {
            struct Data_: Decodable {
                struct Alert: Decodable {
                    let kind: String
                    let freeBytes: UInt64?
                    let floorBytes: UInt64?
                    enum CodingKeys: String, CodingKey { case kind, freeBytes = "free_bytes", floorBytes = "floor_bytes" }
                }
                let alerts: [Alert]
            }
            let data: Data_
        }
        let decoded = try JSONDecoder().decode(Body.self, from: body)
        guard let a = decoded.data.alerts.first(where: { $0.kind == "disk" }) else { return nil }
        return DiskAlert(freeBytes: a.freeBytes ?? 0, floorBytes: a.floorBytes ?? 0)
    }
}

public protocol DiskAlertReading: Sendable {
    /// GET /alerts, reduced to the open disk alert (nil when none).
    func diskAlert() async throws -> DiskAlert?
}

public protocol DiskEventSource: Sendable {
    /// GET /events?types=disk.* — ends normally when the daemon closes the
    /// stream, throws on a transport or HTTP error.
    func diskEvents() -> AsyncThrowingStream<DaemonStreamItem, Error>
}

/// What the notifier must do: show the notice, or withdraw it.
public enum DiskNotificationAction: Equatable, Sendable {
    case post(id: String, title: String, body: String)
    case remove(id: String)
}

/// When the low-disk notification shows and goes: one per crossing (a second
/// disk.low while low is silent), withdrawn on disk.ok, and an alert already
/// open when the app starts or reconnects is notified once. Pure state, no I/O.
public struct DiskNotificationPolicy: Sendable {
    public static let notificationID = "pigtail-disk-low"
    public static let title = "pigtail · disque"

    private var isLow = false
    public init() {}

    /// « Disque presque plein : <n> Go libres. pigtail fait attendre les tests
    /// sous <plancher> Go. » (GB = 2^30 bytes, like the daemon's flags.) Without a
    /// floor the second sentence is left out: nothing waits.
    public static func body(freeBytes: UInt64, floorBytes: UInt64) -> String {
        guard freeBytes > 0 else { return "Disque presque plein." }
        var s = "Disque presque plein : \(freeBytes >> 30) Go libres."
        if floorBytes > 0 { s += " pigtail fait attendre les tests sous \(floorBytes >> 30) Go." }
        return s
    }

    private func post(_ freeBytes: UInt64, _ floorBytes: UInt64) -> DiskNotificationAction {
        .post(id: Self.notificationID, title: Self.title, body: Self.body(freeBytes: freeBytes, floorBytes: floorBytes))
    }

    public mutating func low(freeBytes: UInt64, floorBytes: UInt64) -> DiskNotificationAction? {
        guard !isLow else { return nil }
        isLow = true
        return post(freeBytes, floorBytes)
    }

    public mutating func ok() -> DiskNotificationAction? {
        guard isLow else { return nil }
        isLow = false
        return .remove(id: Self.notificationID)
    }

    /// The daemon's answer to GET /alerts: the truth, whatever events were missed.
    public mutating func reconcile(_ alert: DiskAlert?) -> DiskNotificationAction? {
        if let alert { return low(freeBytes: alert.freeBytes, floorBytes: alert.floorBytes) }
        return ok()
    }
}

extension SSEEvent {
    /// `data.free_bytes` and `data.floor_bytes` of a disk event envelope; 0 when absent.
    var diskPayload: (freeBytes: UInt64, floorBytes: UInt64) {
        guard let obj = try? JSONSerialization.jsonObject(with: Data(data.utf8)) as? [String: Any],
              let inner = obj["data"] as? [String: Any] else { return (0, 0) }
        func n(_ k: String) -> UInt64 { (inner[k] as? NSNumber)?.uint64Value ?? 0 }
        return (n("free_bytes"), n("floor_bytes"))
    }
}

@MainActor
public protocol DiskEventHandling: AnyObject {
    /// Re-read GET /alerts and align the notification with it.
    func reconcile() async
    func handle(_ event: SSEEvent)
}

/// Holds the policy and hands its actions to the notifier (`onAction`).
@MainActor
public final class DiskMonitor: DiskEventHandling {
    private let reader: DiskAlertReading
    private let onAction: @MainActor (DiskNotificationAction) -> Void
    private var policy = DiskNotificationPolicy()

    public init(reader: DiskAlertReading, onAction: @escaping @MainActor (DiskNotificationAction) -> Void) {
        self.reader = reader
        self.onAction = onAction
    }

    public func reconcile() async {
        do {
            let alert = try await reader.diskAlert()
            if let a = policy.reconcile(alert) { onAction(a) }
        } catch {
            // Freeze, don't clear: an unreadable daemon says nothing about the disk.
            BarLog.events.error("[Pigtail] disk alert read failed: \(String(describing: error), privacy: .public)")
        }
    }

    public func handle(_ event: SSEEvent) {
        switch event.type {
        case "disk.low":
            let p = event.diskPayload
            if let a = policy.low(freeBytes: p.freeBytes, floorBytes: p.floorBytes) { onAction(a) }
        case "disk.ok":
            if let a = policy.ok() { onAction(a) }
        default:
            break
        }
    }
}
