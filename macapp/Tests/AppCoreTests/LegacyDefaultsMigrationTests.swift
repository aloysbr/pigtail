import XCTest
@testable import AppCore

final class LegacyDefaultsMigrationTests: XCTestCase {
    private func suite() -> UserDefaults {
        let name = "test.pigtail.\(UUID().uuidString)"
        let d = UserDefaults(suiteName: name)!
        addTeardownBlock { UserDefaults(suiteName: name)?.removePersistentDomain(forName: name) }
        return d
    }

    func testWindowPrefsCarried() {
        let t = suite(), w = suite(), b = suite()
        w.set("127.0.0.1:7811", forKey: "cockpitHTTPAddr")
        w.set("terra", forKey: "cockpitDockPalette")
        w.set("dark", forKey: "cockpitWindowAppearance")
        w.set("10 10 1280 800 0 0 1728 1079 ", forKey: "NSWindow Frame PigtailCockpitMainWindow")
        let copied = LegacyDefaultsMigration.run(into: t, window: w, bar: b)
        XCTAssertEqual(Set(copied), ["httpAddr", "dockPalette", "windowAppearance", "NSWindow Frame PigtailMainWindow"])
        XCTAssertEqual(t.string(forKey: "httpAddr"), "127.0.0.1:7811")
        XCTAssertEqual(t.string(forKey: "dockPalette"), "terra")
        XCTAssertEqual(t.string(forKey: "windowAppearance"), "dark")
        XCTAssertEqual(t.string(forKey: "NSWindow Frame PigtailMainWindow"), "10 10 1280 800 0 0 1728 1079 ")
        XCTAssertEqual(w.string(forKey: "cockpitHTTPAddr"), "127.0.0.1:7811", "legacy domain left in place")
    }

    func testAbsentLegacyPaletteMeansOcean() {
        let t = suite(), w = suite(), b = suite()
        w.set("127.0.0.1:7800", forKey: "cockpitHTTPAddr")
        w.set("light", forKey: "cockpitWindowAppearance")
        let copied = LegacyDefaultsMigration.run(into: t, window: w, bar: b)
        XCTAssertEqual(t.string(forKey: "dockPalette"), "ocean", "the old app showed Ocean: the web must not fall back to Terra")
        XCTAssertTrue(copied.contains("dockPalette"))
    }

    func testUnusedLegacyWindowDomainSetsNoPalette() {
        let t = suite(), w = suite(), b = suite()
        b.set(341.0, forKey: "NSStatusItem Preferred Position Item-0")
        LegacyDefaultsMigration.run(into: t, window: w, bar: b)
        XCTAssertNil(t.object(forKey: "dockPalette"), "no old window app: the web default holds")
    }

    func testStatusItemPositionCarried() {
        let t = suite(), w = suite(), b = suite()
        b.set(341.0, forKey: "NSStatusItem Preferred Position Item-0")
        let copied = LegacyDefaultsMigration.run(into: t, window: w, bar: b)
        XCTAssertEqual(t.double(forKey: "NSStatusItem Preferred Position Item-0"), 341)
        XCTAssertEqual(copied, ["NSStatusItem Preferred Position Item-0"])
    }

    func testCheckNoticeLedgerCarried() {
        let t = suite(), w = suite(), b = suite()
        b.set(["cmux-check-a", "cmux-check-b"], forKey: "cmuxCheckNoticeIDs")
        let copied = LegacyDefaultsMigration.run(into: t, window: w, bar: b)
        XCTAssertEqual(t.stringArray(forKey: "cmuxCheckNoticeIDs"), ["cmux-check-a", "cmux-check-b"],
                       "a notice already posted by the old bar must not be posted again")
        XCTAssertEqual(copied, ["cmuxCheckNoticeIDs"])
    }

    func testRetiredBundleIDs() {
        XCTAssertEqual(Set(LegacyDefaultsMigration.retiredBundleIDs), ["sh.pigtail.bar", "sh.pigtail.cockpit"])
    }

    func testLoginOptOutCarried() {
        let t = suite(), w = suite(), b = suite()
        b.set(false, forKey: "LaunchAtLogin.userPreference")
        LegacyDefaultsMigration.run(into: t, window: w, bar: b)
        XCTAssertEqual(t.object(forKey: "LaunchAtLogin.userPreference") as? Bool, false)
    }

    func testLoginOptInNotCarried() {
        let t = suite(), w = suite(), b = suite()
        b.set(true, forKey: "LaunchAtLogin.userPreference")
        LegacyDefaultsMigration.run(into: t, window: w, bar: b)
        XCTAssertNil(t.object(forKey: "LaunchAtLogin.userPreference"), "fresh policy must register the new bundle")
    }

    func testRunsOnceAndNeverOverwrites() {
        let t = suite(), w = suite(), b = suite()
        t.set("ocean", forKey: "dockPalette")
        w.set("terra", forKey: "cockpitDockPalette")
        LegacyDefaultsMigration.run(into: t, window: w, bar: b)
        XCTAssertEqual(t.string(forKey: "dockPalette"), "ocean")
        w.set("127.0.0.1:9999", forKey: "cockpitHTTPAddr")
        XCTAssertEqual(LegacyDefaultsMigration.run(into: t, window: w, bar: b), [], "second run reports nothing")
        XCTAssertNil(t.string(forKey: "httpAddr"), "second run must not read the legacy domain again")
        XCTAssertTrue(t.bool(forKey: LegacyDefaultsMigration.doneKey))
    }
}
