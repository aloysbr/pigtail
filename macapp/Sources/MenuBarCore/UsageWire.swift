import Foundation

// MARK: - GET /usage/overview wire mirrors (pkg/pigtail/usage.go, docs/wire-protocol.md § GET /usage/overview)
//
// Additive contract: unknown keys are ignored, and `state` / `unusable` stay raw
// Strings — an enum would throw on a value this build does not know and turn the
// whole read into "daemon unreachable". The verdict's `oldest_reading_at`,
// `next_renewal`, `blocked_from` and `frees` are always present and `null`
// outside their state; `unreadable_count` is omitted at 0.

public protocol UsageReading: Sendable {
    func usageOverview() async throws -> UsageOverview
}

public struct UsageWindow: Decodable, Equatable, Sendable {
    public let percent: Int
    public let resetsAt: Date?
    public let full: Bool

    public init(percent: Int, resetsAt: Date?, full: Bool) {
        self.percent = percent; self.resetsAt = resetsAt; self.full = full
    }

    enum CodingKeys: String, CodingKey { case percent, full, resetsAt = "resets_at" }
}

/// A per-model weekly limit (Fable, …): the window's fields flattened next to `model`.
public struct UsageScoped: Decodable, Equatable, Sendable {
    public let model: String
    public let percent: Int
    public let resetsAt: Date?
    public let full: Bool

    public init(model: String, percent: Int, resetsAt: Date?, full: Bool) {
        self.model = model; self.percent = percent; self.resetsAt = resetsAt; self.full = full
    }

    enum CodingKeys: String, CodingKey { case model, percent, full, resetsAt = "resets_at" }
}

public struct UsageAccount: Decodable, Equatable, Sendable {
    public let accountUUID: String
    public let label: String
    public let inVerdict: Bool
    /// "no-reading" | "cap" | "" — raw, an unknown value stays readable.
    public let unusable: String
    public let fetchedAt: Date?
    public let sevenDay: UsageWindow?
    public let fiveHour: UsageWindow?
    public let scoped: [UsageScoped]
    public let windowLeft: Double?
    public let fast: Bool
    public let refreshFailed: Bool
    public let historySince: Date?

    public init(accountUUID: String, label: String, inVerdict: Bool = true, unusable: String = "",
                fetchedAt: Date? = nil, sevenDay: UsageWindow? = nil, fiveHour: UsageWindow? = nil,
                scoped: [UsageScoped] = [], windowLeft: Double? = nil, fast: Bool = false,
                refreshFailed: Bool = false, historySince: Date? = nil) {
        self.accountUUID = accountUUID; self.label = label; self.inVerdict = inVerdict
        self.unusable = unusable; self.fetchedAt = fetchedAt; self.sevenDay = sevenDay
        self.fiveHour = fiveHour; self.scoped = scoped; self.windowLeft = windowLeft
        self.fast = fast; self.refreshFailed = refreshFailed; self.historySince = historySince
    }

    enum CodingKeys: String, CodingKey {
        case label, unusable, scoped, fast
        case accountUUID = "account_uuid", inVerdict = "in_verdict", fetchedAt = "fetched_at"
        case sevenDay = "seven_day", fiveHour = "five_hour", windowLeft = "window_left"
        case refreshFailed = "refresh_failed", historySince = "history_since"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        accountUUID = try c.decode(String.self, forKey: .accountUUID)
        label = try c.decode(String.self, forKey: .label)
        inVerdict = try c.decodeIfPresent(Bool.self, forKey: .inVerdict) ?? false
        unusable = try c.decodeIfPresent(String.self, forKey: .unusable) ?? ""
        fetchedAt = try c.decodeIfPresent(Date.self, forKey: .fetchedAt)
        sevenDay = try c.decodeIfPresent(UsageWindow.self, forKey: .sevenDay)
        fiveHour = try c.decodeIfPresent(UsageWindow.self, forKey: .fiveHour)
        scoped = try c.decodeIfPresent([UsageScoped].self, forKey: .scoped) ?? []
        windowLeft = try c.decodeIfPresent(Double.self, forKey: .windowLeft)
        fast = try c.decodeIfPresent(Bool.self, forKey: .fast) ?? false
        refreshFailed = try c.decodeIfPresent(Bool.self, forKey: .refreshFailed) ?? false
        historySince = try c.decodeIfPresent(Date.self, forKey: .historySince)
    }
}

public struct UsageLabelAt: Decodable, Equatable, Sendable {
    public let label: String
    public let at: Date
    public init(label: String, at: Date) { self.label = label; self.at = at }
}

public struct UsageVerdict: Decodable, Equatable, Sendable {
    /// "ok" | "tight" | "later" | "now" | "none" — raw.
    public let state: String
    public let provisional: Bool
    public let oldestReadingAt: Date?
    public let nextRenewal: UsageLabelAt?
    public let blockedFrom: Date?
    public let frees: UsageLabelAt?

    public init(state: String, provisional: Bool = false, oldestReadingAt: Date? = nil,
                nextRenewal: UsageLabelAt? = nil, blockedFrom: Date? = nil, frees: UsageLabelAt? = nil) {
        self.state = state; self.provisional = provisional; self.oldestReadingAt = oldestReadingAt
        self.nextRenewal = nextRenewal; self.blockedFrom = blockedFrom; self.frees = frees
    }

    enum CodingKeys: String, CodingKey {
        case state, provisional, frees
        case oldestReadingAt = "oldest_reading_at", nextRenewal = "next_renewal", blockedFrom = "blocked_from"
    }
}

public struct UsageScopePick: Decodable, Equatable, Sendable {
    public let scope: String
    /// The scope's name as the route shows it (never written in Swift).
    public let display: String
    public let master: Bool
    /// Designates an entry of `accounts[]` (I11), never by label.
    public let accountUUID: String?
    /// "no-candidate" only.
    public let error: String?

    public init(scope: String, display: String, master: Bool, accountUUID: String? = nil, error: String? = nil) {
        self.scope = scope; self.display = display; self.master = master
        self.accountUUID = accountUUID; self.error = error
    }

    enum CodingKeys: String, CodingKey {
        case scope, display, master, error
        case accountUUID = "account_uuid"
    }
}

public struct UsageOverview: Decodable, Equatable, Sendable {
    public let generatedAt: Date
    public let accounts: [UsageAccount]
    public let verdict: UsageVerdict?
    public let nextSession: [UsageScopePick]
    /// Cells whose `.claude.json` could not be read (I13, G16); absent = 0.
    public let unreadableCount: Int

    public init(generatedAt: Date, accounts: [UsageAccount], verdict: UsageVerdict?,
                nextSession: [UsageScopePick], unreadableCount: Int = 0) {
        self.generatedAt = generatedAt; self.accounts = accounts; self.verdict = verdict
        self.nextSession = nextSession; self.unreadableCount = unreadableCount
    }

    enum CodingKeys: String, CodingKey {
        case accounts, verdict
        case generatedAt = "generated_at", nextSession = "next_session", unreadableCount = "unreadable_count"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        generatedAt = try c.decode(Date.self, forKey: .generatedAt)
        accounts = try c.decodeIfPresent([UsageAccount].self, forKey: .accounts) ?? []
        verdict = try c.decodeIfPresent(UsageVerdict.self, forKey: .verdict)
        nextSession = try c.decodeIfPresent([UsageScopePick].self, forKey: .nextSession) ?? []
        unreadableCount = try c.decodeIfPresent(Int.self, forKey: .unreadableCount) ?? 0
    }
}
