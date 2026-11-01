import Foundation

// MARK: - Wire-type mirrors

/// Who claims a server, in the three states the wire can actually express.
///
/// `owner_alive` is `omitempty` on the Go side, so it only ever appears as
/// `true`. Its *absence* means two different things, told apart by whether
/// `owner` is present at all — see docs/wire-protocol.md. Resolving that once,
/// here, keeps every call site from re-deriving it and getting it wrong in the
/// direction that flags every unclaimed server as orphaned.
public enum OwnerState: Equatable, Sendable {
    /// No `owner` on the wire: nobody has claimed this server.
    case unclaimed
    /// `owner` present and confirmed alive.
    case alive(String)
    /// `owner` present, `owner_alive` absent — a derived identity whose session
    /// leader is gone. There is no "confirmed dead" on the wire, only "not
    /// confirmed alive", which is the case `pigtail stop --all` may reclaim.
    case unconfirmed(String)

    /// The identity, or "" when unclaimed.
    public var name: String {
        switch self {
        case .unclaimed: return ""
        case .alive(let n), .unconfirmed(let n): return n
        }
    }
}

public struct ServerView: Codable, Equatable, Sendable {
    public let workspaceID: String
    public let server: String
    public let branch: String?
    public let port: Int
    public let rssBytes: UInt64
    public let idleSeconds: Int
    public let pinnedUntil: Int64?
    /// Pinned until unpinned (no expiry; `pinnedUntil` is nil then). False
    /// from a daemon that predates durable pins.
    public let pinnedDurable: Bool
    /// The claiming session, absent when unclaimed. Prefer `ownerState`.
    public let owner: String?
    /// The raw wire flag. False also for an unclaimed server, which is NOT a
    /// dead-owner verdict — read `ownerState` rather than this.
    public let ownerAlive: Bool

    public var ownerState: OwnerState {
        guard let owner, !owner.isEmpty else { return .unclaimed }
        return ownerAlive ? .alive(owner) : .unconfirmed(owner)
    }

    public enum CodingKeys: String, CodingKey {
        case workspaceID = "workspace_id"
        case server, branch, port, owner
        case rssBytes = "rss_bytes"
        case idleSeconds = "idle_seconds"
        case pinnedUntil = "pinned_until"
        case ownerAlive = "owner_alive"
        case pinnedDurable = "pinned_durable"
    }

    public init(workspaceID: String, server: String, branch: String?, port: Int,
                rssBytes: UInt64, idleSeconds: Int, pinnedUntil: Int64?,
                owner: String? = nil, ownerAlive: Bool = false,
                pinnedDurable: Bool = false) {
        self.workspaceID = workspaceID
        self.server = server
        self.branch = branch
        self.port = port
        self.rssBytes = rssBytes
        self.idleSeconds = idleSeconds
        self.pinnedUntil = pinnedUntil
        self.owner = owner
        self.ownerAlive = ownerAlive
        self.pinnedDurable = pinnedDurable
    }

    // Hand-written because `ownerAlive` is a non-optional Bool the daemon omits
    // when false; the synthesised decoder would throw on the missing key.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        workspaceID = try c.decode(String.self, forKey: .workspaceID)
        server = try c.decode(String.self, forKey: .server)
        branch = try c.decodeIfPresent(String.self, forKey: .branch)
        port = try c.decode(Int.self, forKey: .port)
        rssBytes = try c.decode(UInt64.self, forKey: .rssBytes)
        idleSeconds = try c.decode(Int.self, forKey: .idleSeconds)
        pinnedUntil = try c.decodeIfPresent(Int64.self, forKey: .pinnedUntil)
        owner = try c.decodeIfPresent(String.self, forKey: .owner)
        ownerAlive = try c.decodeIfPresent(Bool.self, forKey: .ownerAlive) ?? false
        // Omitted by the daemon unless true, and absent from older daemons.
        pinnedDurable = try c.decodeIfPresent(Bool.self, forKey: .pinnedDurable) ?? false
    }
}

