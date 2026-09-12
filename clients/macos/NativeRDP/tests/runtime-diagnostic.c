#include <inttypes.h>
#ifdef VCW_TEST_WINPR_FORMAT
#undef PRIx8
#define PRIx8 "hhx"
#endif
#include "../VCWRuntimeDiagnostic.h"
#include <assert.h>

int main(void)
{
    VCWTCPDiagnostic tcp = {0};
    const char* tf = "VCW TCP connect error=%d";
    const char* ts = "freerdp_tcp_connect_timeout";
    assert(vcw_tcp_diagnostic(ts, tf, "VCW TCP connect error=10061", &tcp) && tcp.stage == 1 && tcp.code == 10061);
    assert(vcw_tcp_diagnostic(ts, "VCW TCP socket error=%d", "VCW TCP socket error=61", &tcp) && tcp.stage == 2 && tcp.code == 61);
    assert(vcw_tcp_diagnostic(ts, "VCW TCP wait timed out", "VCW TCP wait timed out", &tcp) && tcp.stage == 3 && tcp.code == 0);
    const char* invalidTCP[] = {"VCW TCP connect error=0", "VCW TCP connect error=-1", "VCW TCP connect error=+1",
        "VCW TCP connect error=061", "VCW TCP connect error=65536", "VCW TCP connect error=999999999999999999",
        "VCW TCP connect error=61\nsecret", "VCW TCP connect error=61 host=private", "VCW TCP connect error=", "VCW TCP connect error= 61"};
    for (size_t i = 0; i < sizeof(invalidTCP)/sizeof(invalidTCP[0]); i++)
        assert(!vcw_tcp_diagnostic(ts, tf, invalidTCP[i], &tcp));
    assert(!vcw_tcp_diagnostic("other", tf, "VCW TCP connect error=61", &tcp));
    assert(!vcw_tcp_diagnostic(ts, "%s", "VCW TCP connect error=61", &tcp));
    assert(!vcw_tcp_diagnostic(ts, tf, "VCW TCP socket error=61", &tcp));
    assert(!vcw_tcp_diagnostic(ts, "VCW TCP wait timed out", "VCW TCP wait timed out private", &tcp));
    assert(!vcw_tcp_diagnostic(NULL, tf, "", &tcp));
    assert(!vcw_tcp_diagnostic(ts, NULL, "", &tcp));
    assert(!vcw_tcp_diagnostic(ts, tf, NULL, &tcp));
    assert(!vcw_tcp_diagnostic(ts, tf, "", NULL));
    const char* paste = "VCW paste rejected: desktop does not own active focus";
    const char* site = "-[MRDPView paste:]_block_invoke";
    assert(vcw_paste_diagnostic(site, paste, paste) == 2);
    assert(!vcw_paste_diagnostic("unrelated", paste, paste));
    assert(!vcw_paste_diagnostic(site, "%s", paste));
    assert(!vcw_paste_diagnostic(site, paste, "private"));
    assert(!vcw_paste_diagnostic(site, paste, "VCW paste rejected: desktop does not own active focus\nprivate"));
    assert(!vcw_paste_diagnostic(NULL, paste, paste));
    const char* function = "fastpath_recv_update";
    const char* format = "Fastpath update %s [%" PRIx8 "] failed, status %d";
    assert(vcw_fastpath_failure_type(function, format, "Fastpath update Cached Pointer [a] failed, status 0") == 10);
    assert(vcw_fastpath_failure_type(function, format, "Fastpath update Orders [0] failed, status 0") == 0);
    assert(vcw_fastpath_failure_type(function, format, "Fastpath update Surface Commands [4] failed, status -1") == 4);
    assert(vcw_fastpath_failure_type(function, format, "Fastpath update Cached Pointer [a] failed, status 0\nprivate") == -1);
    assert(vcw_fastpath_failure_type(function, format, "Fastpath update private [a] failed, status 0") == -1);
    assert(vcw_fastpath_failure_type(function, format, "Fastpath update Cached Pointer [a] failed, status 100") == -1);
    assert(vcw_fastpath_failure_type(function, format, "Fastpath update Cached Pointer [0] failed, status 0") == -1);
    assert(vcw_fastpath_failure_type(function, "%s", "Fastpath update Cached Pointer [a] failed, status 0") == -1);
    assert(vcw_fastpath_failure_type("unrelated", format, "Fastpath update Cached Pointer [a] failed, status 0") == -1);
    assert(vcw_fastpath_failure_type(NULL, format, "") == -1);
    assert(vcw_fastpath_failure_type(function, NULL, "") == -1);
    assert(vcw_fastpath_failure_type(function, format, NULL) == -1);
    VCWPointerDiagnostic p = {0};
    const char* pf = "VCW pointer cache op=%u index=%u capacity=%u";
    assert(vcw_pointer_diagnostic("pointer_cache_new", pf, "VCW pointer cache op=0 index=0 capacity=26", &p) && p.capacity == 26);
    assert(vcw_pointer_diagnostic("pointer_cache_put", pf, "VCW pointer cache op=1 index=21 capacity=26", &p) && p.index == 21);
    assert(vcw_pointer_diagnostic("update_pointer_cached", pf, "VCW pointer cache op=2 index=1 capacity=26", &p) && p.operation == 2);
    assert(!vcw_pointer_diagnostic("unrelated", pf, "VCW pointer cache op=1 index=21 capacity=26", &p));
    assert(!vcw_pointer_diagnostic("pointer_cache_new", pf, "VCW pointer cache op=0 index=1 capacity=26", &p));
    assert(!vcw_pointer_diagnostic("pointer_cache_put", "%s", "VCW pointer cache op=1 index=21 capacity=26", &p));
    const char* bad[] = {"VCW pointer cache op=1 index=21 capacity=26\nprivate", "VCW pointer cache op=1 index=999999999999999999999 capacity=26",
        "VCW pointer cache op=1 index=-1 capacity=26", "VCW pointer cache op=1 index=+1 capacity=26", "VCW pointer cache op=1 index=01 capacity=26",
        "VCW pointer cache op=1 index=65536 capacity=26", "VCW pointer cache op=1 index=0 capacity=65537", "VCW pointer cache op=1 index=0 capacity=0"};
    for (size_t i = 0; i < sizeof(bad)/sizeof(bad[0]); i++) assert(!vcw_pointer_diagnostic("pointer_cache_put", pf, bad[i], &p));
    assert(!vcw_pointer_diagnostic(NULL, pf, "", &p));
    assert(!vcw_pointer_diagnostic("pointer_cache_put", NULL, "", &p));
    assert(!vcw_pointer_diagnostic("pointer_cache_put", pf, NULL, &p));
    assert(!vcw_pointer_diagnostic("pointer_cache_put", pf, "", NULL));
    puts("PASS: runtime diagnostic allowlists");
}
