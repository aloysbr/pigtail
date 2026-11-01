import Foundation

/// The last HTTP address pigtaild reported, kept between launches so a
/// valid cookie needs no new code (UserDefaults).
public final class AddressStore: @unchecked Sendable {
    static let key = "httpAddr"
    private let defaults: UserDefaults

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    /// The saved address, or pigtaild's default before any code.
    public func load() -> DaemonAddress {
        defaults.string(forKey: Self.key).flatMap(DaemonAddress.init(httpAddr:)) ?? .default
    }

    public func save(_ address: DaemonAddress) {
        defaults.set(address.httpAddr, forKey: Self.key)
    }
}
