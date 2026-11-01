import XCTest
@testable import WindowCore

@MainActor
final class WindowAppearanceTests: XCTestCase {
    private func freshDefaults() -> UserDefaults {
        let name = "WindowCoreTests.\(UUID().uuidString)"
        let d = UserDefaults(suiteName: name)!
        d.removePersistentDomain(forName: name)
        addTeardownBlock { UserDefaults(suiteName: name)?.removePersistentDomain(forName: name) }
        return d
    }

    func testAMessageIsExactlyLightDarkOrSystem() {
        XCTAssertEqual(AppearanceChoice(message: "light"), .light)
        XCTAssertEqual(AppearanceChoice(message: "dark"), .dark)
        XCTAssertEqual(AppearanceChoice(message: "system"), .system)
        let rejected: [Any] = ["Dark", "", "System", " light", 1, NSNull(), ["dark"]]
        for bad in rejected {
            XCTAssertNil(AppearanceChoice(message: bad), "\(bad)")
        }
    }

    func testStoreReadsNilBeforeAnyAppearance() {
        XCTAssertNil(WindowAppearanceStore(defaults: freshDefaults()).load())
    }

    func testStoreKeepsTheAppearance() {
        let d = freshDefaults()
        WindowAppearanceStore(defaults: d).save(.dark)
        XCTAssertEqual(WindowAppearanceStore(defaults: d).load(), .dark)
        XCTAssertEqual(d.string(forKey: "windowAppearance"), "dark")
    }

    func testStoreRoundTripsSystem() {
        let d = freshDefaults()
        WindowAppearanceStore(defaults: d).save(.system)
        XCTAssertEqual(WindowAppearanceStore(defaults: d).load(), .system)
        XCTAssertEqual(d.string(forKey: "windowAppearance"), "system")
    }

    func testLaunchWithSavedSystemStartsOnSystem() {
        let d = freshDefaults()
        WindowAppearanceStore(defaults: d).save(.system)
        let appearance = WindowAppearance(store: WindowAppearanceStore(defaults: d))
        XCTAssertEqual(appearance.current, .system)
        XCTAssertNil(appearance.current?.appKitName, "system forces no appearance")
    }

    func testSwitchingFromLightToSystemIsReturnedAndSaved() {
        let d = freshDefaults()
        let appearance = WindowAppearance(store: WindowAppearanceStore(defaults: d))
        XCTAssertEqual(appearance.receive("light"), .light)
        XCTAssertEqual(appearance.receive("system"), .system)
        XCTAssertEqual(WindowAppearanceStore(defaults: d).load(), .system)
    }

    func testStoreReadsGarbageAsNil() {
        let d = freshDefaults()
        d.set("sepia", forKey: "windowAppearance")
        XCTAssertNil(WindowAppearanceStore(defaults: d).load())
    }

    func testStartsOnTheSavedAppearance() {
        let d = freshDefaults()
        WindowAppearanceStore(defaults: d).save(.light)
        XCTAssertEqual(WindowAppearance(store: WindowAppearanceStore(defaults: d)).current, .light)
    }

    func testAChangeIsReturnedAndSaved() {
        let d = freshDefaults()
        let appearance = WindowAppearance(store: WindowAppearanceStore(defaults: d))
        XCTAssertEqual(appearance.receive("dark"), .dark)
        XCTAssertEqual(appearance.current, .dark)
        XCTAssertEqual(WindowAppearanceStore(defaults: d).load(), .dark)
        XCTAssertEqual(appearance.receive("light"), .light)
        XCTAssertEqual(WindowAppearanceStore(defaults: d).load(), .light)
    }

    func testTheSameAppearanceTwiceChangesNothing() {
        let appearance = WindowAppearance(store: WindowAppearanceStore(defaults: freshDefaults()))
        XCTAssertEqual(appearance.receive("dark"), .dark)
        XCTAssertNil(appearance.receive("dark"))
    }

    func testAnUnknownValueKeepsTheAppearanceAndSavesNothing() {
        let d = freshDefaults()
        let appearance = WindowAppearance(store: WindowAppearanceStore(defaults: d))
        XCTAssertNil(appearance.receive("sepia"))
        XCTAssertNil(appearance.receive(42))
        XCTAssertNil(appearance.current)
        XCTAssertNil(d.string(forKey: "windowAppearance"))
    }

    func testTheNativeNamesFollowTheWebFront() {
        XCTAssertEqual(AppearanceChoice.light.appKitName, "NSAppearanceNameAqua")
        XCTAssertEqual(AppearanceChoice.dark.appKitName, "NSAppearanceNameDarkAqua")
        XCTAssertNil(AppearanceChoice.system.appKitName)
    }
}
