import Foundation
import os
import ServiceManagement
import SwiftUI
import AppCore
import MenuBarCore

// MARK: - LoginItemController

/// Owns the app's "launch at login" state via the modern `ServiceManagement`
/// framework (`SMAppService.mainApp`), replacing the external System Events
/// login-item hack.
///
/// Responsibilities:
/// - Expose an observable `isEnabled` for the popover toggle.
/// - Register / unregister on user toggle, persisting the choice in UserDefaults.
/// - Self-install once on a fresh launch (see `bootstrapIfNeeded`), while
///   respecting a deliberate user opt-out.
/// - Surface `requiresApproval` (the user must approve in System Settings)
///   without crashing.
///
/// The pure decision of *whether* to self-install lives in
/// `AppCore.LoginItemPolicy` so it stays unit-testable; this type only
/// performs the ServiceManagement side effects and observable plumbing.
@MainActor
final class LoginItemController: ObservableObject {

    /// Whether the app is set to launch at login. `requiresApproval` counts as
    /// "on" (the app is registered; the OS is just waiting on the user), so the
    /// toggle stays lit while the approval banner guides them.
    @Published private(set) var isEnabled: Bool = false

    /// True when macOS needs the user to approve the login item in
    /// System Settings › General › Login Items. Purely informational.
    @Published private(set) var requiresApproval: Bool = false

    private let service: SMAppService
    private let defaults: UserDefaults

    /// UserDefaults key holding the user's explicit choice. Absent = never
    /// configured (lets `LoginItemPolicy` distinguish "fresh" from "disabled").
    private let preferenceKey = "LaunchAtLogin.userPreference"

    init(service: SMAppService = .mainApp, defaults: UserDefaults = .standard) {
        self.service = service
        self.defaults = defaults
        syncFromStatus()
    }

    // MARK: - Bootstrap

    /// Called once at startup. Self-installs the login item on a fresh launch
    /// (no stored preference and the OS has no registration), while honouring a
    /// deliberate opt-out. Delegates the decision to the pure policy.
    func bootstrapIfNeeded(bundleInApplications: Bool) {
        let decision = LoginItemPolicy.bootstrapDecision(
            status: currentStatus(),
            storedPreference: storedPreference(),
            bundleInApplications: bundleInApplications
        )
        if decision == .register {
            setEnabled(true)
        } else {
            // Keep the published state honest even when we take no action
            // (e.g. reflect a pre-existing registration or pending approval).
            syncFromStatus()
        }
    }

    // MARK: - Toggle

    /// Two-way binding for a SwiftUI `Toggle`. Reads the observable state and
    /// routes writes through `setEnabled`, which performs the register /
    /// unregister and records the choice.
    var binding: Binding<Bool> {
        Binding(get: { self.isEnabled }, set: { self.setEnabled($0) })
    }

    /// Applies an explicit user choice: persists it, then registers or
    /// unregisters accordingly. Errors are logged (never fatal); the resulting
    /// OS status is re-read into the published state so the UI reflects reality
    /// — including a bounce-back if the operation failed.
    func setEnabled(_ enabled: Bool) {
        defaults.set(enabled, forKey: preferenceKey)
        do {
            if enabled {
                // register() throws if already registered; guard on status.
                if service.status != .enabled {
                    try service.register()
                }
            } else {
                if service.status == .enabled || service.status == .requiresApproval {
                    try service.unregister()
                }
            }
        } catch {
            BarLog.login.error("[Pigtail] login item \(enabled ? "register" : "unregister", privacy: .public) failed: \(error.localizedDescription, privacy: .public)")
        }
        syncFromStatus()
    }

    // MARK: - Status

    /// Re-reads `SMAppService.mainApp.status` into the published properties.
    private func syncFromStatus() {
        let status = currentStatus()
        isEnabled = (status == .enabled || status == .requiresApproval)
        requiresApproval = (status == .requiresApproval)
    }

    /// The user's stored choice, or `nil` if never configured.
    private func storedPreference() -> Bool? {
        defaults.object(forKey: preferenceKey) as? Bool
    }

    /// Maps the live `SMAppService` status onto the framework-agnostic enum
    /// consumed by `LoginItemPolicy`.
    private func currentStatus() -> LoginItemStatus {
        switch service.status {
        case .enabled: return .enabled
        case .requiresApproval: return .requiresApproval
        case .notRegistered: return .notRegistered
        case .notFound: return .notFound
        @unknown default: return .notFound
        }
    }
}
