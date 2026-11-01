// swift-tools-version:6.0
import PackageDescription

// One app, Pigtail: a main window and a menu bar.
// MenuBarCore depends on no other target: an icon script
// compiles Sources/MenuBarCore/*.swift alone with swiftc, so an `import AppCore`
// there must fail the build here first.
let package = Package(
    name: "Pigtail",
    platforms: [.macOS(.v14)],
    targets: [
        .target(name: "AppCore", path: "Sources/AppCore"),
        .target(name: "MenuBarCore", dependencies: [], path: "Sources/MenuBarCore"),
        .target(name: "WindowCore", dependencies: ["AppCore"], path: "Sources/WindowCore"),
        .executableTarget(
            name: "Pigtail",
            dependencies: ["AppCore", "MenuBarCore", "WindowCore"],
            path: "Sources/Pigtail"
        ),
        .testTarget(name: "AppCoreTests", dependencies: ["AppCore"], path: "Tests/AppCoreTests"),
        .testTarget(
            name: "MenuBarCoreTests",
            dependencies: ["MenuBarCore"],
            path: "Tests/MenuBarCoreTests",
            exclude: ["Fixtures"]
        ),
        .testTarget(name: "WindowCoreTests", dependencies: ["WindowCore", "AppCore"], path: "Tests/WindowCoreTests"),
    ]
)