/// A dev server that vanished on its own — a crash, an OOM kill, or a kill
/// from outside pigtail. A deliberate stop never appears here.
public struct DeadServerView: Codable, Equatable, Sendable {
    public let workspaceID: String
    public let server: String
    public let port: Int
    /// How long ago the death was detected, in seconds. Recomputed by the
    /// daemon on every call, not cached.
    public let agoSeconds: Int

    public enum CodingKeys: String, CodingKey {
        case workspaceID = "workspace_id"
        case server, port
        case agoSeconds = "ago_seconds"
    }

    public init(workspaceID: String, server: String, port: Int, agoSeconds: Int) {
        self.workspaceID = workspaceID
        self.server = server
        self.port = port
        self.agoSeconds = agoSeconds
    }
}

public struct ServersResponse: Codable, Equatable, Sendable {
    public let availableBytes: UInt64
    public let floorBytes: UInt64
    public let servers: [ServerView]
    /// Servers that died on their own inside the detector's 30-minute window,
    /// oldest first. Non-optional: the daemon omits the key when there are
    /// none, and "absent" and "empty" must not become two different things on
    /// this side of the wire.
    public let recentDeaths: [DeadServerView]

    public enum CodingKeys: String, CodingKey {
        case availableBytes = "available_bytes"
        case floorBytes = "floor_bytes"
        case servers
        case recentDeaths = "recent_deaths"
    }

    public init(availableBytes: UInt64, floorBytes: UInt64, servers: [ServerView],
                recentDeaths: [DeadServerView] = []) {
        self.availableBytes = availableBytes
        self.floorBytes = floorBytes
        self.servers = servers
        self.recentDeaths = recentDeaths
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        availableBytes = try c.decode(UInt64.self, forKey: .availableBytes)
        floorBytes = try c.decode(UInt64.self, forKey: .floorBytes)
        servers = try c.decode([ServerView].self, forKey: .servers)
        recentDeaths = try c.decodeIfPresent([DeadServerView].self, forKey: .recentDeaths) ?? []
    }
}

public struct Workspace: Codable, Equatable, Sendable {
    public let id: String
    public let path: String
    public let ports: [String: Int]?
    public let lastActivityAt: Date
    public let displayRef: String?

    public enum CodingKeys: String, CodingKey {
        case id, path, ports
        case lastActivityAt = "last_activity_at"
        case displayRef = "display_ref"
    }
}

public enum PinDuration: String, Sendable {
    case m30 = "30m"
    case h1 = "1h"
    case h4 = "4h"
    /// Pinned until unpinned; survives daemon restarts (decision D9).
    case untilUnpinned = "until-unpinned"
}

public struct PurgePolicy: Codable, Equatable, Sendable {
    public let graceSeconds: Int

    public enum CodingKeys: String, CodingKey {
        case graceSeconds = "grace_seconds"
    }

    public init(graceSeconds: Int) { self.graceSeconds = graceSeconds }
}

// MARK: - Protocol

public protocol DaemonClienting: Sendable {
    func servers() async throws -> ServersResponse
    func workspaces() async throws -> [Workspace]
    func pin(workspace: String, server: String, duration: PinDuration) async throws
    func unpin(workspace: String, server: String) async throws
    func stop(workspace: String, server: String) async throws
    func stopAll() async throws
    func purgePolicy() async throws -> PurgePolicy
    func setPurgePolicy(graceSeconds: Int) async throws -> PurgePolicy
}

// MARK: - Envelope

struct Envelope<T: Decodable>: Decodable {
    let data: T
}

// MARK: - Errors

public struct DaemonHTTPError: Error, Sendable {
    public let statusCode: Int
    public let body: Data
}

// MARK: - DaemonClient

public final class DaemonClient: DaemonClienting, LidReading, UsageReading, LidEventSource, CheckNoticeReading, CheckNoticeEventSource, DiskAlertReading, DiskEventSource {
    /// Internal rather than private so a test can assert which base a
    /// resolved override actually produced — see `DaemonClientBaseURLTests`.
    let baseURL: URL
    private let token: String?
    private let session: URLSession

