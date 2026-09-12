#ifndef VCW_RUNTIME_DIAGNOSTIC_H
#define VCW_RUNTIME_DIAGNOSTIC_H

#include <inttypes.h>
#include <stdio.h>
#include <string.h>

typedef struct { unsigned operation, index, capacity; } VCWPointerDiagnostic;

/* Errors from our pinned TCP call site only. Winsock and SO_ERROR have
 * distinct namespaces; retain the stage, not endpoint-bearing library text. */
typedef struct { unsigned stage, code; } VCWTCPDiagnostic;
static inline int vcw_tcp_diagnostic(const char* function, const char* format,
                                     const char* text, VCWTCPDiagnostic* result)
{
    if (!function || !format || !text || !result ||
        strcmp(function, "freerdp_tcp_connect_timeout")) return 0;
    if (!strcmp(format, "VCW TCP wait timed out") && !strcmp(text, format)) {
        *result = (VCWTCPDiagnostic){3, 0};
        return 1;
    }
    const char* const formats[] = {"VCW TCP connect error=%d", "VCW TCP socket error=%d"};
    for (unsigned i = 0; i < 2; i++) {
        if (strcmp(format, formats[i])) continue;
        const char* prefix = i == 0 ? "VCW TCP connect error=" : "VCW TCP socket error=";
        const size_t length = strlen(prefix);
        if (strncmp(text, prefix, length)) return 0;
        const char* digits = text + length;
        unsigned code = 0, count = 0;
        if (*digits < '1' || *digits > '9') return 0;
        while (*digits >= '0' && *digits <= '9') {
            if (++count > 5) return 0;
            code = code * 10 + (unsigned)(*digits++ - '0');
        }
        if (*digits || code > 65535) return 0;
        *result = (VCWTCPDiagnostic){i + 1, code};
        return 1;
    }
    return 0;
}

/* Static messages only: never forward clipboard contents or arbitrary text. */
static inline int vcw_paste_diagnostic(const char* function, const char* format, const char* text)
{
    if (!function || !format || !text || strcmp(format, text)) return 0;
    const char* const messages[] = {
        "VCW paste rejected: channel or policy unavailable",
        "VCW paste rejected: desktop does not own active focus",
        "VCW paste cancelled: readiness or focus changed",
        "VCW paste cancelled: version changed, confirmation rejected or timed out",
        "VCW paste rejected: confirmed snapshot has no supported text"
    };
    for (unsigned i = 0; i < sizeof(messages) / sizeof(messages[0]); i++) {
        const char* site = i < 2 ? "-[MRDPView paste:]_block_invoke" : "-[MRDPView vcwAdvancePaste]_block_invoke";
        if (!strcmp(function, site) && !strcmp(text, messages[i])) return (int)i + 1;
    }
    return 0;
}

/* Only canonical, bounded integers from three compiled-in call sites. */
static inline int vcw_pointer_diagnostic(const char* function, const char* format,
                                         const char* text, VCWPointerDiagnostic* result)
{
    const char* const expected = "VCW pointer cache op=%u index=%u capacity=%u";
    if (!function || !format || !text || !result || strcmp(format, expected)) return 0;
    unsigned operation, index, capacity;
    int end = 0;
    if (sscanf(text, "VCW pointer cache op=%1u index=%5u capacity=%5u%n", &operation, &index, &capacity, &end) != 3 ||
        !end || text[end] || operation > 2 || index > 65535 || !capacity || capacity > 65536) return 0;
    const char* const sites[] = {"pointer_cache_new", "pointer_cache_put", "update_pointer_cached"};
    if (strcmp(function, sites[operation]) || (operation == 0 && index != 0)) return 0;
    char canonical[96];
    const int count = snprintf(canonical, sizeof(canonical), expected, operation, index, capacity);
    if (count != end || strcmp(text, canonical)) return 0;
    *result = (VCWPointerDiagnostic){operation, index, capacity};
    return 1;
}

/* Do not forward library log text. Recognize only complete, fixed FastPath
 * failure messages from the pinned runtime and return a protocol enum. An
 * unknown format, status or extra byte produces no additional diagnostic. */
static inline int vcw_fastpath_failure_type(const char* function, const char* format,
                                            const char* text)
{
    const char* const expectedFormat = "Fastpath update %s [%" PRIx8 "] failed, status %d";
    if (!function || !format || !text || strcmp(function, "fastpath_recv_update") != 0 ||
        strcmp(format, expectedFormat) != 0)
        return -1;
    static const char* const names[] = {
        "Orders", "Bitmap", "Palette", "Synchronize", "Surface Commands",
        "System Pointer Hidden", "System Pointer Default", "???", "Pointer Position",
        "Color Pointer", "Cached Pointer", "New Pointer", "UNKNOWN", "UNKNOWN", "UNKNOWN", "UNKNOWN"
    };
    for (unsigned int type = 0; type < sizeof(names) / sizeof(names[0]); type++)
    {
        for (int status = -1; status <= 0; status++)
        {
            char expected[128];
            int count = snprintf(expected, sizeof(expected), expectedFormat, names[type], (uint8_t)type, status);
            if (count > 0 && (size_t)count < sizeof(expected) &&
                strncmp(text, expected, (size_t)count + 1) == 0)
                return (int)type;
        }
    }
    return -1;
}

#endif
