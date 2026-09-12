// SPDX-License-Identifier: Apache-2.0
// One headless RDP transport owned by a supervisor pipe and a hard deadline.
// No local display server, shell, interactive prompts or user input channel.
#define _POSIX_C_SOURCE 200809L
#include "protocol.h"
#include <errno.h>
#include <inttypes.h>
#include <poll.h>
#include <pthread.h>
#include <signal.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <sys/resource.h>
#include <sys/stat.h>
#include <freerdp/freerdp.h>
#include <freerdp/gdi/gdi.h>
#include <freerdp/settings.h>
#include <winpr/synch.h>
#include <winpr/wlog.h>

typedef struct {
    rdpContext context;
    uint64_t expires, monotonic_end;
    atomic_bool stopping, finished;
    bool frame_sent;
    const char* failure;
} worker_context;

static void setup_failed(unsigned code) {
    printf("{\"version\":1,\"event\":\"failed\",\"reason\":\"setup\",\"code\":%u}\n", code);
    fflush(stdout);
}

static uint64_t clock_ms(clockid_t clock) {
    struct timespec value;
    if (clock_gettime(clock, &value) != 0 || value.tv_sec < 0) return UINT64_MAX;
    return (uint64_t)value.tv_sec * 1000 + (uint64_t)value.tv_nsec / 1000000;
}

static bool read_exact(uint8_t* data, size_t size, uint64_t deadline) {
    while (size) {
        if (clock_ms(CLOCK_MONOTONIC) >= deadline) return false;
        struct pollfd input = {.fd = STDIN_FILENO, .events = POLLIN};
        int ready = poll(&input, 1, 100);
        if (ready < 0 && errno == EINTR) continue;
        if (ready < 0 || (ready > 0 && (input.revents & (POLLERR | POLLNVAL)))) return false;
        if (ready == 0) continue;
        ssize_t count = read(STDIN_FILENO, data, size);
        if (count < 0 && errno == EINTR) continue;
        if (count <= 0) return false;
        data += count; size -= (size_t)count;
    }
    return true;
}

static void* supervise(void* raw) {
    worker_context* worker = raw;
    while (!atomic_load(&worker->finished)) {
        struct pollfd input = {.fd = STDIN_FILENO, .events = POLLIN};
        int status = poll(&input, 1, 100);
        // EOF, extra commands, invalid descriptor, parent death or expiry all
        // close this transport. There is no implicit retry or lease renewal.
        if (atomic_load(&worker->stopping) || (status < 0 && errno != EINTR) || (status > 0 && input.revents) ||
            clock_ms(CLOCK_REALTIME) >= worker->expires || clock_ms(CLOCK_MONOTONIC) >= worker->monotonic_end) {
            atomic_store(&worker->stopping, true);
            freerdp_abort_connect_context(&worker->context);
            // A cancellation just before freerdp_connect must survive its
            // initialization/reset. Continue fencing until main acknowledges
            // completion, without spinning on a permanently closed pipe.
            const struct timespec pause = {.tv_sec=0, .tv_nsec=100000000};
            nanosleep(&pause, NULL);
        }
    }
    return NULL;
}

static BOOL begin_paint(rdpContext* context) {
    rdpGdi* gdi = context->gdi;
    if (!gdi || !gdi->primary || !gdi->primary->hdc || !gdi->primary->hdc->hwnd || !gdi->primary->hdc->hwnd->invalid) return FALSE;
    gdi->primary->hdc->hwnd->invalid->null = TRUE;
    return TRUE;
}

static BOOL end_paint(rdpContext* context) {
    worker_context* worker = (worker_context*)context;
    rdpGdi* gdi = context->gdi;
    if (!gdi || !gdi->primary || !gdi->primary->hdc || !gdi->primary->hdc->hwnd || !gdi->primary->hdc->hwnd->invalid) return FALSE;
    if (!worker->frame_sent && !gdi->primary->hdc->hwnd->invalid->null) {
        worker->frame_sent = true;
        printf("{\"version\":1,\"event\":\"frame\",\"width\":%u,\"height\":%u}\n", gdi->width, gdi->height);
        if (fflush(stdout) != 0) return FALSE;
    }
    return TRUE;
}

static BOOL desktop_resize(rdpContext* context) {
    UINT32 width = freerdp_settings_get_uint32(context->settings, FreeRDP_DesktopWidth);
    UINT32 height = freerdp_settings_get_uint32(context->settings, FreeRDP_DesktopHeight);
    if (width < 320 || height < 240 || width > 4096 || height > 2160) return FALSE;
    return gdi_resize(context->gdi, width, height);
}

