import XCTest
@testable import MenuBarCore

/// JSON of the Go route for the mockup scenario « Rythme trop fort » (now = 2026-10-20T21:00Z):
/// verdict `later`, blocked from 2026-10-22T14:40Z, freed by i at 2026-10-23T08:59Z.
let tooFastOverviewJSON = """
{"data":{"generated_at":"2026-10-20T21:00:00Z",
 "accounts":[
  {"account_uuid":"uuid-m","label":"m","in_verdict":true,"unusable":"","fetched_at":"2026-10-20T20:55:00Z",
   "seven_day":{"percent":55,"resets_at":"2026-10-25T17:59:00Z","full":false},
   "five_hour":{"percent":30,"resets_at":"2026-10-20T23:19:00Z","full":false},
   "scoped":[{"model":"Fable","percent":0,"resets_at":"2026-10-25T17:59:00Z","full":false}],
   "window_left":0.6,"fast":false,"refresh_failed":false,"history_since":"2026-10-19T21:00:00Z"},
  {"account_uuid":"uuid-i","label":"i","in_verdict":true,"unusable":"","fetched_at":"2026-10-20T20:55:00Z",
   "seven_day":{"percent":78,"resets_at":"2026-10-23T08:59:00.123Z","full":false},
   "five_hour":{"percent":50,"resets_at":"2026-10-20T23:29:00Z","full":false},
   "scoped":[],"window_left":0.35,"fast":true,"refresh_failed":false,"history_since":null},
  {"account_uuid":"uuid-j","label":"j","in_verdict":true,"unusable":"","fetched_at":"2026-10-20T20:55:00Z",
   "seven_day":{"percent":40,"resets_at":"2026-10-27T02:59:00Z","full":false},
   "five_hour":{"percent":12,"resets_at":"2026-10-21T00:40:00Z","full":false},
   "scoped":[],"window_left":0.9,"fast":false,"refresh_failed":true,"history_since":null}],
 "verdict":{"state":"later","provisional":true,"oldest_reading_at":"2026-10-20T20:55:00Z",
            "next_renewal":null,"blocked_from":"2026-10-22T14:40:00Z",
            "frees":{"label":"i","at":"2026-10-23T08:59:00Z"}},
 "next_session":[{"scope":"perso","display":"perso","master":true,"account_uuid":"uuid-i"},
                 {"scope":"globex","display":"gx","master":false,"error":"no-candidate"}],
 "a_future_key":1},"meta":{"request_id":"r"}}
"""

final class UsageWireTests: XCTestCase {
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

    private func read(_ json: String) async throws -> UsageOverview {
        StubURLProtocol.handler = { _ in (200, Data(json.utf8)) }
        return try await makeClient().usageOverview()
    }

    private func minimal(extra: String) -> String {
        """
        {"data":{"generated_at":"2026-10-20T21:00:00Z","accounts":[],"verdict":null,"next_session":[]\(extra)},"meta":{"request_id":"r"}}
        """
    }

    func testDecodesUnreadableCount() async throws {
        let two = try await read(minimal(extra: #","unreadable_count":2"#))
        XCTAssertEqual(two.unreadableCount, 2)
        let absent = try await read(minimal(extra: ""))
        XCTAssertEqual(absent.unreadableCount, 0, "omitted at 0 on the wire: absent = 0")
    }

    func testDecodesOverviewFixture() async throws {
        let o = try await read(tooFastOverviewJSON)
        XCTAssertEqual(StubURLProtocol.lastRequest?.url?.path, "/usage/overview")
        XCTAssertEqual(StubURLProtocol.lastRequest?.value(forHTTPHeaderField: "X-Pigtail-Scope"), DaemonClient.machineScope)
        XCTAssertEqual(o.accounts.map(\.label), ["m", "i", "j"])
        let m = o.accounts[0]
        XCTAssertEqual(m.accountUUID, "uuid-m")
        XCTAssertEqual(m.sevenDay?.percent, 55)
        XCTAssertEqual(m.scoped, [UsageScoped(model: "Fable", percent: 0,
                                              resetsAt: ISO8601DateFormatter().date(from: "2026-10-25T17:59:00Z"), full: false)])
        XCTAssertNotNil(m.historySince)
        let i = o.accounts[1]
        XCTAssertTrue(i.fast)
        XCTAssertNil(i.historySince)
        XCTAssertNotNil(i.sevenDay?.resetsAt, "fractional seconds decode")
        XCTAssertTrue(o.accounts[2].refreshFailed)
        let v = try XCTUnwrap(o.verdict)
        XCTAssertEqual(v.state, "later")
        XCTAssertTrue(v.provisional)
        XCTAssertNil(v.nextRenewal)
        XCTAssertEqual(v.frees?.label, "i")
        XCTAssertEqual(v.blockedFrom, ISO8601DateFormatter().date(from: "2026-10-22T14:40:00Z"))
        XCTAssertEqual(o.nextSession.count, 2)
        XCTAssertEqual(o.nextSession[0].accountUUID, "uuid-i")
        XCTAssertNil(o.nextSession[1].accountUUID)
        XCTAssertEqual(o.nextSession[1].error, "no-candidate")
        XCTAssertEqual(o.nextSession[1].display, "gx")
    }

    func testNullVerdictFieldsDecode() async throws {
        let o = try await read(minimal(extra: ""))
        XCTAssertNil(o.verdict)
        let json = """
        {"data":{"generated_at":"2026-10-20T21:00:00Z","accounts":[],
         "verdict":{"state":"none","provisional":false,"oldest_reading_at":null,"next_renewal":null,"blocked_from":null,"frees":null},
         "next_session":[]},"meta":{"request_id":"r"}}
        """
        let n = try await read(json)
        XCTAssertEqual(n.verdict, UsageVerdict(state: "none"))
    }

    func testUnknownStateStillDecodes() async throws {
        let json = """
        {"data":{"generated_at":"2026-10-20T21:00:00Z",
         "accounts":[{"account_uuid":"u","label":"m","in_verdict":true,"unusable":"a-new-reason","fetched_at":null,
                      "seven_day":null,"five_hour":null,"scoped":[],"window_left":null,"fast":false,
                      "refresh_failed":false,"history_since":null}],
         "verdict":{"state":"somethingnew","provisional":false,"oldest_reading_at":null,"next_renewal":null,"blocked_from":null,"frees":null},
         "next_session":[]},"meta":{"request_id":"r"}}
        """
        let o = try await read(json)
        XCTAssertEqual(o.verdict?.state, "somethingnew")
        XCTAssertEqual(o.accounts[0].unusable, "a-new-reason")
    }
}
