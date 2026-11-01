import Foundation

/// What the session probe learnt.
public enum ProbeOutcome: Equatable, Sendable {
    /// The cookie opens the web front (404 or 200 on an unknown asset).
    case valid
    /// 401: no cookie, or one the daemon refuses.
    case invalid
    /// 403: the daemon refuses this Host.
    case hostRefused
    /// Any other status.
    case unexpected(Int)
    /// No HTTP answer (refused, timed out).
    case unreachable
}

public protocol SessionProbing: Sendable {
    func check(address: DaemonAddress, cookieValue: String?) async -> ProbeOutcome
}

/// Probes the session with `GET /assets/_probe`: 401 without a valid cookie,
/// 404 with one (internal/daemon/web.go handleWebAsset). `GET /`
/// does not tell: it answers 200 either way.
public struct SessionProbe: SessionProbing {
    public static let cookieName = "pigtail_web"
    public static let probePath = "/assets/_probe"

    private let session: URLSession

    public init(session: URLSession = URLSession(configuration: SessionProbe.makeConfiguration())) {
        self.session = session
    }

    /// No cookie store: the cookie is set by hand on each request.
    public static func makeConfiguration() -> URLSessionConfiguration {
        let c = URLSessionConfiguration.ephemeral
        c.httpShouldSetCookies = false
        c.httpCookieStorage = nil
        c.httpCookieAcceptPolicy = .never
        c.urlCache = nil
        c.requestCachePolicy = .reloadIgnoringLocalCacheData
        c.timeoutIntervalForRequest = 3
        c.timeoutIntervalForResource = 3
        return c
    }

    public func check(address: DaemonAddress, cookieValue: String?) async -> ProbeOutcome {
        var req = URLRequest(url: address.url(path: Self.probePath))
        req.httpMethod = "GET"
        if let v = cookieValue {
            req.setValue("\(Self.cookieName)=\(v)", forHTTPHeaderField: "Cookie")
        }
        do {
            let (_, resp) = try await session.data(for: req)
            guard let http = resp as? HTTPURLResponse else { return .unreachable }
            switch http.statusCode {
            case 401: return .invalid
            case 200, 404: return .valid
            case 403: return .hostRefused
            default: return .unexpected(http.statusCode)
            }
        } catch {
            return .unreachable
        }
    }
}
