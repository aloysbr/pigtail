import Foundation

/// Contract C1: `~/.local/state/pigtail/maintenance-window.json`, written by the maintenance-window tool
/// (0600, atomic). The bar only reads it. `version != 1` is unreadable, never guessed at.
public struct MaintenanceWindow: Decodable, Equatable, Sendable {
    public struct Serving: Decodable, Equatable, Sendable {
        public let key: String
        public let label: String
        public let since: Double
        public let until: Double
        public let needMin: Int
    }
    public struct QueueItem: Decodable, Equatable, Sendable {
        public let key: String
        public let label: String
        public let needMin: Int
    }
    public struct Served: Decodable, Equatable, Sendable {
        public let label: String
        public let needMin: Int
        public let tookS: Int
        public let interrupted: Bool
    }
    public struct Passage: Decodable, Equatable, Sendable {
        public let from: String
        public let to: String
        public let at: Double
    }

    public let version: Int
    public let state: String
    public let started: Double
    public let updated: Double
    public let until: Double?
    public let serving: Serving?
    public let queue: [QueueItem]
    public let served: [Served]
    public let totalMin: Int
    public let passage: Passage?
    public let ended: Double?
    public let endedBy: String?
    public let warnings: [String]
    public let failure: String?

    struct UnsupportedVersion: Error { let version: Int }

    public static func decode(_ data: Data) throws -> MaintenanceWindow {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        let w = try d.decode(MaintenanceWindow.self, from: data)
        guard w.version == 1 else { throw UnsupportedVersion(version: w.version) }
        return w
    }
}
