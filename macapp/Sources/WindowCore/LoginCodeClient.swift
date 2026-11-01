import Foundation

/// A single-use web login code and the daemon's HTTP address
/// (POST /web/login-code, pkg/pigtail/web.go WebLoginCode).
public struct LoginCode: Equatable, Sendable {
    public let code: String
    public let httpAddr: String

    public init(code: String, httpAddr: String) {
        self.code = code
        self.httpAddr = httpAddr
    }
}

public enum LoginCodeError: Error, Equatable, Sendable {
    /// No daemon on the socket (absent, refused, closed without a reply).
    case daemonUnreachable
    /// The daemon did not answer in time.
    case timeout
    /// 409: pigtaild runs without an HTTP listener (--http "").
    case httpOff
    /// 403.
    case forbidden
    /// Any other non-200 status.
    case server(Int)
    /// A 200 whose body is not a login code, or bytes that are not HTTP.
    case malformed
}

/// Something that can ask pigtaild for a login code (the socket client, or a
/// fake in tests).
public protocol LoginCodeMinting: Sendable {
    func mint() async throws(LoginCodeError) -> LoginCode
}

/// Asks pigtaild for a login code on its Unix socket, like `pigtail web`.
public struct LoginCodeClient: LoginCodeMinting {
    public let socketPath: String
    public let timeout: TimeInterval

    public init(socketPath: String, timeout: TimeInterval = 3) {
        self.socketPath = socketPath
        self.timeout = timeout
    }

    public func mint() async throws(LoginCodeError) -> LoginCode {
        let resp: UnixHTTPResponse
        do {
            resp = try await UnixHTTP.post(path: socketPath, requestPath: "/web/login-code", timeout: timeout)
        } catch UnixHTTPError.timeout {
            throw .timeout
        } catch UnixHTTPError.unparseable {
            throw .malformed
        } catch {
            throw .daemonUnreachable
        }
        switch resp.status {
        case 200:
            return try Self.decode(resp.body)
        case 409: throw .httpOff
        case 403: throw .forbidden
        default: throw .server(resp.status)
        }
    }

    /// pigtaild answers `{"data": {...}, "meta": {...}}` (writeJSON); a bare
    /// object is read too.
    static func decode(_ body: Data) throws(LoginCodeError) -> LoginCode {
        struct Wire: Decodable {
            let code: String?
            let http_addr: String?
        }
        struct Envelope: Decodable {
            let data: Wire?
        }
        let decoder = JSONDecoder()
        let enveloped = (try? decoder.decode(Envelope.self, from: body))?.data
        guard let w = enveloped ?? (try? decoder.decode(Wire.self, from: body)),
              let code = w.code, !code.isEmpty,
              let addr = w.http_addr else {
            throw .malformed
        }
        return LoginCode(code: code, httpAddr: addr)
    }
}
