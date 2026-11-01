import XCTest
@testable import WindowCore

/// URLProtocol stub for the probe: no real network.
final class ProbeStubProtocol: URLProtocol {
    // nonisolated(unsafe): serialized by the test framework (one async test at a time).
    nonisolated(unsafe) static var handler: ((URLRequest) -> Result<Int, URLError>)?
    nonisolated(unsafe) static var lastRequest: URLRequest?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.lastRequest = request
        switch Self.handler?(request) ?? .failure(URLError(.badServerResponse)) {
        case .success(let status):
            let resp = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: nil)!
            client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: Data())
            client?.urlProtocolDidFinishLoading(self)
        case .failure(let err):
            client?.urlProtocol(self, didFailWithError: err)
        }
    }
    override func stopLoading() {}
}

final class SessionProbeTests: XCTestCase {
    private let address = DaemonAddress(httpAddr: "127.0.0.1:7800")!

    override func tearDown() {
        ProbeStubProtocol.handler = nil
        ProbeStubProtocol.lastRequest = nil
        super.tearDown()
    }

    private func probe(_ result: Result<Int, URLError>, cookie: String? = "secret") async -> ProbeOutcome {
        ProbeStubProtocol.handler = { _ in result }
        let config = SessionProbe.makeConfiguration()
        config.protocolClasses = [ProbeStubProtocol.self]
        return await SessionProbe(session: URLSession(configuration: config)).check(address: address, cookieValue: cookie)
    }

    func testRequestShape() async throws {
        _ = await probe(.success(404))
        let req = try XCTUnwrap(ProbeStubProtocol.lastRequest)
        XCTAssertEqual(req.httpMethod, "GET")
        XCTAssertEqual(req.url?.absoluteString, "http://127.0.0.1:7800/assets/_probe")
        XCTAssertEqual(req.value(forHTTPHeaderField: "Cookie"), "pigtail_web=secret")
    }

    func testNoCookieSendsNoHeader() async throws {
        _ = await probe(.success(401), cookie: nil)
        let req = try XCTUnwrap(ProbeStubProtocol.lastRequest)
        XCTAssertNil(req.value(forHTTPHeaderField: "Cookie"))
    }

    func testTable() async {
        let o401 = await probe(.success(401)); XCTAssertEqual(o401, .invalid)
        let o404 = await probe(.success(404)); XCTAssertEqual(o404, .valid)
        let o200 = await probe(.success(200)); XCTAssertEqual(o200, .valid)
        let o403 = await probe(.success(403)); XCTAssertEqual(o403, .hostRefused)
        let o500 = await probe(.success(500)); XCTAssertEqual(o500, .unexpected(500))
        let oRef = await probe(.failure(URLError(.cannotConnectToHost))); XCTAssertEqual(oRef, .unreachable)
        let oTO = await probe(.failure(URLError(.timedOut))); XCTAssertEqual(oTO, .unreachable)
    }

    func testConfigurationKeepsNoCookies() {
        let c = SessionProbe.makeConfiguration()
        XCTAssertFalse(c.httpShouldSetCookies)
        XCTAssertNil(c.httpCookieStorage)
        XCTAssertEqual(c.timeoutIntervalForRequest, 3)
    }
}
