import XCTest
@testable import AppCore

final class LoginItemPolicyTests: XCTestCase {

    // MARK: - Fresh install (no stored preference)

    func testFreshInstallNotRegisteredSelfInstalls() {
        // Never configured + OS has no record → self-install once.
        XCTAssertEqual(
            LoginItemPolicy.bootstrapDecision(status: .notRegistered, storedPreference: nil, bundleInApplications: true),
            .register
        )
    }

    func testFreshInstallAlreadyEnabledDoesNothing() {
        // Never configured but somehow already enabled → leave it alone.
        XCTAssertEqual(
            LoginItemPolicy.bootstrapDecision(status: .enabled, storedPreference: nil, bundleInApplications: true),
            .none
        )
    }

    func testFreshInstallRequiresApprovalDoesNothing() {
        XCTAssertEqual(
            LoginItemPolicy.bootstrapDecision(status: .requiresApproval, storedPreference: nil, bundleInApplications: true),
            .none
        )
    }

    func testFreshInstallNotFoundDoesNothing() {
        // .notFound register would just fail — don't attempt it.
        XCTAssertEqual(
            LoginItemPolicy.bootstrapDecision(status: .notFound, storedPreference: nil, bundleInApplications: true),
            .none
        )
    }

    // MARK: - Explicit user opt-out (stored = false)

    func testExplicitOptOutNeverReRegisters() {
        // The user disabled it — never resurrect, regardless of OS status.
        for status: LoginItemStatus in [.notRegistered, .enabled, .requiresApproval, .notFound] {
            XCTAssertEqual(
                LoginItemPolicy.bootstrapDecision(status: status, storedPreference: false, bundleInApplications: true),
                .none,
                "status \(status) with opt-out should stay .none"
            )
        }
    }

    // MARK: - Explicit user opt-in (stored = true)

    func testExplicitOptInDoesNotDoubleRegister() {
        // The user wants it on; bootstrap takes no action (the toggle/register
        // path already handles reconciliation). Never returns .register here.
        for status: LoginItemStatus in [.notRegistered, .enabled, .requiresApproval, .notFound] {
            XCTAssertEqual(
                LoginItemPolicy.bootstrapDecision(status: status, storedPreference: true, bundleInApplications: true),
                .none,
                "status \(status) with opt-in should stay .none"
            )
        }
    }

    // MARK: - Only from /Applications

    func testNeverRegistersOutsideApplications() {
        XCTAssertEqual(LoginItemPolicy.bootstrapDecision(status: .notRegistered, storedPreference: nil, bundleInApplications: false), .none)
    }
    func testRegistersFromApplicationsOnFreshInstall() {
        XCTAssertEqual(LoginItemPolicy.bootstrapDecision(status: .notRegistered, storedPreference: nil, bundleInApplications: true), .register)
    }
    func testOptOutWinsInApplications() {
        XCTAssertEqual(LoginItemPolicy.bootstrapDecision(status: .notRegistered, storedPreference: false, bundleInApplications: true), .none)
    }
}
