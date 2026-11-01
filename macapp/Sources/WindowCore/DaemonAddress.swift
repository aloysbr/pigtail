import AppCore
import Foundation

/// The daemon's HTTP address as the app loads it: a loopback host
/// (127.0.0.1 or localhost, kept exactly as pigtaild reports it — the
/// session cookie is host-only) and a port.
public struct DaemonAddress: Hashable, Sendable {
    public let host: String
    public let port: Int

    /// nil unless httpAddr is "<127.0.0.1|localhost>:<1-65535>".
    public init?(httpAddr: String) {
        let parts = httpAddr.split(separator: ":", omittingEmptySubsequences: false)
        guard parts.count == 2 else { return nil }
        let host = String(parts[0])
        let portText = parts[1]
        guard host == "127.0.0.1" || host == "localhost",
              !portText.isEmpty, portText.allSatisfy(\.isASCII), portText.allSatisfy(\.isNumber),
              let port = Int(portText), (1...65535).contains(port) else { return nil }
        self.host = host
        self.port = port
    }

    public var httpAddr: String { "\(host):\(port)" }

    public var origin: URL { URL(string: "http://\(httpAddr)")! }

    /// An absolute URL on this origin; path starts with "/".
    public func url(path: String) -> URL {
        URL(string: "http://\(httpAddr)\(path)")!
    }

    /// The login URL that trades a single-use code for the session cookie.
    public func loginURL(code: String) -> URL {
        var allowed = CharacterSet.alphanumerics
        allowed.insert(charactersIn: "-._~")
        let encoded = code.addingPercentEncoding(withAllowedCharacters: allowed) ?? ""
        return url(path: "/web/login?code=\(encoded)")
    }

    /// True when url is on this exact origin (scheme, host, port).
    public func isSameOrigin(_ url: URL) -> Bool {
        guard url.scheme?.lowercased() == "http", let h = url.host?.lowercased() else { return false }
        return h == host && (url.port ?? 80) == port
    }

    public static let `default` = DaemonAddress(httpAddr: AppIdentity.defaultHTTPAddr)!
}
