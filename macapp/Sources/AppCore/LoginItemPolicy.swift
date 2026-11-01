import Foundation

// MARK: - LoginItemStatus

/// Framework-agnostic mirror of `SMAppService.Status`, so the launch-at-login
/// decision logic can be unit-tested without touching ServiceManagement (which
/// mutates real system state). The app layer maps `SMAppService.mainApp.status`
/// onto this enum before feeding it to `LoginItemPolicy`.
public enum LoginItemStatus: Sendable, Equatable {
    /// No login-item registration exists for this app.
    case notRegistered
    /// Registered and active — the app will launch at login.
    case enabled
    /// Registered but the user must approve it in System Settings first.
    case requiresApproval
    /// The OS could not find a matching bundle to register (e.g. run outside a
    /// `.app` bundle, or Launch Services hasn't indexed it yet).
    case notFound
}

// MARK: - LoginItemBootstrapDecision

/// What the app should do about its login item at startup, derived purely from
/// the current OS status and the user's stored preference.
public enum LoginItemBootstrapDecision: Sendable, Equatable {
    /// Self-install as a login item (first launch, never configured).
    case register
    /// Leave the current state untouched.
    case none
}

// MARK: - LoginItemPolicy

/// Pure decision logic for the launch-at-login behaviour. Kept free of any
/// ServiceManagement / AppKit dependency so it is deterministic and testable.
///
/// The stored preference distinguishes three states:
/// - `nil`   → the user has never configured launch-at-login (fresh install).
/// - `true`  → the user wants the app to launch at login.
/// - `false` → the user explicitly disabled launch-at-login.
public enum LoginItemPolicy {

    /// Decides what to do at launch.
    ///
    /// The rule: self-install exactly once on a fresh install (no stored
    /// preference *and* the OS has no registration yet). An explicit user
    /// choice — including an explicit "off" — is always respected, so we never
    /// resurrect a login item the user deliberately disabled.
    ///
    /// - Parameters:
    ///   - status: The current OS registration status.
    ///   - storedPreference: The user's stored choice (`nil` = never configured).
    ///   - bundleInApplications: Whether the running bundle lives in /Applications. A
    ///     build anywhere else (a test build, a verify build) never enters
    ///     System Settings › Login Items.
    /// - Returns: `.register` to auto-enable on first launch, else `.none`.
    public static func bootstrapDecision(
        status: LoginItemStatus,
        storedPreference: Bool?,
        bundleInApplications: Bool
    ) -> LoginItemBootstrapDecision {
        guard bundleInApplications else { return .none }
        switch storedPreference {
        case .some:
            // The user has expressed a choice — honour it, never override.
            return .none
        case .none:
            // Fresh install: self-install only if the OS has no record yet.
            // If it's already enabled / requiresApproval / notFound, do nothing
            // (nothing to gain, and .notFound register would just fail).
            return status == .notRegistered ? .register : .none
        }
    }
}
