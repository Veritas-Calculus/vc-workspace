#include "../VCWDisplayPresentation.h"
#include <assert.h>
#include <stdio.h>

int main(void)
{
    assert(vcw_display_presentation(0, 0, 0, 0, 0, 0) == VCW_DISPLAY_READY);
    assert(vcw_display_presentation(5120, 2678, 1640, 980, 1000, 1000) == VCW_DISPLAY_ADJUSTING);
    /* A matching width or the old full-screen frame is not sufficient. */
    assert(vcw_display_presentation(5120, 2678, 5120, 2804, 1000, 1600) == VCW_DISPLAY_ADJUSTING);
    assert(vcw_display_presentation(5120, 2678, 0, 0, 1000, 1600) == VCW_DISPLAY_ADJUSTING);
    /* The old fixed 600 ms delay would incorrectly pass these two cases. */
    assert(vcw_display_presentation(5120, 2678, 1640, 980, 1000, 2000) == VCW_DISPLAY_ADJUSTING);
    assert(vcw_display_presentation(5120, 2678, 5120, 2678, 1000, 1200) == VCW_DISPLAY_READY);
    assert(vcw_display_presentation(1640, 980, 5120, 2678, 2000, 2600) == VCW_DISPLAY_ADJUSTING);
    assert(vcw_display_presentation(1640, 980, 1640, 980, 2000, 2700) == VCW_DISPLAY_READY);
    assert(vcw_display_presentation(5120, 2678, 1640, 980, 1000, 8999) == VCW_DISPLAY_ADJUSTING);
    assert(vcw_display_presentation(5120, 2678, 1640, 980, 1000, 9000) == VCW_DISPLAY_SCALED);
    assert(vcw_display_presentation(5120, 2678, 5120, 2678, 1000, 10000) == VCW_DISPLAY_READY);
    assert(vcw_display_retry_due(1000, 0, 0, 1000));
    assert(!vcw_display_retry_due(1000, 1000, 1, 1499));
    assert(vcw_display_retry_due(1000, 1000, 1, 1500));
    assert(!vcw_display_retry_due(1000, 2500, 4, 4499));
    assert(vcw_display_retry_due(1000, 2500, 4, 4500));
    assert(vcw_display_retry_due(1000, 6500, 6, 8500));
    assert(!vcw_display_retry_due(1000, 29500, 18, 31000));
    assert(!vcw_display_retry_due(1000, 2000, 20, 3000));
    assert(!vcw_display_retry_due(1000, 0, 0, 999));
    assert(!vcw_display_retry_due(1000, 2000, 1, 1999));

    /* Reproduce the observed xrdp sequence: caps before login, then Xorg
     * available after 4.2 s. The old five-attempt policy never reaches it. */
    uint32_t attempts = 0;
    uint64_t lastAttempt = 0;
    uint64_t acceptedAt = 0;
    for (uint64_t now = 1000; now < 32000; now += 125)
    {
        if (!vcw_display_retry_due(1000, lastAttempt, attempts, now))
            continue;
        lastAttempt = now;
        attempts++;
        if (now >= 5200 && !acceptedAt)
            acceptedAt = now;
    }
    assert(acceptedAt >= 5200 && acceptedAt <= 7200);
    assert(attempts <= 20 && attempts > 5);
    assert(lastAttempt < 31000);
    puts("PASS: display presentation and cold-login retries (24 assertions)");
}