static BOOL post_connect(freerdp* instance) {
    rdpContext* context = instance->context;
    UINT32 width = freerdp_settings_get_uint32(context->settings, FreeRDP_DesktopWidth);
    UINT32 height = freerdp_settings_get_uint32(context->settings, FreeRDP_DesktopHeight);
    if (width < 320 || height < 240 || width > 4096 || height > 2160 || !gdi_init(instance, PIXEL_FORMAT_XRGB32)) return FALSE;
    context->update->BeginPaint = begin_paint;
    context->update->EndPaint = end_paint;
    context->update->DesktopResize = desktop_resize;
    return TRUE;
}

static int reject_certificate(freerdp* instance, const BYTE* data, size_t length, const char* hostname, UINT16 port, DWORD flags) {
    (void)data; (void)length; (void)hostname; (void)port; (void)flags;
    ((worker_context*)instance->context)->failure = "certificate";
    // FreeRDP checks our single SHA256 pin BEFORE external verification. Any
    // unmatched certificate is denied, even if a system CA would trust it.
    return 0;
}
static BOOL reject_redirect(freerdp* instance) { ((worker_context*)instance->context)->failure = "redirect"; return FALSE; }
static BOOL authenticate(freerdp* instance, char** user, char** password, char** domain, rdp_auth_reason reason) {
    (void)instance; (void)reason;
    return user && *user && **user && password && *password && **password && domain && *domain && **domain;
}

static bool configure(freerdp* instance, const vcw_request* request) {
    rdpSettings* settings = instance->context->settings;
    const char* runtime = getenv("XDG_CONFIG_HOME");
    struct stat info;
    if (!runtime || runtime[0] != '/' || lstat(runtime, &info) != 0 || !S_ISDIR(info.st_mode) ||
        info.st_uid != getuid() || (info.st_mode & 077) != 0) return false;
    if (!freerdp_settings_set_string(settings, FreeRDP_HomePath, runtime) ||
        !freerdp_settings_set_string(settings, FreeRDP_ConfigPath, runtime) ||
        !freerdp_settings_set_string(settings, FreeRDP_ActionScript, NULL) ||
        !freerdp_settings_set_string(settings, FreeRDP_ServerHostname, request->host) ||
        !freerdp_settings_set_string(settings, FreeRDP_Username, request->username) ||
        !freerdp_settings_set_string(settings, FreeRDP_Password, request->password) ||
        !freerdp_settings_set_string(settings, FreeRDP_Domain, request->domain) ||
        !freerdp_settings_set_string(settings, FreeRDP_ClientHostname, "VCW-SESSION") ||
        !freerdp_settings_set_string(settings, FreeRDP_AuthenticationPackageList, "ntlm") ||
        !freerdp_settings_set_string(settings, FreeRDP_CertificateAcceptedFingerprints, request->fingerprint) ||
        !freerdp_settings_set_uint32(settings, FreeRDP_ServerPort, request->port) ||
        !freerdp_settings_set_uint32(settings, FreeRDP_DesktopWidth, request->width) ||
        !freerdp_settings_set_uint32(settings, FreeRDP_DesktopHeight, request->height) ||
        !freerdp_settings_set_uint32(settings, FreeRDP_ColorDepth, 32) ||
        !freerdp_settings_set_uint32(settings, FreeRDP_TcpConnectTimeout, 10000)) return false;
    const size_t enabled[] = {FreeRDP_ExternalCertificateManagement, FreeRDP_NlaSecurity,
        FreeRDP_Authentication, FreeRDP_SoftwareGdi, FreeRDP_FastPathOutput, FreeRDP_RemoteFxCodec};
    const size_t disabled[] = {FreeRDP_IgnoreCertificate, FreeRDP_AutoAcceptCertificate,
        FreeRDP_TlsSecurity, FreeRDP_RdpSecurity, FreeRDP_ExtSecurity, FreeRDP_RdstlsSecurity,
        FreeRDP_GatewayEnabled, FreeRDP_AuthenticationOnly, FreeRDP_AutoReconnectionEnabled,
        FreeRDP_SupportGraphicsPipeline, FreeRDP_GfxH264, FreeRDP_GfxAVC444, FreeRDP_GfxAVC444v2,
        FreeRDP_AudioPlayback, FreeRDP_DeviceRedirection, FreeRDP_RedirectClipboard,
        FreeRDP_RedirectDrives, FreeRDP_RedirectHomeDrive, FreeRDP_RedirectPrinters,
        FreeRDP_RedirectSmartCards, FreeRDP_RedirectWebAuthN, FreeRDP_RedirectSerialPorts,
        FreeRDP_RedirectParallelPorts, FreeRDP_DynamicResolutionUpdate, FreeRDP_TransportDump,
        FreeRDP_DeactivateClientDecoding};
    for (size_t i = 0; i < sizeof(enabled)/sizeof(*enabled); i++) if (!freerdp_settings_set_bool(settings, enabled[i], TRUE)) return false;
    for (size_t i = 0; i < sizeof(disabled)/sizeof(*disabled); i++) if (!freerdp_settings_set_bool(settings, disabled[i], FALSE)) return false;
    instance->PostConnect = post_connect;
    instance->VerifyX509Certificate = reject_certificate;
    instance->Redirect = reject_redirect;
    instance->AuthenticateEx = authenticate;
    return true;
}

