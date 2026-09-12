// SPDX-License-Identifier: Apache-2.0
#ifndef VCW_SESSION_WORKER_PROTOCOL_H
#define VCW_SESSION_WORKER_PROTOCOL_H
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define VCW_MAX_FRAME 1024
#define VCW_MAX_LIFETIME_MS (UINT64_C(8) * 60 * 60 * 1000)

typedef struct {
    uint64_t expires_unix_ms;
    uint16_t port, width, height;
    char host[16], username[16], domain[16], password[257];
    char fingerprint[72]; // "sha256:" and exactly 64 lowercase hex digits.
} vcw_request;

// Decode only one fixed-schema frame body; no arbitrary arguments, paths,
// channels, environment, gateway or certificate policy can be supplied.
bool vcw_decode(const uint8_t* data, size_t length, uint64_t now, vcw_request* out);
void vcw_clear(void* data, size_t length);
#endif
