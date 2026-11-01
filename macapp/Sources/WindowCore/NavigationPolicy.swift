import Foundation

public enum NavigationKind: Equatable, Sendable {
    case reload
    case linkActivated
    case backForward
    case other
}

public struct NavigationRequest: Equatable, Sendable {
    public var url: URL?
    public var isMainFrame: Bool
    public var opensNewWindow: Bool
    public var kind: NavigationKind

    public init(url: URL?, isMainFrame: Bool = true, opensNewWindow: Bool = false, kind: NavigationKind = .other) {
        self.url = url
        self.isMainFrame = isMainFrame
        self.opensNewWindow = opensNewWindow
        self.kind = kind
    }
}

public enum NavigationDecision: Equatable, Sendable {
    /// Load it in the window.
    case allow
    /// The front reloads `/` when its session is refused
    /// (web/src/api/client.ts): cancel it and let the flow probe.
    case gateSession
    /// Hand it to the system (default browser, Obsidian, Mail…), D3.
    case openExternally(URL)
    case cancel
}

/// Where a navigation goes: the web front's own origin stays in the window,
/// anything else opens outside (D3). Pure; rebuilt at each saved address.
public struct NavigationPolicy: Sendable {
    public let origin: DaemonAddress

    public init(origin: DaemonAddress) {
        self.origin = origin
    }

    public func decide(_ r: NavigationRequest) -> NavigationDecision {
        guard let url = r.url, let scheme = url.scheme?.lowercased() else { return .cancel }
        switch scheme {
        case "about":
            return url.absoluteString == "about:blank" ? .allow : .cancel
        case "file", "javascript", "data", "blob":
            return .cancel
        default:
            break
        }
        if origin.isSameOrigin(url) {
            if r.kind == .reload, r.isMainFrame, url.path.isEmpty || url.path == "/" {
                return .gateSession
            }
            return .allow
        }
        // A foreign frame inside the page never pops a browser by itself.
        if !r.isMainFrame && !r.opensNewWindow { return .cancel }
        return .openExternally(url)
    }
}

public enum ResponseDecision: Equatable, Sendable {
    case allow
    /// 410 on the login: the code was spent or expired.
    case loginRefused
}

public enum ResponsePolicy {
    public static func decide(url: URL?, status: Int, isMainFrame: Bool, origin: DaemonAddress) -> ResponseDecision {
        guard status == 410, isMainFrame, let url, origin.isSameOrigin(url), url.path == "/web/login" else {
            return .allow
        }
        return .loginRefused
    }
}
