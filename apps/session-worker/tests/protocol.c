// SPDX-License-Identifier: Apache-2.0
#include "../protocol.h"
#include <assert.h>
#include <string.h>

int main(void) {
    uint8_t frame[VCW_MAX_FRAME] = {0};
    memcpy(frame, "VCW1", 4);
    frame[10] = 0x27; frame[11] = 0x10; // expires=10000, test clock=1000
    frame[12] = 0x0d; frame[13] = 0x3d; // port=3389
    frame[14] = 5; frame[16] = 2; frame[17] = 0xd0; // 1280x720
    frame[18] = 9; frame[19] = 15; frame[20] = 6; frame[22] = 24;
    memcpy(frame+55, "10.31.0.2vca0123456789abVCW-PCAbc1!2345678901234567890", 54);
    vcw_request request;
    assert(vcw_decode(frame, 109, 1000, &request));
    assert(!strcmp(request.host, "10.31.0.2"));
    assert(!strcmp(request.username, "vca0123456789ab"));
    assert(!strcmp(request.domain, "VCW-PC"));
    assert(request.port == 3389 && request.width == 1280 && request.height == 720);
    for (size_t i=0; i<109; i++) assert(!vcw_decode(frame, i, 1000, &request));
    assert(!vcw_decode(frame, 110, 1000, &request));
    assert(!vcw_decode(frame, 109, 10000, &request));
    assert(!vcw_decode(NULL, 109, 1000, &request));
    assert(!vcw_decode(frame, 109, 1000, NULL));
    const char* denied_hosts[] = {"127.0.0.1", "100.0.0.1", "169.254.1", "10.31.0.x"};
    for (size_t i=0; i<sizeof(denied_hosts)/sizeof(*denied_hosts); i++) {
        memcpy(frame+55, denied_hosts[i], 9);
        assert(!vcw_decode(frame, 109, 1000, &request));
    }
    memcpy(frame+55, "10.31.0.2", 9);
    for (size_t i=55; i<109; i++) {
        uint8_t saved=frame[i]; frame[i]=0;
        assert(!vcw_decode(frame, 109, 1000, &request));
        frame[i]=saved;
    }
    const size_t mutations[] = {0, 4, 12, 14, 16, 18, 19, 20, 21, 22, 55, 66, 84, 85, 108};
    for (size_t i=0; i<sizeof(mutations)/sizeof(*mutations); i++) {
        size_t index=mutations[i]; uint8_t saved=frame[index]; frame[index]=255;
        bool parsed = vcw_decode(frame, 109, 1000, &request);
        if (index != 12) assert(!parsed); // any nonzero uint16 port is valid
        if (!parsed) { vcw_request empty={0}; assert(!memcmp(&request, &empty, sizeof(request))); }
        frame[index]=saved;
    }
    assert(vcw_decode(frame, 109, 1000, &request));
    vcw_clear(&request, sizeof(request));
    vcw_request empty={0}; assert(!memcmp(&request, &empty, sizeof(request)));
    return 0;
}
