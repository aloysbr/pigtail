import Foundation

// MARK: - GET/POST /lid wire mirrors (docs/wire-protocol.md § /lid)
//
// Travel Mode is "lid" inside pigtail (D10): the routes and the types keep
// that name, the visible name is « Travel Mode ». Pigtail only reads
// (GET /lid) and sets (POST /lid) — never sudo, pmset nor /lid/probe (D1, D7).

public protocol LidReading: Sendable {
    /// GET /lid — the real sleep state plus pigtail's timer.
    func lid() async throws -> LidResponse
    /// POST /lid {"on":…, "hours":…}; `hours` nil → the key is absent.
    func setLid(on: Bool, hours: Int?) async throws -> LidResponse
}

public struct LidOff: Decodable, Equatable, Sendable {
    public let at: Date
    public let reason: String
    public init(at: Date, reason: String) { self.at = at; self.reason = reason }
}

public struct LidResponse: Decodable, Equatable, Sendable {
    public let sleepDisabled: Bool
    /// Absent = no pigtail timer.
    public let deadline: Date?
    public let batteryFloor: Int
    public let lastOff: LidOff?
    /// omitempty on the daemon side.
    public let lastError: String?
    /// A guard reading (battery, thermal, lid) failed at the last tick; absent = none.
    public let guardError: String?
    /// The last thermal reading, mode on ("nominal" | "fair" | "serious" | "critical"); absent off.
    public let thermalState: String?

    public init(sleepDisabled: Bool, deadline: Date? = nil, batteryFloor: Int = 25,
                lastOff: LidOff? = nil, lastError: String? = nil,
                guardError: String? = nil, thermalState: String? = nil) {
        self.sleepDisabled = sleepDisabled; self.deadline = deadline; self.batteryFloor = batteryFloor
        self.lastOff = lastOff; self.lastError = lastError
        self.guardError = guardError; self.thermalState = thermalState
    }

    enum CodingKeys: String, CodingKey {
        case deadline
        case sleepDisabled = "sleep_disabled"
        case batteryFloor = "battery_floor"
        case lastOff = "last_off"
        case lastError = "last_error"
        case guardError = "guard_error"
        case thermalState = "thermal_state"
    }
}

/// What went wrong with a /lid call, in the terms the popover shows (D15, D19).
public enum LidActionError: Equatable, Sendable {
    /// 412 or code LID_NOT_SET_UP: the sudoers rule is missing (D15).
    case notSetUp
    /// 501 LID_UNAVAILABLE: this daemon has no sleep control.
    case unavailable
    /// URLError: the daemon is not there — « Démon injoignable » (D19, G15).
    case unreachable
    /// The daemon's reason (error envelope message), or a failed decode's description.
    case failed(String)

    public static func from(_ error: Error) -> LidActionError {
        if error is URLError { return .unreachable }
        if let http = error as? DaemonHTTPError {
            let envelope = try? JSONDecoder().decode(ErrorEnvelope.self, from: http.body)
            let code = envelope?.error.code
            if http.statusCode == 412 || code == "LID_NOT_SET_UP" { return .notSetUp }
            if http.statusCode == 501 || code == "LID_UNAVAILABLE" { return .unavailable }
            if let message = envelope?.error.message, !message.isEmpty { return .failed(message) }
            return .failed("HTTP \(http.statusCode)")
        }
        return .failed(error.localizedDescription)
    }

    /// `{"error":{"code","message"}}` — docs/wire-protocol.md § Standard response shape (error).
    private struct ErrorEnvelope: Decodable {
        struct Body: Decodable { let code: String?; let message: String? }
        let error: Body
    }
}

/// POST /lid request body; `hours` nil is omitted (synthesized encodeIfPresent).
struct LidRequest: Encodable {
    let on: Bool
    let hours: Int?
}