    private static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let str = try container.decode(String.self)

            // Try with fractional seconds first (Go time.Time default)
            let withFrac = ISO8601DateFormatter()
            withFrac.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = withFrac.date(from: str) { return date }

            // Fall back to without fractional seconds
            let withoutFrac = ISO8601DateFormatter()
            withoutFrac.formatOptions = [.withInternetDateTime]
            if let date = withoutFrac.date(from: str) { return date }

            throw DecodingError.dataCorruptedError(
                in: container,
                debugDescription: "Cannot parse date: \(str)"
            )
        }
        return d
    }()

    public init(
        baseURL: URL = DaemonClient.resolveBaseURL(),
        token: String? = DaemonClient.readToken(),
        session: URLSession = .shared
    ) {
        self.baseURL = baseURL
        self.token = token
        self.session = session
    }

    /// The daemon the client talks to, as a default argument only.
    ///
    /// `PIGTAIL_APP_BASE_URL` when that variable holds a usable absolute
    /// URL, and the hardcoded loopback in every other case — unset, empty,
    /// or malformed. It exists because there was **no way to reach the
    /// bar's expired state without stopping the real daemon**: the marker
    /// only expires when `/backup` stops answering, and the only `/backup`
    /// the app knew was the live one. With this, a second instance of
    /// `Pigtail` can be launched against a stub server serving a canned
    /// `/backup`, side by side with the real bar, and driven into exactly
    /// the state to be judged — including the one combination nothing else
    /// produces, a frozen `alarm` that has expired:
    ///
    /// ```
    /// PIGTAIL_APP_BASE_URL=http://127.0.0.1:7899 \
    ///   macapp/.build/release/Pigtail
    /// ```
    ///
    /// Read as a **default argument**, never consulted anywhere else: with
    /// the variable unset the value is byte-for-byte the literal that was
    /// hardcoded here before, so the app's behaviour without it is
    /// rigorously unchanged. The malformed case falls back rather than
    /// trapping — a debugging aid that bricks the real bar on a typo is not
    /// one.
    ///
    /// - Parameter environment: injected so the resolution is testable
    ///   without mutating the process environment, which is a snapshot the
    ///   whole test bundle shares.
    public static func resolveBaseURL(
        environment: [String: String] = ProcessInfo.processInfo.environment
    ) -> URL {
        let fallback = URL(string: "http://127.0.0.1:7800")!
        guard let raw = environment["PIGTAIL_APP_BASE_URL"]?
                .trimmingCharacters(in: .whitespacesAndNewlines),
              !raw.isEmpty,
              let url = URL(string: raw),
              url.scheme != nil,
              url.host != nil
        else { return fallback }
        return url
    }

    public static func readToken() -> String? {
        let path = FileManager.default
            .homeDirectoryForCurrentUser
            .appendingPathComponent(".config/pigtail/token")
        guard let raw = try? String(contentsOf: path, encoding: .utf8) else { return nil }
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }

    // MARK: - DaemonClienting

    public func servers() async throws -> ServersResponse {
        try await get(path: "/servers")
    }

    public func workspaces() async throws -> [Workspace] {
        try await get(path: "/workspaces")
    }

    public func pin(workspace: String, server: String, duration: PinDuration) async throws {
        try await mutate(method: "POST",
                         path: "/servers/\(workspace)/\(server)/pin",
                         query: "duration=\(duration.rawValue)")
    }

    public func unpin(workspace: String, server: String) async throws {
        try await mutate(method: "DELETE",
                         path: "/servers/\(workspace)/\(server)/pin")
    }

    public func stop(workspace: String, server: String) async throws {
        try await mutate(method: "POST",
                         path: "/servers/\(workspace)/\(server)/stop")
    }

    public func stopAll() async throws {
        try await mutate(method: "POST", path: "/servers/stop-all")
    }

    public func purgePolicy() async throws -> PurgePolicy {
        try await get(path: "/purge")
    }

    public func setPurgePolicy(graceSeconds: Int) async throws -> PurgePolicy {
        try await postJSON(path: "/purge", body: ["grace_seconds": graceSeconds])
    }

    // MARK: - UsageReading

    /// The scope the bar reads as: the master of the built-in scope table (the bar is the
    /// machine's single view); any other scope gets a 403 from the usage route.
    public static let machineScope = "perso"

    /// The master's view of the Claude accounts (403 for any other scope).
    public func usageOverview() async throws -> UsageOverview {
        try await get(path: "/usage/overview", headers: ["X-Pigtail-Scope": Self.machineScope])
    }

    // MARK: - CheckNoticeReading

    /// The end-of-pass line of the cmux check (master scope only: the bar
    /// sends no scope by default, which would be a silent 403).
    public func cmuxCheckNotice() async throws -> CheckNoticeResponse {
        try await get(path: "/cmux/check/notice", headers: ["X-Pigtail-Scope": Self.machineScope])
    }

    // MARK: - LidReading

    /// A /lid call can wait 70 s on the daemon side (pkg/pigtail/lid_client.go);
    /// URLSession's default is 60 s. The GET and the POST both carry it.
    static let lidTimeout: TimeInterval = 95

    public func lid() async throws -> LidResponse {
        try await get(path: "/lid", timeout: Self.lidTimeout)
    }

    public func setLid(on: Bool, hours: Int?) async throws -> LidResponse {
        try await postBody(path: "/lid", body: LidRequest(on: on, hours: hours), timeout: Self.lidTimeout)
    }

    /// The daemon pings `/events` every 15 s: 45 s of silence means it is
    /// gone without a FIN.
    static let eventsIdleTimeout: TimeInterval = 45

    /// GET /events?types=lid.* for as long as the consumer iterates. One Task
    /// owns the bytes from birth to end and pushes them into a synchronous
    /// `SSEParser`: only the request, the session and the continuation (all
    /// Sendable) are captured. Cancelling the consumer closes the connection.
    public func lidEvents() -> AsyncThrowingStream<DaemonStreamItem, Error> {
        let req = buildRequest(method: "GET", path: "/events", query: "types=lid.*",
                               headers: ["Accept": "text/event-stream"], timeout: Self.eventsIdleTimeout)
        let session = self.session
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    let (bytes, response) = try await session.bytes(for: req)
                    if let code = (response as? HTTPURLResponse)?.statusCode, code >= 400 {
                        var body = Data()
                        for try await b in bytes {
                            body.append(b)
                            if body.count >= 4096 { break }
                        }
                        throw DaemonHTTPError(statusCode: code, body: body)
                    }
                    continuation.yield(.connected)
                    var parser = SSEParser()
                    for try await b in bytes {
                        if let e = parser.feed(byte: b) { continuation.yield(.event(e)) }
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() } // consumer gone → connection closed
        }
    }

    // MARK: - DiskAlertReading / DiskEventSource

    /// GET /alerts, reduced to the open disk alert. The alert is machine-wide:
    /// every scope reads it, the machine scope as the other bar reads.
    public func diskAlert() async throws -> DiskAlert? {
        let req = buildRequest(method: "GET", path: "/alerts", headers: ["X-Pigtail-Scope": Self.machineScope])
        return try DiskAlert.firstDiskAlert(inAlertsBody: try await execute(req))
    }

    /// GET /events?types=disk.* — same stream mechanics as `lidEvents`.
    public func diskEvents() -> AsyncThrowingStream<DaemonStreamItem, Error> {
        let req = buildRequest(method: "GET", path: "/events", query: "types=disk.*",
                               headers: ["Accept": "text/event-stream", "X-Pigtail-Scope": Self.machineScope],
                               timeout: Self.eventsIdleTimeout)
        let session = self.session
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    let (bytes, response) = try await session.bytes(for: req)
                    if let code = (response as? HTTPURLResponse)?.statusCode, code >= 400 {
                        var body = Data()
                        for try await b in bytes {
                            body.append(b)
                            if body.count >= 4096 { break }
                        }
                        throw DaemonHTTPError(statusCode: code, body: body)
                    }
                    continuation.yield(.connected)
                    var parser = SSEParser()
                    for try await b in bytes {
                        if let e = parser.feed(byte: b) { continuation.yield(.event(e)) }
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    /// GET /events?types=cmux.check_notice — same stream mechanics as
    /// `lidEvents`, with the machine scope (the route's events are master's).
    public func checkNoticeEvents() -> AsyncThrowingStream<DaemonStreamItem, Error> {
        let req = buildRequest(method: "GET", path: "/events", query: "types=cmux.check_notice",
                               headers: ["Accept": "text/event-stream", "X-Pigtail-Scope": Self.machineScope],
                               timeout: Self.eventsIdleTimeout)
        let session = self.session
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    let (bytes, response) = try await session.bytes(for: req)
                    if let code = (response as? HTTPURLResponse)?.statusCode, code >= 400 {
                        var body = Data()
                        for try await b in bytes {
                            body.append(b)
                            if body.count >= 4096 { break }
                        }
                        throw DaemonHTTPError(statusCode: code, body: body)
                    }
                    continuation.yield(.connected)
                    var parser = SSEParser()
                    for try await b in bytes {
                        if let e = parser.feed(byte: b) { continuation.yield(.event(e)) }
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    // MARK: - Private helpers

    private func buildRequest(method: String, path: String, query: String? = nil,
                              headers: [String: String] = [:], timeout: TimeInterval? = nil) -> URLRequest {
        var components = URLComponents(url: baseURL.appendingPathComponent(path),
                                       resolvingAgainstBaseURL: false)!
        if let query { components.query = query }
        var req = URLRequest(url: components.url!)
        req.httpMethod = method
        if let timeout { req.timeoutInterval = timeout }
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.setValue("v1", forHTTPHeaderField: "X-Pigtail-API")
        for (name, value) in headers {
            req.setValue(value, forHTTPHeaderField: name)
        }
        if let token {
            req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        return req
    }

    private func execute(_ req: URLRequest) async throws -> Data {
        let (data, response) = try await session.data(for: req)
        guard let http = response as? HTTPURLResponse else {
            throw URLError(.badServerResponse)
        }
        guard http.statusCode < 400 else {
            throw DaemonHTTPError(statusCode: http.statusCode, body: data)
        }
        return data
    }

    private func get<T: Decodable>(path: String, headers: [String: String] = [:],
                                   timeout: TimeInterval? = nil) async throws -> T {
        let req = buildRequest(method: "GET", path: path, headers: headers, timeout: timeout)
        let data = try await execute(req)
        let envelope = try Self.decoder.decode(Envelope<T>.self, from: data)
        return envelope.data
    }

    private func mutate(method: String, path: String, query: String? = nil) async throws {
        let req = buildRequest(method: method, path: path, query: query)
        _ = try await execute(req)
    }

    /// `POST` with no request body, decoding the response envelope — the
    /// counterpart to `get`, for a mutating route whose response carries a
    /// payload `mutate` above discards. `runOffsiteBackup` is the one caller
    /// today.
    private func post<T: Decodable>(path: String) async throws -> T {
        let req = buildRequest(method: "POST", path: path)
        let data = try await execute(req)
        let envelope = try Self.decoder.decode(Envelope<T>.self, from: data)
        return envelope.data
    }

    /// `POST` with an encoded body — `postJSON` below only takes `[String: Int]`.
    private func postBody<B: Encodable, T: Decodable>(path: String, body: B,
                                                      timeout: TimeInterval? = nil) async throws -> T {
        var req = buildRequest(method: "POST", path: path, timeout: timeout)
        req.httpBody = try JSONEncoder().encode(body)
        let data = try await execute(req)
        return try Self.decoder.decode(Envelope<T>.self, from: data).data
    }

    private func postJSON<T: Decodable>(path: String, body: [String: Int]) async throws -> T {
        var req = buildRequest(method: "POST", path: path)
        req.httpBody = try JSONSerialization.data(withJSONObject: body)
        let data = try await execute(req)
        return try Self.decoder.decode(Envelope<T>.self, from: data).data
    }
}
