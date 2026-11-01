import XCTest
@testable import MenuBarCore

final class EventStreamTests: XCTestCase {
    /// Feeds `s` one byte at a time, as the reader task does.
    private func parse(_ s: String) -> [SSEEvent] {
        var p = SSEParser()
        var out: [SSEEvent] = []
        for b in Array(s.utf8) {
            if let e = p.feed(byte: b) { out.append(e) }
        }
        return out
    }

    static let twoEvents = "id: evt_1\nevent: lid.changed\ndata: {\"data\":{\"on\":true,\"reason\":\"on\"}}\n\n"
        + ": ping\n\n"
        + "id: evt_2\nevent: lid.changed\ndata: {}\n\n"

    func testBlankLineDispatchesEvents() {
        let events = parse(Self.twoEvents)
        XCTAssertEqual(events, [
            SSEEvent(id: "evt_1", type: "lid.changed", data: #"{"data":{"on":true,"reason":"on"}}"#),
            SSEEvent(id: "evt_2", type: "lid.changed", data: "{}"),
        ])
    }

    func testNoDispatchWithoutBlankLine() {
        XCTAssertEqual(parse("id: evt_1\nevent: lid.changed\ndata: {}\n"), [])
    }

    func testCRLFIsAccepted() {
        let crlf = Self.twoEvents.replacingOccurrences(of: "\n", with: "\r\n")
        XCTAssertEqual(parse(crlf), parse(Self.twoEvents))
        XCTAssertEqual(parse(crlf).count, 2)
    }

    func testMultibyteUTF8AcrossFeeds() {
        let events = parse("event: lid.changed\ndata: {\"reason\":\"é\"}\n\n")
        XCTAssertEqual(events.map(\.data), [#"{"reason":"é"}"#])
    }

    func testReasonIsDecoded() {
        XCTAssertEqual(SSEEvent(id: nil, type: "lid.changed",
                                data: #"{"data":{"on":false,"reason":"battery 10%"}}"#).lidReason, "battery 10%")
        XCTAssertNil(SSEEvent(id: nil, type: "lid.changed", data: #"{"data":{"on":true}}"#).lidReason)
        XCTAssertNil(SSEEvent(id: nil, type: "lid.changed", data: "not json").lidReason)
    }
}
