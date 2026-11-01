import Foundation

/// GET /cmux/check/notice (docs/wire-protocol.md § cmux check notice, D2 of
/// sub_x000000000000037): the end-of-pass line, empty when nothing is left
/// for the user.
public struct CheckNoticeResponse: Decodable, Equatable, Sendable {
    public let launchPID: Int
    public let at: Date?
    public let line: String
    public init(launchPID: Int, at: Date? = nil, line: String) { self.launchPID = launchPID; self.at = at; self.line = line }
    enum CodingKeys: String, CodingKey { case launchPID = "launch_pid", at, line }
}

public protocol CheckNoticeReading: Sendable {
    func cmuxCheckNotice() async throws -> CheckNoticeResponse
}

public struct CheckNotice: Equatable, Sendable {
    public let id: String
    public let title: String
    public let body: String
    public init(id: String, title: String, body: String) { self.id = id; self.title = title; self.body = body }
}

/// Which launches were already notified — survives a bar restart.
public protocol NoticeLedger: AnyObject {
    func seen(_ id: String) -> Bool
    func mark(_ id: String)
}

public final class InMemoryNoticeLedger: NoticeLedger {
    private var ids = Set<String>()
    public init() {}
    public func seen(_ id: String) -> Bool { ids.contains(id) }
    public func mark(_ id: String) { ids.insert(id) }
}

/// UserDefaults-backed ledger: the last 20 ids.
public final class DefaultsNoticeLedger: NoticeLedger {
    private let key = "cmuxCheckNoticeIDs"
    private let defaults: UserDefaults
    public init(defaults: UserDefaults = .standard) { self.defaults = defaults }
    public func seen(_ id: String) -> Bool { (defaults.stringArray(forKey: key) ?? []).contains(id) }
    public func mark(_ id: String) {
        var ids = defaults.stringArray(forKey: key) ?? []
        ids.append(id)
        defaults.set(Array(ids.suffix(20)), forKey: key)
    }
}

public protocol CheckNoticeEventSource: Sendable {
    /// GET /events?types=cmux.check_notice — ends normally when the daemon
    /// closes the stream, throws on a transport or HTTP error.
    func checkNoticeEvents() -> AsyncThrowingStream<DaemonStreamItem, Error>
}
