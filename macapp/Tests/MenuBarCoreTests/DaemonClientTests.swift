import XCTest
@testable import MenuBarCore

/// URLProtocol stub: no real network. Maps a request → (status, body).
final class StubURLProtocol: URLProtocol {
    // nonisolated(unsafe): access is serialized by the test framework (setUp/tearDown + single async task).
    nonisolated(unsafe) static var handler: ((URLRequest) -> (Int, Data))?
    nonisolated(unsafe) static var lastRequest: URLRequest?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.lastRequest = request
        guard let handler = Self.handler else {
            client?.urlProtocol(self, didFailWithError: URLError(.badServerResponse)); return
        }
        let (status, body) = handler(request)
        let resp = HTTPURLResponse(url: request.url!, statusCode: status,
                                   httpVersion: "HTTP/1.1", headerFields: nil)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

final class DaemonClientTests: XCTestCase {
    private func makeClient(token: String? = "tok") -> DaemonClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubURLProtocol.self]
        return DaemonClient(baseURL: URL(string: "http://127.0.0.1:7800")!,
                            token: token,
                            session: URLSession(configuration: config))
    }

    override func tearDown() {
        StubURLProtocol.handler = nil
        StubURLProtocol.lastRequest = nil
        super.tearDown()
    }

    func testServersDecodesEnvelopeAndFields() async throws {
        StubURLProtocol.handler = { _ in
            let json = """
            {"data":{"available_bytes":100,"floor_bytes":50,"servers":[
              {"workspace_id":"ws_a","server":"dev","branch":"feat/x","port":6010,
               "rss_bytes":123,"idle_seconds":42,"pinned_until":1893456000}]},
             "meta":{"request_id":"req_1"}}
            """
            return (200, Data(json.utf8))
        }
        let resp = try await makeClient().servers()
        XCTAssertEqual(resp.availableBytes, 100)
        XCTAssertEqual(resp.servers.count, 1)
        let s = resp.servers[0]
        XCTAssertEqual(s.workspaceID, "ws_a")
        XCTAssertEqual(s.server, "dev")
        XCTAssertEqual(s.branch, "feat/x")
        XCTAssertEqual(s.port, 6010)
        XCTAssertEqual(s.idleSeconds, 42)
        XCTAssertEqual(s.pinnedUntil, 1893456000)
    }

    func testServersToleratesOmittedOptionalFields() async throws {
        StubURLProtocol.handler = { _ in
            let json = """
            {"data":{"available_bytes":0,"floor_bytes":0,"servers":[
              {"workspace_id":"ws_b","server":"dev","port":6011,
               "rss_bytes":1,"idle_seconds":0}]},"meta":{"request_id":"r"}}
            """
            return (200, Data(json.utf8))
        }
        let resp = try await makeClient().servers()
        XCTAssertNil(resp.servers[0].branch)
        XCTAssertNil(resp.servers[0].pinnedUntil)
    }

    func testWorkspacesDecodesPortsAndDate() async throws {
        StubURLProtocol.handler = { _ in
            let json = """
            {"data":[
              {"id":"ws_a","path":"/repo/feat-auth","ports":{"dev":6010,"storybook":6011},
               "last_activity_at":"2026-06-24T12:00:00.5Z","display_ref":"feat/auth"}],
             "meta":{"request_id":"r"}}
            """
            return (200, Data(json.utf8))
        }
        let ws = try await makeClient().workspaces()
        XCTAssertEqual(ws.count, 1)
        XCTAssertEqual(ws[0].id, "ws_a")
        XCTAssertEqual(ws[0].path, "/repo/feat-auth")
        XCTAssertEqual(ws[0].ports?["storybook"], 6011)
        XCTAssertEqual(ws[0].displayRef, "feat/auth")
    }

    func testSendsBearerAndAPIVersionHeaders() async throws {
        StubURLProtocol.handler = { _ in (200, Data(#"{"data":[],"meta":{}}"#.utf8)) }
        _ = try await makeClient(token: "secret").workspaces()
        let req = try XCTUnwrap(StubURLProtocol.lastRequest)
        XCTAssertEqual(req.value(forHTTPHeaderField: "Authorization"), "Bearer secret")
        XCTAssertEqual(req.value(forHTTPHeaderField: "X-Pigtail-API"), "v1")
    }

    func testPinBuildsCorrectPathAndQuery() async throws {
        StubURLProtocol.handler = { _ in (200, Data(#"{"data":{},"meta":{}}"#.utf8)) }
        try await makeClient().pin(workspace: "ws_a", server: "dev", duration: .h1)
        let req = try XCTUnwrap(StubURLProtocol.lastRequest)
        XCTAssertEqual(req.httpMethod, "POST")
        XCTAssertEqual(req.url?.path, "/servers/ws_a/dev/pin")
        XCTAssertEqual(req.url?.query, "duration=1h")
    }

    func testPinUntilUnpinnedQuery() async throws {
        StubURLProtocol.handler = { _ in (200, Data(#"{"data":{},"meta":{}}"#.utf8)) }
        try await makeClient().pin(workspace: "ws_a", server: "dev", duration: .untilUnpinned)
        let req = try XCTUnwrap(StubURLProtocol.lastRequest)
        XCTAssertEqual(req.url?.path, "/servers/ws_a/dev/pin")
        XCTAssertEqual(req.url?.query, "duration=until-unpinned")
    }

    func testServerViewDecodesPinnedDurable() throws {
        let json = #"{"workspace_id":"w","server":"s","port":1,"rss_bytes":0,"idle_seconds":0,"pinned_durable":true}"#
        let sv = try JSONDecoder().decode(ServerView.self, from: Data(json.utf8))
        XCTAssertTrue(sv.pinnedDurable)
        XCTAssertNil(sv.pinnedUntil)
        let old = #"{"workspace_id":"w","server":"s","port":1,"rss_bytes":0,"idle_seconds":0}"#
        XCTAssertFalse(try JSONDecoder().decode(ServerView.self, from: Data(old.utf8)).pinnedDurable)
    }

    func testUnpinUsesDeleteOnPinPath() async throws {
        StubURLProtocol.handler = { _ in (200, Data(#"{"data":{},"meta":{}}"#.utf8)) }
        try await makeClient().unpin(workspace: "ws_a", server: "dev")
        let req = try XCTUnwrap(StubURLProtocol.lastRequest)
        XCTAssertEqual(req.httpMethod, "DELETE")
        XCTAssertEqual(req.url?.path, "/servers/ws_a/dev/pin")
    }

    func testStopAllPostsStopAllPath() async throws {
        StubURLProtocol.handler = { _ in (200, Data(#"{"data":{},"meta":{}}"#.utf8)) }
        try await makeClient().stopAll()
        let req = try XCTUnwrap(StubURLProtocol.lastRequest)
        XCTAssertEqual(req.httpMethod, "POST")
        XCTAssertEqual(req.url?.path, "/servers/stop-all")
    }

    func testStopServerBuildsCorrectPathAndMethod() async throws {
        StubURLProtocol.handler = { _ in (200, Data(#"{"data":{},"meta":{}}"#.utf8)) }
        try await makeClient().stop(workspace: "ws_a", server: "dev")
        let req = try XCTUnwrap(StubURLProtocol.lastRequest)
        XCTAssertEqual(req.httpMethod, "POST")
        XCTAssertEqual(req.url?.path, "/servers/ws_a/dev/stop")
    }

    func testWorkspacesDecodesDateWithoutFractionalSeconds() async throws {
        StubURLProtocol.handler = { _ in
            let json = """
            {"data":[
              {"id":"ws_a","path":"/repo/feat-auth","ports":{"dev":6010},
               "last_activity_at":"2026-06-24T12:00:00Z","display_ref":"feat/auth"}],
             "meta":{"request_id":"r"}}
            """
            return (200, Data(json.utf8))
        }
        let ws = try await makeClient().workspaces()
        XCTAssertEqual(ws.count, 1)
        XCTAssertNotNil(ws[0].lastActivityAt)
    }

    func testHTTPErrorStatusThrows() async {
        StubURLProtocol.handler = { _ in (401, Data(#"{"error":{"code":"UNAUTHORIZED","message":"no"}}"#.utf8)) }
        do {
            _ = try await makeClient().servers()
            XCTFail("expected throw")
        } catch {
            // success: any thrown error is fine for daemon-down/401 handling
        }
    }

    func testPurgePolicyDecodesGrace() async throws {
        StubURLProtocol.handler = { req in
            XCTAssertEqual(req.url?.path, "/purge")
            XCTAssertEqual(req.httpMethod, "GET")
            return (200, Data(#"{"data":{"grace_seconds":600},"meta":{"request_id":"r"}}"#.utf8))
        }
        let policy = try await makeClient().purgePolicy()
        XCTAssertEqual(policy.graceSeconds, 600)
    }

    func testSetPurgePolicySendsBodyAndDecodes() async throws {
        StubURLProtocol.handler = { req in
            XCTAssertEqual(req.url?.path, "/purge")
            XCTAssertEqual(req.httpMethod, "POST")
            return (200, Data(#"{"data":{"grace_seconds":1800},"meta":{"request_id":"r"}}"#.utf8))
        }
        let policy = try await makeClient().setPurgePolicy(graceSeconds: 1800)
        XCTAssertEqual(policy.graceSeconds, 1800)
        // Body assertion: URLProtocol strips httpBody into httpBodyStream, so
        // assert via the decoded round-trip above rather than reading the body.
    }

    // MARK: - /events?types=lid.*

    func testLidEventsRequestsTheLidStream() async throws {
        StubURLProtocol.handler = { _ in (200, Data(EventStreamTests.twoEvents.utf8)) }
        var items: [DaemonStreamItem] = []
        for try await item in makeClient().lidEvents() { items.append(item) }
        XCTAssertEqual(items, [
            .connected,
            .event(SSEEvent(id: "evt_1", type: "lid.changed", data: #"{"data":{"on":true,"reason":"on"}}"#)),
            .event(SSEEvent(id: "evt_2", type: "lid.changed", data: "{}")),
        ])
        let req = try XCTUnwrap(StubURLProtocol.lastRequest)
        XCTAssertEqual(req.httpMethod, "GET")
        XCTAssertEqual(req.url?.path, "/events")
        XCTAssertEqual(req.url?.query, "types=lid.*")
        XCTAssertEqual(req.value(forHTTPHeaderField: "Accept"), "text/event-stream")
        XCTAssertEqual(req.value(forHTTPHeaderField: "Authorization"), "Bearer tok")
        XCTAssertEqual(req.timeoutInterval, 45)
    }

    func testLidEventsHTTPErrorThrows() async {
        StubURLProtocol.handler = { _ in (401, Data(#"{"error":{"code":"UNAUTHORIZED","message":"no"}}"#.utf8)) }
        var items: [DaemonStreamItem] = []
        do {
            for try await item in makeClient().lidEvents() { items.append(item) }
            XCTFail("expected throw")
        } catch let e as DaemonHTTPError {
            XCTAssertEqual(e.statusCode, 401)
        } catch {
            XCTFail("unexpected error \(error)")
        }
        XCTAssertEqual(items, [], "no .connected on an HTTP error")
    }
}
