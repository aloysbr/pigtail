import XCTest
@testable import MenuBarCore

final class PigtailLauncherTests: XCTestCase {

    // MARK: - resolveBinary

    func testResolvesFromPATH() {
        // pathEnv "/a:/b", pigtail exists at /b/pigtail → returns /b/pigtail
        let result = PigtailLauncher.resolveBinary(
            pathEnv: "/a:/b",
            executableDir: "/exec",
            fileExists: { path in path == "/b/pigtail" }
        )
        XCTAssertEqual(result, "/b/pigtail")
    }

    func testFallsBackToExecutableSibling() {
        // No PATH hit, but pigtail exists next to the running executable
        let result = PigtailLauncher.resolveBinary(
            pathEnv: "/a:/b",
            executableDir: "/exec",
            fileExists: { path in path == "/exec/pigtail" }
        )
        XCTAssertEqual(result, "/exec/pigtail")
    }

    func testReturnsNilWhenAbsent() {
        // Nothing exists anywhere
        let result = PigtailLauncher.resolveBinary(
            pathEnv: "/a:/b",
            executableDir: "/exec",
            fileExists: { _ in false }
        )
        XCTAssertNil(result)
    }
}
