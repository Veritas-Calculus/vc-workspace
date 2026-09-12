#pragma once

#include <stdint.h>

/* Shared by the bridge and its standalone regression test. A ResizeWindow
 * acknowledgement is not a rendered frame: keep the transition until pixels
 * for the latest requested geometry have actually reached EndPaint. */
enum VCWDisplayPresentation {
    VCW_DISPLAY_READY = 0,
    VCW_DISPLAY_ADJUSTING = 1,
    VCW_DISPLAY_SCALED = 2
};

/* Display Control caps can arrive while xrdp is still logging into Xorg.
 * Five 500 ms sends all get discarded in that phase. Keep a bounded, slowing
 * retry window independent of the 8 s visual fallback; a late native frame
 * can replace smart sizing without another user action. */
static inline int vcw_display_retry_due(uint64_t requestedAt, uint64_t lastAttempt,
                                        uint32_t attempts, uint64_t now)
{
    if (now < requestedAt || now - requestedAt >= 30000 || attempts >= 20)
        return 0;
    if (attempts == 0)
        return 1;
    const uint64_t interval = attempts < 4 ? 500 : 2000;
    return now >= lastAttempt && now - lastAttempt >= interval;
}

static inline int vcw_display_presentation(uint32_t requestedWidth, uint32_t requestedHeight,
                                           uint32_t paintedWidth, uint32_t paintedHeight,
                                           uint64_t requestedAt, uint64_t now)
{
    if (!requestedWidth || !requestedHeight)
        return VCW_DISPLAY_READY;
    if (paintedWidth == requestedWidth && paintedHeight == requestedHeight)
        return VCW_DISPLAY_READY;
    /* Unsupported/slow peers retain usable smart sizing, with an explicit
     * retry in the client. Never trap a session behind an endless overlay. */
    if (now >= requestedAt && now - requestedAt >= 8000)
        return VCW_DISPLAY_SCALED;
    return VCW_DISPLAY_ADJUSTING;
}
