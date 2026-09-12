// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "VCWorkspace",
    platforms: [.macOS(.v14)],
    products: [.executable(name: "VCWorkspace", targets: ["VCWorkspace"])],
    targets: [
        .executableTarget(
            name: "VCWorkspace",
            path: "Sources/VCWorkspace",
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
        .testTarget(
            name: "VCWorkspaceTests",
            dependencies: ["VCWorkspace"],
            path: "Tests/VCWorkspaceTests",
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
    ]
)
