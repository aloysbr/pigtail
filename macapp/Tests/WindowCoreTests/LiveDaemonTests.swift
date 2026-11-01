import XCTest
@testable import WindowCore

/// Live check against an isolated pigtaild, never the real one:
///
///     make build
///     S=$(mktemp -d /tmp/pcliveXXXX)
///     ./bin/pigtaild --socket "$S/p.sock" --db "$S/p.db" --http 127.0.0.1:<free port> &
///     PIGTAIL_LIVE_SOCKET="$S/p.sock" PIGTAIL_LIVE_HTTP=127.0.0.1:<port> \
///       swift test --package-path macapp --filter LiveDaemonTests
///
/// Skipped without both variables.
final class LiveDaemonTests: XCTestCase {
    private final class NoRedirect: NSObject, URLSessionTaskDelegate {
        func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                        newRequest request: URLRequest) async -> URLRequest? { nil }
    }

    private func login(_ address: DaemonAddress, code: String) async throws -> HTTPURLResponse {
        let session = URLSession(configuration: SessionProbe.makeConfiguration())
        let (_, resp) = try await session.data(for: URLRequest(url: address.loginURL(code: code)), delegate: NoRedirect())
        return try XCTUnwrap(resp as? HTTPURLResponse)
    }

    func testLoginCodeExchangeAndProbe() async throws {
        let env = ProcessInfo.processInfo.environment
        guard let socket = env["PIGTAIL_LIVE_SOCKET"], !socket.isEmpty,
              let http = env["PIGTAIL_LIVE_HTTP"], !http.isEmpty else {
            throw XCTSkip("PIGTAIL_LIVE_SOCKET and PIGTAIL_LIVE_HTTP unset")
        }

        // A code on the socket, with no scope header: the route takes it as is.
        let code = try await LoginCodeClient(socketPath: socket, timeout: 3).mint()
        XCTAssertEqual(code.httpAddr, http)
        let address = try XCTUnwrap(DaemonAddress(httpAddr: code.httpAddr))

        // The exchange: 303 to / with the session cookie.
        let resp = try await login(address, code: code.code)
        XCTAssertEqual(resp.statusCode, 303)
        let setCookie = try XCTUnwrap(resp.value(forHTTPHeaderField: "Set-Cookie"))
        XCTAssertTrue(setCookie.hasPrefix("pigtail_web="), setCookie)
        let cookie = String(setCookie.dropFirst("pigtail_web=".count).prefix { $0 != ";" })
        XCTAssertFalse(cookie.isEmpty)

        // The probe tells a valid cookie from none and from a forged one.
        let probe = SessionProbe()
        let valid = await probe.check(address: address, cookieValue: cookie)
        XCTAssertEqual(valid, .valid)
        let none = await probe.check(address: address, cookieValue: nil)
        XCTAssertEqual(none, .invalid)
        let forged = await probe.check(address: address, cookieValue: "forged-" + cookie)
        XCTAssertEqual(forged, .invalid)

        // Single use: the same code again is gone.
        let again = try await login(address, code: code.code)
        XCTAssertEqual(again.statusCode, 410)
    }
}
