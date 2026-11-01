import Foundation
import Network

/// A parsed HTTP/1.1 response.
public struct UnixHTTPResponse: Sendable, Equatable {
    public let status: Int
    public let headers: [String: String] // lower-cased names
    public let body: Data
}

public enum UnixHTTPError: Error, Equatable, Sendable {
    /// No connection, or the peer closed without a byte.
    case unreachable
    /// No complete answer within the timeout.
    case timeout
    /// Bytes came back but they are not an HTTP response.
    case unparseable
}

/// A minimal HTTP/1.1 client over a Unix socket: one request, read until the
/// peer closes (Connection: close), parse status, headers and a
/// Content-Length or chunked body.
public enum UnixHTTP {
    public static func post(path socketPath: String, requestPath: String, timeout: TimeInterval) async throws -> UnixHTTPResponse {
        let request = "POST \(requestPath) HTTP/1.1\r\nHost: pigtail\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
        let raw = try await exchange(socketPath: socketPath, request: Data(request.utf8), timeout: timeout)
        guard !raw.isEmpty else { throw UnixHTTPError.unreachable }
        guard let resp = parse(raw) else { throw UnixHTTPError.unparseable }
        return resp
    }

    /// Sends request and collects every byte until the peer closes. Resolved
    /// exactly once: by the close, a failure, or the timeout.
    static func exchange(socketPath: String, request: Data, timeout: TimeInterval) async throws -> Data {
        let queue = DispatchQueue(label: "sh.pigtail.app.unixhttp")
        let conn = NWConnection(to: .unix(path: socketPath), using: .tcp)
        let once = Once()
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Data, Error>) in
                let finish: @Sendable (Result<Data, Error>) -> Void = { result in
                    guard once.claim() else { return }
                    conn.cancel()
                    cont.resume(with: result)
                }
                let buffer = Buffer()
                @Sendable func receive() {
                    conn.receive(minimumIncompleteLength: 1, maximumLength: 65536) { data, _, isComplete, error in
                        if let data { buffer.append(data) }
                        if isComplete {
                            finish(.success(buffer.data))
                        } else if error != nil {
                            // A reset after a full answer still counts as the answer.
                            let got = buffer.data
                            finish(got.isEmpty ? .failure(UnixHTTPError.unreachable) : .success(got))
                        } else {
                            receive()
                        }
                    }
                }
                conn.stateUpdateHandler = { state in
                    switch state {
                    case .ready:
                        conn.send(content: request, completion: .contentProcessed { error in
                            if error != nil { finish(.failure(UnixHTTPError.unreachable)) }
                        })
                        receive()
                    case .failed, .waiting:
                        // The peer closing right after its answer can surface
                        // as .failed before the last receive: what was read
                        // stands, and parse() judges whether it is whole.
                        let got = buffer.data
                        finish(got.isEmpty ? .failure(UnixHTTPError.unreachable) : .success(got))
                    default:
                        break
                    }
                }
                queue.asyncAfter(deadline: .now() + timeout) {
                    finish(.failure(UnixHTTPError.timeout))
                }
                once.onCancel = { finish(.failure(CancellationError())) }
                conn.start(queue: queue)
            }
        } onCancel: {
            once.cancel()
        }
    }

    /// Parses a full HTTP/1.1 response; nil when it is not one.
    static func parse(_ raw: Data) -> UnixHTTPResponse? {
        let sep = Data("\r\n\r\n".utf8)
        guard let headEnd = raw.range(of: sep) else { return nil }
        let head = String(decoding: raw[raw.startIndex..<headEnd.lowerBound], as: UTF8.self)
        var lines = head.components(separatedBy: "\r\n")
        guard !lines.isEmpty else { return nil }
        let statusLine = lines.removeFirst().split(separator: " ", maxSplits: 2)
        guard statusLine.count >= 2, statusLine[0].hasPrefix("HTTP/1."), let status = Int(statusLine[1]) else { return nil }
        var headers: [String: String] = [:]
        for line in lines {
            guard let colon = line.firstIndex(of: ":") else { continue }
            let name = line[line.startIndex..<colon].trimmingCharacters(in: .whitespaces).lowercased()
            let value = line[line.index(after: colon)...].trimmingCharacters(in: .whitespaces)
            headers[name] = value
        }
        let rest = Data(raw[headEnd.upperBound...])
        let body: Data
        if headers["transfer-encoding"]?.lowercased().contains("chunked") == true {
            guard let decoded = dechunk(rest) else { return nil }
            body = decoded
        } else if let len = headers["content-length"].flatMap({ Int($0) }) {
            guard rest.count >= len else { return nil }
            body = rest.prefix(len)
        } else {
            body = rest
        }
        return UnixHTTPResponse(status: status, headers: headers, body: body)
    }

    static func dechunk(_ data: Data) -> Data? {
        var out = Data()
        var i = data.startIndex
        let crlf = Data("\r\n".utf8)
        while true {
            guard let lineEnd = data.range(of: crlf, in: i..<data.endIndex) else { return nil }
            let sizeText = String(decoding: data[i..<lineEnd.lowerBound], as: UTF8.self)
                .split(separator: ";").first.map(String.init)?.trimmingCharacters(in: .whitespaces) ?? ""
            guard let size = Int(sizeText, radix: 16) else { return nil }
            i = lineEnd.upperBound
            if size == 0 { return out }
            guard data.distance(from: i, to: data.endIndex) >= size else { return nil }
            let end = data.index(i, offsetBy: size)
            out.append(data[i..<end])
            i = end
            guard data.distance(from: i, to: data.endIndex) >= 2 else { return nil }
            i = data.index(i, offsetBy: 2)
        }
    }
}

/// Resolve-once latch shared by the callbacks of one exchange.
private final class Once: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    private var cancelled = false
    private var _onCancel: (@Sendable () -> Void)?

    var onCancel: (@Sendable () -> Void)? {
        get { lock.lock(); defer { lock.unlock() }; return _onCancel }
        set {
            lock.lock()
            _onCancel = newValue
            let fire = cancelled
            lock.unlock()
            if fire { newValue?() }
        }
    }

    func claim() -> Bool {
        lock.lock(); defer { lock.unlock() }
        if done { return false }
        done = true
        return true
    }

    func cancel() {
        lock.lock()
        cancelled = true
        let cb = _onCancel
        lock.unlock()
        cb?()
    }
}

private final class Buffer: @unchecked Sendable {
    private let lock = NSLock()
    private var _data = Data()
    func append(_ d: Data) { lock.lock(); _data.append(d); lock.unlock() }
    var data: Data { lock.lock(); defer { lock.unlock() }; return _data }
}
