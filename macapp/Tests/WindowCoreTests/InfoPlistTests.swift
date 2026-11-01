import XCTest
@testable import WindowCore

final class InfoPlistTests: XCTestCase {
    private func plist() throws -> [String: Any] {
        let url = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("Info.plist")
        let data = try Data(contentsOf: url)
        return try XCTUnwrap(PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any])
    }

    func testPlistIsTheSingleApp() throws {
        let p = try plist()
        XCTAssertEqual(p["CFBundleIdentifier"] as? String, "sh.pigtail.app")
        XCTAssertEqual(p["CFBundleName"] as? String, "Pigtail")
        XCTAssertEqual(p["CFBundleDisplayName"] as? String, "Pigtail")
        XCTAssertEqual(p["CFBundleExecutable"] as? String, "Pigtail")
        XCTAssertEqual(p["CFBundleIconFile"] as? String, "AppIcon")
        XCTAssertEqual(p["LSUIElement"] as? Bool, true)
        XCTAssertEqual(p["LSMinimumSystemVersion"] as? String, "14.0")
        XCTAssertEqual((p["NSAppTransportSecurity"] as? [String: Any])?["NSAllowsLocalNetworking"] as? Bool, true)
        XCTAssertEqual(p["NSHighResolutionCapable"] as? Bool, true)
        XCTAssertEqual(p["LSApplicationCategoryType"] as? String, "public.app-category.developer-tools")
    }

    func testNoSecondPlist() {
        let dir = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        XCTAssertFalse(FileManager.default.fileExists(atPath: dir.appendingPathComponent("CockpitInfo.plist").path))
    }

    func testLocalNetworkingAllowed() throws {
        let ats = try XCTUnwrap(plist()["NSAppTransportSecurity"] as? [String: Any])
        XCTAssertEqual(ats["NSAllowsLocalNetworking"] as? Bool, true)
    }

    private static let macapp = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    /// A whole .icns: its magic, and the length its header declares equals the file's.
    private func wholeIcns(_ name: String) throws -> Data {
        let data = try Data(contentsOf: Self.macapp.appendingPathComponent("Resources/\(name)"))
        XCTAssertEqual(data.prefix(4), Data("icns".utf8), name)
        let declared = data.dropFirst(4).prefix(4).reduce(0) { $0 << 8 | Int($1) }
        XCTAssertEqual(declared, data.count, "\(name) is truncated")
        XCTAssertGreaterThan(data.count, 100_000, "\(name): a full iconset, not a stub")
        return data
    }

    /// Ocean is the bundle's icon (Finder, notifications, Login Items), Terra rides along for
    /// the Dock; they are not the same file twice.
    func testTheTwoIconsAreDistinct() throws {
        let ocean = try wholeIcns("AppIcon.icns")
        let terra = try wholeIcns("AppIconTerra.icns")
        XCTAssertNotEqual(ocean, terra)
    }
}
