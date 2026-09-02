// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "VCVDI",
    platforms: [.macOS(.v14)],
    products: [.executable(name: "VCVDI", targets: ["VCVDI"])],
    targets: [
        .executableTarget(
            name: "VCVDI",
            path: "Sources/VCVDI",
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
        .testTarget(
            name: "VCVDITests",
            dependencies: ["VCVDI"],
            path: "Tests/VCVDITests",
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
    ]
)