int main(int argc, char** argv) {
    (void)argv;
    if (argc != 1) return 2;
    const struct rlimit no_core = {0, 0};
    if (setrlimit(RLIMIT_CORE, &no_core) != 0) return 2;
    signal(SIGPIPE, SIG_IGN);
    // Never install diagnostic crash handlers, load CLI/config files or expose
    // library logs containing server-provided text, paths or credentials.
    setenv("WLOG_LEVEL", "OFF", 1);
    WLog_SetLogLevel(WLog_GetRoot(), WLOG_OFF);
    uint8_t frame[VCW_MAX_FRAME] = {0}, header[4] = {0};
    vcw_request request = {0};
    uint64_t deadline = clock_ms(CLOCK_MONOTONIC) + 5000;
    if (!read_exact(header, 4, deadline)) return 2;
    uint32_t length = ((uint32_t)header[0] << 24) | ((uint32_t)header[1] << 16) | ((uint32_t)header[2] << 8) | header[3];
    bool valid = length <= sizeof(frame) && read_exact(frame, length, deadline) && vcw_decode(frame, length, clock_ms(CLOCK_REALTIME), &request);
    vcw_clear(frame, sizeof(frame));
    if (!valid) return 2;
    int result = 1;
    freerdp* instance = freerdp_new();
    if (!instance) { setup_failed(1); goto clear; }
    instance->ContextSize = sizeof(worker_context);
    if (!freerdp_context_new(instance)) { setup_failed(2); freerdp_free(instance); goto clear; }
    worker_context* worker = (worker_context*)instance->context;
    atomic_init(&worker->stopping, false);
    atomic_init(&worker->finished, false);
    worker->expires = request.expires_unix_ms;
    uint64_t now = clock_ms(CLOCK_REALTIME);
    worker->monotonic_end = clock_ms(CLOCK_MONOTONIC) + (worker->expires > now ? worker->expires - now : 0);
    if (!configure(instance, &request)) { setup_failed(3); goto free_context; }
    vcw_clear(&request, sizeof(request));
    pthread_t guard;
    if (pthread_create(&guard, NULL, supervise, worker) != 0) { setup_failed(4); goto free_context; }
    if (freerdp_connect(instance)) {
        puts("{\"version\":1,\"event\":\"connected\"}");
        if (fflush(stdout) == 0) {
            while (!atomic_load(&worker->stopping) && !freerdp_shall_disconnect_context(instance->context)) {
                HANDLE handles[MAXIMUM_WAIT_OBJECTS] = {0};
                DWORD count = freerdp_get_event_handles(instance->context, handles, MAXIMUM_WAIT_OBJECTS);
                if (!count || WaitForMultipleObjects(count, handles, FALSE, 100) == WAIT_FAILED || !freerdp_check_event_handles(instance->context)) break;
            }
            result = worker->frame_sent ? 0 : 1;
        }
    } else {
        // Only our closed vocabulary crosses this pipe, never server strings,
        // usernames, certificate contents or raw library diagnostics.
        printf("{\"version\":1,\"event\":\"failed\",\"reason\":\"%s\",\"code\":%" PRIu32 "}\n", worker->failure ? worker->failure : "connect", freerdp_get_last_error(instance->context));
        fflush(stdout);
    }
    atomic_store(&worker->finished, true);
    pthread_join(guard, NULL);
    freerdp_disconnect(instance);
free_context:
    gdi_free(instance);
    freerdp_context_free(instance);
    freerdp_free(instance);
clear:
    vcw_clear(&request, sizeof(request));
    return result;
}
