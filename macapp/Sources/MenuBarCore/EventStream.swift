import Foundation

/// One Server-Sent Event of the daemon's `/events` stream.
public struct SSEEvent: Equatable, Sendable {
    public let id: String?
    public let type: String
    /// The `data:` payload: pigtail's event envelope `{id, type, timestamp, data}`.
    public let data: String

    public init(id: String?, type: String, data: String) {
        self.id = id
        self.type = type
        self.data = data
    }

    /// `data.reason` of a pigtail event envelope, nil if absent.
    public var lidReason: String? {
        guard let obj = try? JSONSerialization.jsonObject(with: Data(data.utf8)) as? [String: Any],
              let inner = obj["data"] as? [String: Any] else { return nil }
        return inner["reason"] as? String
    }
}

public enum DaemonStreamItem: Equatable, Sendable {
    case connected            // 200 received: the subscription exists
    case event(SSEEvent)
}

/// Synchronous SSE parser, fed one byte at a time by whoever owns the bytes
/// (no AsyncSequence, no inner Task: nothing non-Sendable crosses an
/// isolation boundary under swift-tools 6.0 strict concurrency).
/// Splits on "\n" itself, strips a trailing "\r", keeps EMPTY lines
/// (URLSession.AsyncBytes.lines drops them — never use it here).
public struct SSEParser: Sendable {
    private var lineBuffer: [UInt8] = []
    private var id: String?
    private var type: String?
    private var data: [String] = []

    public init() {}

    /// Returns the event the byte completes (on the blank line), else nil.
    public mutating func feed(byte: UInt8) -> SSEEvent? {
        guard byte == UInt8(ascii: "\n") else {
            lineBuffer.append(byte)
            return nil
        }
        if lineBuffer.last == UInt8(ascii: "\r") { lineBuffer.removeLast() }
        // UTF-8 is decoded per line, never per byte: a multibyte character
        // split across reads stays whole.
        let line = String(decoding: lineBuffer, as: UTF8.self)
        lineBuffer.removeAll(keepingCapacity: true)
        return feed(line: line)
    }

    /// "" dispatches; ":" comment; id/event/data fields.
    mutating func feed(line: String) -> SSEEvent? {
        if line.isEmpty {
            defer { id = nil; type = nil; data = [] }
            guard !data.isEmpty || type != nil else { return nil }
            return SSEEvent(id: id, type: type ?? "message", data: data.joined(separator: "\n"))
        }
        if line.hasPrefix(":") { return nil }
        let field: Substring
        var value: Substring
        if let colon = line.firstIndex(of: ":") {
            field = line[..<colon]
            value = line[line.index(after: colon)...]
            if value.hasPrefix(" ") { value = value.dropFirst() }
        } else {
            field = Substring(line)
            value = ""
        }
        switch field {
        case "id": id = String(value)
        case "event": type = String(value)
        case "data": data.append(String(value))
        default: break
        }
        return nil
    }
}

public protocol LidEventSource: Sendable {
    /// GET /events?types=lid.* — ends normally when the daemon closes the
    /// stream, throws on a transport or HTTP error.
    func lidEvents() -> AsyncThrowingStream<DaemonStreamItem, Error>
}
