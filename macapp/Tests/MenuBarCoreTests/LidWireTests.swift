import XCTest
@testable import MenuBarCore

final class LidWireTests: XCTestCase {
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

    /// The JSON body of the last request: URLSession sometimes moves it into the stream.
    private func lastBody() throws -> [String: Any] {
        let req = try XCTUnwrap(StubURLProtocol.lastRequest)
        var data = req.httpBody
        if data == nil, let stream = req.httpBodyStream {
            var out = Data()
            stream.open()
            defer { stream.close() }
            var buf = [UInt8](repeating: 0, count: 1024)
            while stream.hasBytesAvailable {
                let n = stream.read(&buf, maxLength: buf.count)
                if n <= 0 { break }
                out.append(buf, count: n)
            }
            data = out
        }
        let body = try XCTUnwrap(data, "no request body")
        return try XCTUnwrap(try JSONSerialization.jsonObject(with: body) as? [String: Any])
    }

    func testLidDecodesFullAndMinimalPayloads() async throws {
        StubURLProtocol.handler = { _ in (200, Data(#"{"data":{"sleep_disabled":true,"deadline":"2026-10-16T23:00:00+02:00","battery_floor":25,"last_off":{"at":"2026-10-16T03:06:00+02:00","reason":"deadline"},"last_error":"x"},"meta":{}}"#.utf8)) }
        let r = try await makeClient().lid()
        XCTAssertEqual(StubURLProtocol.lastRequest?.url?.path, "/lid")
        XCTAssertEqual(StubURLProtocol.lastRequest?.httpMethod, "GET")
        XCTAssertEqual(StubURLProtocol.lastRequest?.timeoutInterval, 95, "le GET /lid aussi attend jusqu'à 95 s (défaut URLSession 60)")
        XCTAssertTrue(r.sleepDisabled); XCTAssertEqual(r.batteryFloor, 25); XCTAssertEqual(r.lastError, "x")
        XCTAssertEqual(r.deadline, ISO8601DateFormatter().date(from: "2026-10-16T21:00:00Z"))
        XCTAssertEqual(r.lastOff?.reason, "deadline")
        StubURLProtocol.handler = { _ in (200, Data(#"{"data":{"sleep_disabled":false,"battery_floor":25},"meta":{}}"#.utf8)) }
        let m = try await makeClient().lid()
        XCTAssertFalse(m.sleepDisabled); XCTAssertNil(m.deadline); XCTAssertNil(m.lastOff); XCTAssertNil(m.lastError)
        XCTAssertNil(m.guardError); XCTAssertNil(m.thermalState)
    }

    func testLidDecodesGuardErrorAndThermalState() async throws {
        StubURLProtocol.handler = { _ in (200, Data(#"{"data":{"sleep_disabled":true,"battery_floor":25,"guard_error":"lid reading failed: x","thermal_state":"serious","lid_closed":true},"meta":{}}"#.utf8)) }
        let r = try await makeClient().lid()
        XCTAssertEqual(r.guardError, "lid reading failed: x")
        XCTAssertEqual(r.thermalState, "serious")
    }

    func testSetLidPostsOnAndHoursWithALongTimeout() async throws {
        StubURLProtocol.handler = { _ in (200, Data(#"{"data":{"sleep_disabled":true,"battery_floor":25},"meta":{}}"#.utf8)) }
        let r = try await makeClient().setLid(on: true, hours: 4)
        XCTAssertTrue(r.sleepDisabled)
        XCTAssertEqual(StubURLProtocol.lastRequest?.url?.path, "/lid")
        XCTAssertEqual(StubURLProtocol.lastRequest?.httpMethod, "POST")
        XCTAssertEqual(StubURLProtocol.lastRequest?.timeoutInterval, 95)
        let on = try lastBody()
        XCTAssertEqual(on as NSDictionary, ["on": true, "hours": 4] as NSDictionary)

        StubURLProtocol.handler = { _ in (200, Data(#"{"data":{"sleep_disabled":false,"battery_floor":25},"meta":{}}"#.utf8)) }
        _ = try await makeClient().setLid(on: false, hours: nil)
        XCTAssertEqual(StubURLProtocol.lastRequest?.timeoutInterval, 95)
        let off = try lastBody()
        XCTAssertEqual(off as NSDictionary, ["on": false] as NSDictionary)
    }

    private func mapped(_ status: Int, _ body: String) async -> LidActionError? {
        StubURLProtocol.handler = { _ in (status, Data(body.utf8)) }
        do {
            _ = try await makeClient().setLid(on: true, hours: 1)
            return nil
        } catch {
            return LidActionError.from(error)
        }
    }

    func testNotSetUpAndOtherErrorsMap() async throws {
        let notSetUp = await mapped(412, #"{"error":{"code":"LID_NOT_SET_UP","message":"lid: not set up"}}"#)
        XCTAssertEqual(notSetUp, .notSetUp)
        let unavailable = await mapped(501, #"{"error":{"code":"LID_UNAVAILABLE","message":"lid: sleep control is only available on macOS"}}"#)
        XCTAssertEqual(unavailable, .unavailable)
        let failed = await mapped(500, #"{"error":{"code":"INTERNAL","message":"pmset -g failed"}}"#)
        XCTAssertEqual(failed, .failed("pmset -g failed"))
        XCTAssertEqual(LidActionError.from(URLError(.cannotConnectToHost)), .unreachable)
        let decode = await mapped(200, #"{"data":{"sleep_disabled":"oui"},"meta":{}}"#)
        guard case .failed(let m)? = decode else {
            return XCTFail("décodage raté → .failed, jamais .unreachable : \(String(describing: decode))")
        }
        XCTAssertFalse(m.isEmpty)
    }
}
