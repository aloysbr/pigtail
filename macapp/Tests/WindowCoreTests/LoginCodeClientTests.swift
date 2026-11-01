import XCTest
@testable import WindowCore

final class LoginCodeClientTests: XCTestCase {
    private var server: TestUnixServer?

    override func tearDown() {
        server?.stop()
        server = nil
        super.tearDown()
    }

    private func serve(_ b: TestUnixServer.Behaviour) throws -> TestUnixServer {
        let s = try TestUnixServer(b)
        server = s
        return s
    }

    private func mintError(_ client: LoginCodeClient, file: StaticString = #filePath, line: UInt = #line) async -> LoginCodeError? {
        do {
            let code = try await client.mint()
            XCTFail("expected an error, got \(code)", file: file, line: line)
            return nil
        } catch {
            return error // typed throws: always a LoginCodeError
        }
    }

    private let okBody = #"{"code":"abc","http_addr":"127.0.0.1:7800"}"#

    func testOKContentLength() async throws {
        let s = try serve(.respond(TestUnixServer.http(status: 200, reason: "OK", body: okBody)))
        let code = try await LoginCodeClient(socketPath: s.path, timeout: 2).mint()
        XCTAssertEqual(code, LoginCode(code: "abc", httpAddr: "127.0.0.1:7800"))
        XCTAssertTrue(s.received.hasPrefix("POST /web/login-code HTTP/1.1\r\n"), s.received)
        XCTAssertTrue(s.received.contains("\r\nContent-Length: 0\r\n"), s.received)
    }

    func testOKDaemonEnvelope() async throws {
        // What pigtaild really sends (writeJSON): the answer under "data", with "meta".
        let body = #"{"data":{"code":"abc","http_addr":"127.0.0.1:7800"},"meta":{"request_id":"req_1"}}"#
        let s = try serve(.respond(TestUnixServer.http(status: 200, reason: "OK", body: body)))
        let code = try await LoginCodeClient(socketPath: s.path, timeout: 2).mint()
        XCTAssertEqual(code, LoginCode(code: "abc", httpAddr: "127.0.0.1:7800"))
    }

    func testEnvelopeWithoutCodeIsMalformed() async throws {
        let body = #"{"data":{"http_addr":"127.0.0.1:7800"},"meta":{}}"#
        let s = try serve(.respond(TestUnixServer.http(status: 200, reason: "OK", body: body)))
        let e = await mintError(LoginCodeClient(socketPath: s.path, timeout: 2))
        XCTAssertEqual(e, .malformed)
    }

    func testOKChunked() async throws {
        let s = try serve(.respond(TestUnixServer.http(status: 200, reason: "OK", body: okBody, chunked: true)))
        let code = try await LoginCodeClient(socketPath: s.path, timeout: 2).mint()
        XCTAssertEqual(code, LoginCode(code: "abc", httpAddr: "127.0.0.1:7800"))
    }

    func testConflictIsHTTPOff() async throws {
        let s = try serve(.respond(TestUnixServer.http(status: 409, reason: "Conflict", body: #"{"error":"x"}"#)))
        let e = await mintError(LoginCodeClient(socketPath: s.path, timeout: 2))
        XCTAssertEqual(e, .httpOff)
    }

    func testForbidden() async throws {
        let s = try serve(.respond(TestUnixServer.http(status: 403, reason: "Forbidden", body: "{}")))
        let e = await mintError(LoginCodeClient(socketPath: s.path, timeout: 2))
        XCTAssertEqual(e, .forbidden)
    }

    func testServerError() async throws {
        let s = try serve(.respond(TestUnixServer.http(status: 500, reason: "Internal Server Error", body: "{}")))
        let e = await mintError(LoginCodeClient(socketPath: s.path, timeout: 2))
        XCTAssertEqual(e, .server(500))
    }

    func testMissingSocketIsUnreachable() async {
        let e = await mintError(LoginCodeClient(socketPath: "/tmp/pigtail-no-such-dir-\(UUID().uuidString.prefix(6))/p.sock", timeout: 2))
        XCTAssertEqual(e, .daemonUnreachable)
    }

    func testClosedWithoutReplyIsUnreachable() async throws {
        let s = try serve(.closeWithoutReply)
        let e = await mintError(LoginCodeClient(socketPath: s.path, timeout: 2))
        XCTAssertEqual(e, .daemonUnreachable)
    }

    func testSilentServerTimesOut() async throws {
        let s = try serve(.silent)
        let started = Date()
        let e = await mintError(LoginCodeClient(socketPath: s.path, timeout: 0.3))
        XCTAssertEqual(e, .timeout)
        XCTAssertLessThan(Date().timeIntervalSince(started), 2.5)
    }

    func testJSONWithoutCodeIsMalformed() async throws {
        let s = try serve(.respond(TestUnixServer.http(status: 200, reason: "OK", body: #"{"http_addr":"127.0.0.1:7800"}"#)))
        let e = await mintError(LoginCodeClient(socketPath: s.path, timeout: 2))
        XCTAssertEqual(e, .malformed)
    }
}
