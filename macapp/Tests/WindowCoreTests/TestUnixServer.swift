import Foundation

/// A one-shot HTTP server on a Unix socket, POSIX only, for tests. It accepts
/// connections on its own thread, records what it read and plays a scripted
/// behaviour. The path lives under a short mkdtemp directory: sun_path holds
/// 104 bytes.
final class TestUnixServer: @unchecked Sendable {
    enum Behaviour {
        /// Writes these raw bytes once the request head is read, then closes.
        case respond(Data)
        /// Closes the connection without writing anything.
        case closeWithoutReply
        /// Reads the request and never answers (the client must time out).
        case silent
    }

    let path: String
    private let dir: String
    private let listenFD: Int32
    private let behaviour: Behaviour
    private let lock = NSLock()
    private var _received = Data()
    private var openFDs: [Int32] = []
    private var stopped = false

    init(_ behaviour: Behaviour) throws {
        self.behaviour = behaviour
        var template = Array("/tmp/pcXXXXXX".utf8CString)
        guard let made = mkdtemp(&template) else { throw POSIXError(.EIO) }
        dir = String(cString: made)
        path = dir + "/s.sock"
        listenFD = socket(AF_UNIX, SOCK_STREAM, 0)
        guard listenFD >= 0 else { throw POSIXError(.EIO) }
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let bytes = Array(path.utf8)
        withUnsafeMutableBytes(of: &addr.sun_path) { raw in
            for (i, b) in bytes.enumerated() { raw[i] = b }
            raw[bytes.count] = 0
        }
        let len = socklen_t(MemoryLayout<sockaddr_un>.size)
        let bound = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(listenFD, $0, len) }
        }
        guard bound == 0, listen(listenFD, 4) == 0 else { throw POSIXError(.EADDRINUSE) }
        let thread = Thread { [self] in acceptLoop() }
        thread.start()
    }

    /// Everything read from the client so far.
    var received: String {
        lock.lock(); defer { lock.unlock() }
        return String(decoding: _received, as: UTF8.self)
    }

    func stop() {
        lock.lock()
        stopped = true
        let fds = openFDs
        openFDs = []
        lock.unlock()
        for fd in fds { shutdown(fd, SHUT_RDWR); close(fd) }
        shutdown(listenFD, SHUT_RDWR)
        close(listenFD)
        unlink(path)
        rmdir(dir)
    }

    private func acceptLoop() {
        while true {
            let fd = accept(listenFD, nil, nil)
            if fd < 0 { return }
            lock.lock()
            if stopped { lock.unlock(); close(fd); return }
            openFDs.append(fd)
            lock.unlock()
            serve(fd)
        }
    }

    private func serve(_ fd: Int32) {
        var buf = [UInt8](repeating: 0, count: 4096)
        var head = Data()
        while !head.contains(Data("\r\n\r\n".utf8)) {
            let n = read(fd, &buf, buf.count)
            if n <= 0 { break }
            head.append(contentsOf: buf[0..<n])
            lock.lock(); _received.append(contentsOf: buf[0..<n]); lock.unlock()
        }
        switch behaviour {
        case .respond(let data):
            data.withUnsafeBytes { raw in
                var off = 0
                while off < raw.count {
                    let w = write(fd, raw.baseAddress!.advanced(by: off), raw.count - off)
                    if w <= 0 { break }
                    off += w
                }
            }
            closeClient(fd)
        case .closeWithoutReply:
            closeClient(fd)
        case .silent:
            break // kept open until stop()
        }
    }

    private func closeClient(_ fd: Int32) {
        lock.lock()
        openFDs.removeAll { $0 == fd }
        lock.unlock()
        shutdown(fd, SHUT_RDWR)
        close(fd)
    }

    static func http(status: Int, reason: String, body: String, chunked: Bool = false) -> Data {
        var s = "HTTP/1.1 \(status) \(reason)\r\nContent-Type: application/json\r\n"
        if chunked {
            s += "Transfer-Encoding: chunked\r\n\r\n"
            let bytes = Array(body.utf8)
            let mid = bytes.count / 2
            for part in [bytes[0..<mid], bytes[mid...]] where !part.isEmpty {
                s += String(part.count, radix: 16) + "\r\n" + String(decoding: part, as: UTF8.self) + "\r\n"
            }
            s += "0\r\n\r\n"
        } else {
            s += "Content-Length: \(body.utf8.count)\r\n\r\n" + body
        }
        return Data(s.utf8)
    }
}
