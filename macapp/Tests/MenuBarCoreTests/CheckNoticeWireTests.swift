import XCTest
@testable import MenuBarCore

final class CheckNoticeWireTests: XCTestCase {
    private func makeClient() -> DaemonClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubURLProtocol.self]
        return DaemonClient(baseURL: URL(string: "http://127.0.0.1:7800")!, token: "tok",
                            session: URLSession(configuration: config))
    }

    override func tearDown() {
        StubURLProtocol.handler = nil
        StubURLProtocol.lastRequest = nil
        super.tearDown()
    }

    /// The route is master-only and the bar sends no scope by default
    /// (default scope = globex = 403, silently): the request must carry the
    /// machine scope, like usageOverview.
    func testNoticeRequestCarriesMachineScope() async throws {
        StubURLProtocol.handler = { _ in (200, Data(#"{"data":{"launch_pid":42,"at":"2026-10-28T08:30:00Z","line":"x"},"meta":{"request_id":"r"}}"#.utf8)) }
        let r = try await makeClient().cmuxCheckNotice()
        XCTAssertEqual(StubURLProtocol.lastRequest?.url?.path, "/cmux/check/notice")
        XCTAssertEqual(StubURLProtocol.lastRequest?.value(forHTTPHeaderField: "X-Pigtail-Scope"), DaemonClient.machineScope)
        XCTAssertEqual(r.launchPID, 42)
        XCTAssertEqual(r.line, "x")
    }

    /// The event stream asks for cmux.check_notice with the machine scope.
    func testNoticeEventsRequestCarriesTypeAndScope() async {
        StubURLProtocol.handler = { _ in (200, Data()) }
        do {
            for try await _ in makeClient().checkNoticeEvents() { break }
        } catch {}
        let req = StubURLProtocol.lastRequest
        XCTAssertEqual(req?.url?.query, "types=cmux.check_notice")
        XCTAssertEqual(req?.value(forHTTPHeaderField: "X-Pigtail-Scope"), DaemonClient.machineScope)
    }
}
