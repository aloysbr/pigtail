/// AppKit only makes a window key inside an active app, outside a
/// deminiaturize animation: a `makeKeyAndOrderFront` sent too early is lost.
/// So a show leaves a request due until the window really becomes key.
public struct WindowKeyRequest: Equatable, Sendable {
    public enum Event: Sendable { case show, appBecameActive, deminiaturized, becameKey, closed }
    public enum Action: Equatable, Sendable { case none, makeKey }

    public private(set) var pending = false
    public init() {}

    public mutating func handle(_ e: Event) -> Action {
        switch e {
        case .show:
            pending = true
            return .makeKey
        case .appBecameActive, .deminiaturized:
            return pending ? .makeKey : .none
        case .becameKey, .closed:
            pending = false
            return .none
        }
    }
}
