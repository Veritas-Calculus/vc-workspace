// SPDX-License-Identifier: Apache-2.0
#include "protocol.h"
#include <arpa/inet.h>
#include <string.h>

void vcw_clear(void* data, size_t length) {
    volatile uint8_t* value = data;
    while (length--) *value++ = 0;
}

static uint16_t u16(const uint8_t* p) { return (uint16_t)((p[0] << 8) | p[1]); }
static bool lower_hex(char c) { return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'); }
static bool ascii_alnum(char c) {
    return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9');
}

bool vcw_decode(const uint8_t* data, size_t length, uint64_t now, vcw_request* out) {
    if (!out) return false;
    memset(out, 0, sizeof(*out));
    if (!data || length < 55 || length > VCW_MAX_FRAME || memcmp(data, "VCW1", 4)) return false;
    vcw_request parsed = {0};
    bool valid = false;
    for (size_t i = 4; i < 12; i++) parsed.expires_unix_ms = (parsed.expires_unix_ms << 8) | data[i];
    if (parsed.expires_unix_ms <= now || parsed.expires_unix_ms - now > VCW_MAX_LIFETIME_MS) goto end;
    parsed.port = u16(data + 12); parsed.width = u16(data + 14); parsed.height = u16(data + 16);
    const size_t host = data[18], user = data[19], domain = data[20], password = u16(data + 21);
    if (!parsed.port || parsed.width < 640 || parsed.height < 480 || parsed.width > 1920 || parsed.height > 1200 ||
        host < 7 || host > 15 || user != 15 || !domain || domain > 15 || password < 24 || password > 256 ||
        length != 55 + host + user + domain + password) goto end;
    memcpy(parsed.fingerprint, "sha256:", 7);
    const char* hex = "0123456789abcdef";
    for (size_t i = 0; i < 32; i++) {
        parsed.fingerprint[7 + 2*i] = hex[data[23+i] >> 4];
        parsed.fingerprint[8 + 2*i] = hex[data[23+i] & 15];
    }
    size_t offset = 55;
    memcpy(parsed.host, data + offset, host); offset += host;
    memcpy(parsed.username, data + offset, user); offset += user;
    memcpy(parsed.domain, data + offset, domain); offset += domain;
    memcpy(parsed.password, data + offset, password);
    struct in_addr address;
    if (strlen(parsed.host) != host || inet_pton(AF_INET, parsed.host, &address) != 1) goto end;
    uint32_t ip = ntohl(address.s_addr);
    if ((ip >> 24) != 10 && (ip >> 20) != 0xac1 && (ip >> 16) != 0xc0a8) goto end;
    if (memcmp(parsed.username, "vca", 3)) goto end;
    for (size_t i = 3; i < user; i++) if (!lower_hex(parsed.username[i])) goto end;
    for (size_t i = 0; i < domain; i++) if (!ascii_alnum(parsed.domain[i]) && parsed.domain[i] != '-') goto end;
    for (size_t i = 0; i < password; i++) if (parsed.password[i] < 33 || parsed.password[i] > 126) goto end;
    *out = parsed;
    valid = true;
end:
    vcw_clear(&parsed, sizeof(parsed));
    return valid;
}
