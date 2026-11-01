import XCTest
@testable import WindowCore

@MainActor
final class DockIconThemeTests: XCTestCase {
    private func freshDefaults() -> UserDefaults {
        let name = "WindowCoreTests.\(UUID().uuidString)"
        let d = UserDefaults(suiteName: name)!
        d.removePersistentDomain(forName: name)
        addTeardownBlock { UserDefaults(suiteName: name)?.removePersistentDomain(forName: name) }
        return d
    }

    func testAMessageIsExactlyOceanOrTerra() {
        XCTAssertEqual(ThemePalette(message: "ocean"), .ocean)
        XCTAssertEqual(ThemePalette(message: "terra"), .terra)
        let rejected: [Any] = ["Terra", "", "forest", " ocean", 1, NSNull(), ["terra"]]
        for bad in rejected {
            XCTAssertNil(ThemePalette(message: bad), "\(bad)")
        }
    }

    func testOceanIsTheBundlesIconAndTerraShipsBeside() throws {
        XCTAssertNil(ThemePalette.ocean.iconResourceName)
        let terra = try XCTUnwrap(ThemePalette.terra.iconResourceName)
        let file = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("Resources/\(terra).icns")
        XCTAssertTrue(FileManager.default.fileExists(atPath: file.path), "the Makefile copies \(file.lastPathComponent) under this name")
    }

    func testStoreReadsOceanBeforeAnyPalette() {
        XCTAssertEqual(DockIconStore(defaults: freshDefaults()).load(), .ocean)
    }

    func testStoreKeepsThePalette() {
        let d = freshDefaults()
        DockIconStore(defaults: d).save(.terra)
        XCTAssertEqual(DockIconStore(defaults: d).load(), .terra)
        XCTAssertEqual(d.string(forKey: "dockPalette"), "terra")
    }

    func testStoreReadsGarbageAsOcean() {
        let d = freshDefaults()
        d.set("forest", forKey: "dockPalette")
        XCTAssertEqual(DockIconStore(defaults: d).load(), .ocean)
    }

    func testThemeStartsOnTheSavedPalette() {
        let d = freshDefaults()
        DockIconStore(defaults: d).save(.terra)
        XCTAssertEqual(DockIconTheme(store: DockIconStore(defaults: d)).current, .terra)
    }

    func testAChangeIsReturnedAndSaved() {
        let d = freshDefaults()
        let theme = DockIconTheme(store: DockIconStore(defaults: d))
        XCTAssertEqual(theme.receive("terra"), .terra)
        XCTAssertEqual(theme.current, .terra)
        XCTAssertEqual(DockIconStore(defaults: d).load(), .terra)
        XCTAssertEqual(theme.receive("ocean"), .ocean)
        XCTAssertEqual(DockIconStore(defaults: d).load(), .ocean)
    }

    func testTheSamePaletteTwiceChangesNothing() {
        let theme = DockIconTheme(store: DockIconStore(defaults: freshDefaults()))
        XCTAssertNil(theme.receive("ocean"), "already Océan: nothing to apply")
        XCTAssertEqual(theme.receive("terra"), .terra)
        XCTAssertNil(theme.receive("terra"))
    }

    func testAnUnknownValueKeepsTheIconAndSavesNothing() {
        let d = freshDefaults()
        let theme = DockIconTheme(store: DockIconStore(defaults: d))
        XCTAssertNil(theme.receive("forest"))
        XCTAssertNil(theme.receive(42))
        XCTAssertEqual(theme.current, .ocean)
        XCTAssertNil(d.string(forKey: "dockPalette"))
    }
}
