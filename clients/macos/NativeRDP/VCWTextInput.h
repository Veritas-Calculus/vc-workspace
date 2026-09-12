#ifndef VCW_TEXT_INPUT_H
#define VCW_TEXT_INPUT_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

enum VCWTextResult { VCWTextSent, VCWTextInvalid, VCWTextUnavailable, VCWTextFailed };
typedef bool (*VCWTextCurrent)(void *context);
typedef bool (*VCWTextEmit)(void *context, uint16_t code, bool release);

/* A committed IME string, never marked/preedit text or clipboard data.
 * Validate the entire UTF-16 snapshot before the first event. Callers retain
 * one session for the synchronous call; current must never resolve a new one.
 * Partial sends are not retried: remote text is not a transactional resource. */
static inline enum VCWTextResult vcw_send_committed_text(const uint16_t *text, size_t length,
                                                        void *context, VCWTextCurrent current,
                                                        VCWTextEmit emit)
{
    if (!text || !length || length > 4096 || !current || !emit) return VCWTextInvalid;
    for (size_t i = 0; i < length; i++) {
        uint16_t code = text[i];
        /* Navigation/control actions must use the physical-key path. */
        if (code < 0x20 || code == 0x7f) return VCWTextInvalid;
        if (code >= 0xd800 && code <= 0xdbff) {
            if (++i == length || text[i] < 0xdc00 || text[i] > 0xdfff) return VCWTextInvalid;
        } else if (code >= 0xdc00 && code <= 0xdfff) return VCWTextInvalid;
    }
    for (size_t i = 0; i < length; i++) {
        if (!current(context)) return VCWTextUnavailable;
        if (!emit(context, text[i], false)) return VCWTextFailed;
        if (!current(context)) return VCWTextUnavailable;
        if (!emit(context, text[i], true)) return VCWTextFailed;
    }
    return VCWTextSent;
}
#endif
