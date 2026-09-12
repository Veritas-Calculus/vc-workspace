import CoreGraphics
import Testing
@testable import VCWorkspace

@Test
func rdpViewportUsesOnePixelPerPointOnStandardDensityDisplays() throws {
    let viewport = try #require(
        RDPViewport(pointSize: CGSize(width: 1_365, height: 720), backingScaleFactor: 1)
    )

    #expect(viewport.pixelWidth == 1_364)
    #expect(viewport.pixelHeight == 720)
    #expect(viewport.desktopScaleFactor == 100)
}

@Test
func rdpViewportRoundsWidthToDisplayControlRequirements() throws {
    let viewport = try #require(
        RDPViewport(pointSize: CGSize(width: 801, height: 601), backingScaleFactor: 1)
    )

    #expect(viewport.pixelWidth == 800)
    #expect(viewport.pixelWidth.isMultiple(of: 2))
    #expect(viewport.pixelHeight == 601)
    #expect(viewport.desktopScaleFactor == 100)
}

@Test
func rdpViewportClampsProtocolBoundsAndRejectsInvalidGeometry() throws {
    let minimum = try #require(
        RDPViewport(pointSize: CGSize(width: 20, height: 30), backingScaleFactor: 1)
    )
    let maximum = try #require(
        RDPViewport(pointSize: CGSize(width: 9_000, height: 9_000), backingScaleFactor: 1)
    )

    #expect(minimum.pixelWidth == 200)
    #expect(minimum.pixelHeight == 200)
    #expect(maximum.pixelWidth == 8_192)
    #expect(maximum.pixelHeight == 8_192)
    #expect(RDPViewport(pointSize: .zero, backingScaleFactor: 1) == nil)
    #expect(RDPViewport(
        pointSize: CGSize(width: 400, height: 400),
        backingScaleFactor: 0
    ) == nil)
}

@Test
func rdpViewportUsesRetinaPixelsWithMatchingDesktopScale() throws {
    let viewport = try #require(
        RDPViewport(pointSize: CGSize(width: 820, height: 486), backingScaleFactor: 2)
    )

    #expect(viewport.pixelWidth == 1_640)
    #expect(viewport.pixelHeight == 972)
    #expect(viewport.desktopScaleFactor == 200)
}

@Test
func rdpViewportCapsSupersamplingAtTwoTimes() throws {
    let viewport = try #require(
        RDPViewport(pointSize: CGSize(width: 820, height: 486), backingScaleFactor: 3)
    )

    #expect(viewport.pixelWidth == 1_640)
    #expect(viewport.pixelHeight == 972)
    #expect(viewport.desktopScaleFactor == 200)
}
