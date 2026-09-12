/* Real pinned FreeRDP peer/client over anonymous local sockets. No AppKit,
 * Guest, OS credentials, listening port, certificate files or system trust.
 * The renderer records pointer lifecycle; this does not prove Mac visual QA. */
#include <freerdp/client.h>
#include <freerdp/peer.h>
#include <freerdp/error.h>
#include <freerdp/graphics.h>
#include <freerdp/crypto/certificate.h>
#include <freerdp/crypto/privatekey.h>
#include <freerdp/transport_io.h>
#include <openssl/pem.h>
#include <openssl/rsa.h>
#include <openssl/x509.h>
#include <winpr/synch.h>
#include <winpr/wlog.h>
#include <winpr/ssl.h>
#include <pthread.h>
#include <signal.h>
#include <stdatomic.h>
#include <sys/socket.h>
#include <unistd.h>
#include "../VCWRuntimeDiagnostic.h"

#define REQUIRE(value) do { if (!(value)) { fprintf(stderr, "fixture assertion at line %d\n", __LINE__); exit(1); } } while (0)

static atomic_int clientSocket = -1;
static atomic_int pointerSets, pointerNews, pointerFrees, resizes, activations;
static atomic_int failedType = -1;
static atomic_bool clientReady, stopPeer, peerDone;
static X509* expectedCertificate;
static char *certificatePEM, *privateKeyPEM;
static int rounds, omitPointer;

static BOOL log_message(const wLogMessage* message)
{
    if (message && message->Level >= WLOG_ERROR) {
        int type = vcw_fastpath_failure_type(message->FunctionName, message->FormatString, message->TextString);
        if (type >= 0) atomic_store(&failedType, type);
        /* Test input and peer are generated here; even so, do not emit text. */
        fprintf(stderr, "runtime failure at %s:%zu\n", message->FunctionName, message->LineNumber);
    }
    return TRUE;
}

static char* pem_copy(BIO* bio)
{
    BUF_MEM* memory = NULL;
    BIO_get_mem_ptr(bio, &memory);
    REQUIRE(memory && memory->length < 16384);
    char* result = calloc(memory->length + 1, 1);
    REQUIRE(result);
    memcpy(result, memory->data, memory->length);
    BIO_free(bio);
    return result;
}

static void create_certificate(void)
{
    EVP_PKEY* key = EVP_RSA_gen(2048);
    REQUIRE(key);
    expectedCertificate = X509_new();
    REQUIRE(expectedCertificate && X509_set_version(expectedCertificate, 2) == 1);
    REQUIRE(ASN1_INTEGER_set(X509_get_serialNumber(expectedCertificate), 1) == 1);
    REQUIRE(X509_gmtime_adj(X509_getm_notBefore(expectedCertificate), -60));
    REQUIRE(X509_gmtime_adj(X509_getm_notAfter(expectedCertificate), 3600));
    REQUIRE(X509_set_pubkey(expectedCertificate, key) == 1);
    X509_NAME* name = X509_get_subject_name(expectedCertificate);
    REQUIRE(X509_NAME_add_entry_by_txt(name, "CN", MBSTRING_ASC, (const unsigned char*)"localhost", -1, -1, 0) == 1);
    REQUIRE(X509_set_issuer_name(expectedCertificate, name) == 1);
    REQUIRE(X509_sign(expectedCertificate, key, EVP_sha256()) > 0);
    BIO* cert = BIO_new(BIO_s_mem());
    BIO* secret = BIO_new(BIO_s_mem());
    REQUIRE(cert && secret && PEM_write_bio_X509(cert, expectedCertificate) == 1);
    REQUIRE(PEM_write_bio_PrivateKey(secret, key, NULL, NULL, 0, NULL, NULL) == 1);
    certificatePEM = pem_copy(cert);
    privateKeyPEM = pem_copy(secret);
    EVP_PKEY_free(key);
}

static int verify_certificate(freerdp* instance, const BYTE* data, size_t length,
                              const char* hostname, UINT16 port, DWORD flags)
{
    (void)instance; (void)flags;
    if (!data || length > 16384 || strcmp(hostname, "localhost") || port != 3389) return 0;
    BIO* bio = BIO_new_mem_buf(data, (int)length);
    X509* cert = bio ? PEM_read_bio_X509(bio, NULL, NULL, NULL) : NULL;
    int result = cert && X509_cmp(cert, expectedCertificate) == 0 ? 2 : 0;
    X509_free(cert); BIO_free(bio);
    return result;
}

static int connect_socket(rdpContext* context, rdpSettings* settings, const char* host,
                          int port, DWORD timeout)
{
    (void)context; (void)settings; (void)timeout;
    if (strcmp(host, "localhost") || port != 3389) return -1;
    return atomic_exchange(&clientSocket, -1);
}

static BOOL pointer_new(rdpContext* context, rdpPointer* pointer)
{
    (void)context;
    REQUIRE(pointer->width == 16 && pointer->height == 16);
    atomic_fetch_add(&pointerNews, 1);
    return TRUE;
}
static BOOL pointer_set(rdpContext* context, rdpPointer* pointer)
{
    (void)context; (void)pointer;
    atomic_fetch_add(&pointerSets, 1);
    return TRUE;
}
static void pointer_free(rdpContext* context, rdpPointer* pointer)
{
    (void)context; (void)pointer;
    atomic_fetch_add(&pointerFrees, 1);
}
static BOOL resized(rdpContext* context)
{
    REQUIRE(freerdp_settings_get_uint32(context->settings, FreeRDP_DesktopWidth) == 800);
    atomic_fetch_add(&resizes, 1);
    return TRUE;
}
static BOOL post_connect(freerdp* instance)
{
    rdpPointer pointer = {0};
    pointer.size = sizeof(pointer);
    pointer.New = pointer_new; pointer.Set = pointer_set; pointer.Free = pointer_free;
    graphics_register_pointer(instance->context->graphics, &pointer);
    instance->context->update->DesktopResize = resized;
    return TRUE;
}
static BOOL peer_activated(freerdp_peer* peer)
{
    (void)peer;
    atomic_fetch_add(&activations, 1);
    return TRUE;
}
static BOOL peer_connected(freerdp_peer* peer) { (void)peer; return TRUE; }
static void send_pointer(freerdp_peer* peer, UINT16 index)
{
    BYTE xorMask[16 * 16 * 4], andMask[16 * 2] = {0};
    memset(xorMask, index ? 0xff : 0x80, sizeof(xorMask));
    POINTER_NEW_UPDATE update = {0};
    update.xorBpp = 32;
    update.colorPtrAttr.cacheIndex = index;
    update.colorPtrAttr.width = 16; update.colorPtrAttr.height = 16;
    update.colorPtrAttr.lengthAndMask = sizeof(andMask);
    update.colorPtrAttr.lengthXorMask = sizeof(xorMask);
    update.colorPtrAttr.andMaskData = andMask; update.colorPtrAttr.xorMaskData = xorMask;
    REQUIRE(peer->context->update->pointer->PointerNew(peer->context, &update));
}

static void* peer_thread(void* argument)
{
    freerdp_peer* peer = argument;
    REQUIRE(freerdp_peer_context_new(peer));
    rdpSettings* settings = peer->context->settings;
    rdpCertificate* cert = freerdp_certificate_new_from_pem(certificatePEM);
    rdpPrivateKey* key = freerdp_key_new_from_pem(privateKeyPEM);
    REQUIRE(cert && key);
    REQUIRE(freerdp_settings_set_pointer_len(settings, FreeRDP_RdpServerCertificate, cert, 1));
    REQUIRE(freerdp_settings_set_pointer_len(settings, FreeRDP_RdpServerRsaKey, key, 1));
    REQUIRE(freerdp_settings_set_bool(settings, FreeRDP_RdpSecurity, FALSE));
    REQUIRE(freerdp_settings_set_bool(settings, FreeRDP_NlaSecurity, FALSE));
    REQUIRE(freerdp_settings_set_bool(settings, FreeRDP_TlsSecurity, TRUE));
    REQUIRE(freerdp_settings_set_bool(settings, FreeRDP_FastPathOutput, TRUE));
    peer->PostConnect = peer_connected; peer->Activate = peer_activated;
    REQUIRE(peer->Initialize(peer));
    int stage = 0, generation = 1, expectedSets = 0;
    BOOL healthy = TRUE;
    while (!atomic_load(&stopPeer)) {
        HANDLE handles[32];
        DWORD count = peer->GetEventHandles(peer, handles, 32);
        REQUIRE(count > 0 && count <= 32);
        DWORD result = WaitForMultipleObjects(count, handles, FALSE, 10);
        REQUIRE(result != WAIT_FAILED);
        if (!peer->CheckFileDescriptor(peer)) { healthy = FALSE; break; }
        if (!atomic_load(&clientReady) || !peer->activated) continue;
        if (stage == 0) {
            if (!omitPointer || generation == 1) { send_pointer(peer, 0); send_pointer(peer, 1); expectedSets += 2; }
            POINTER_CACHED_UPDATE cached = {.cacheIndex = 1};
            REQUIRE(peer->context->update->pointer->PointerCached(peer->context, &cached));
            expectedSets++;
            stage = 1;
        } else if (stage == 1 && atomic_load(&pointerSets) >= expectedSets) {
            if (generation > rounds) { atomic_store(&peerDone, true); break; }
            REQUIRE(freerdp_settings_set_uint32(settings, FreeRDP_DesktopHeight, 600 + generation * 4));
            REQUIRE(peer->context->update->DesktopResize(peer->context));
            generation++; stage = 2;
        } else if (stage == 2 && atomic_load(&activations) >= generation) {
            stage = 0;
        }
    }
    while (healthy && !atomic_load(&stopPeer)) Sleep(1);
    peer->Disconnect(peer);
    freerdp_peer_context_free(peer); freerdp_peer_free(peer);
    return NULL;
}

int main(int argc, char** argv)
{
    REQUIRE(argc == 2);
    const BOOL existingError = !strcmp(argv[1], "existing-error");
    const BOOL cancelled = !strcmp(argv[1], "cancelled");
    REQUIRE(!strcmp(argv[1], "normal") || !strcmp(argv[1], "missing-pointer") || existingError || cancelled);
    omitPointer = !strcmp(argv[1], "missing-pointer") || existingError; rounds = omitPointer ? 1 : 32;
    signal(SIGPIPE, SIG_IGN); alarm(40);
    wLogCallbacks callbacks = {.message = log_message};
    wLog* root = WLog_GetRoot();
    REQUIRE(root && WLog_SetLogAppenderType(root, WLOG_APPENDER_CALLBACK));
    REQUIRE(WLog_ConfigureAppender(WLog_GetLogAppender(root), "callbacks", &callbacks));
    REQUIRE(winpr_InitializeSSL(WINPR_SSL_INIT_DEFAULT));
    create_certificate();
    int sockets[2]; REQUIRE(socketpair(AF_UNIX, SOCK_STREAM, 0, sockets) == 0);
    atomic_store(&clientSocket, sockets[0]);
    freerdp_peer* peer = freerdp_peer_new(sockets[1]); REQUIRE(peer);
    pthread_t thread; REQUIRE(pthread_create(&thread, NULL, peer_thread, peer) == 0);
    RDP_CLIENT_ENTRY_POINTS entry = {0};
    entry.Size = sizeof(entry); entry.Version = RDP_CLIENT_INTERFACE_VERSION; entry.ContextSize = sizeof(rdpContext);
    rdpContext* context = freerdp_client_context_new(&entry); REQUIRE(context);
    rdpTransportIo io = *freerdp_get_io_callbacks(context); io.TCPConnect = connect_socket;
    REQUIRE(freerdp_set_io_callbacks(context, &io));
    rdpSettings* settings = context->settings;
    REQUIRE(freerdp_settings_set_string(settings, FreeRDP_ServerHostname, "localhost"));
    /* Synthetic protocol fields only: this peer has no OS/PAM/auth backend. */
    REQUIRE(freerdp_settings_set_string(settings, FreeRDP_Username, "protocol-fixture"));
    REQUIRE(freerdp_settings_set_string(settings, FreeRDP_Password, "unused-fixture-value"));
    REQUIRE(freerdp_settings_set_bool(settings, FreeRDP_ExternalCertificateManagement, TRUE));
    REQUIRE(freerdp_settings_set_bool(settings, FreeRDP_IgnoreCertificate, FALSE));
    REQUIRE(freerdp_settings_set_bool(settings, FreeRDP_RdpSecurity, FALSE));
    REQUIRE(freerdp_settings_set_bool(settings, FreeRDP_NlaSecurity, FALSE));
    REQUIRE(freerdp_settings_set_bool(settings, FreeRDP_TlsSecurity, TRUE));
    REQUIRE(freerdp_settings_set_bool(settings, FreeRDP_FastPathOutput, TRUE));
    REQUIRE(freerdp_settings_set_uint32(settings, FreeRDP_DesktopWidth, 800));
    REQUIRE(freerdp_settings_set_uint32(settings, FreeRDP_DesktopHeight, 600));
    context->instance->PostConnect = post_connect;
    context->instance->VerifyX509Certificate = verify_certificate;
    REQUIRE(freerdp_connect(context->instance));
    BOOL checked = TRUE;
    if (existingError) freerdp_set_last_error(context, FREERDP_ERROR_CONNECT_ACCESS_DENIED);
    if (cancelled) {
        REQUIRE(freerdp_abort_connect_context(context));
        checked = freerdp_check_event_handles(context);
    } else atomic_store(&clientReady, true);
    while (!cancelled && !atomic_load(&peerDone)) {
        HANDLE handles[32];
        DWORD count = freerdp_get_event_handles(context, handles, 32);
        REQUIRE(count > 0 && count <= 32);
        REQUIRE(WaitForMultipleObjects(count, handles, FALSE, 10) != WAIT_FAILED);
        checked = freerdp_check_event_handles(context);
        if (!checked) break;
    }
    const UINT32 endError = freerdp_get_last_error(context);
    atomic_store(&stopPeer, true);
    REQUIRE(freerdp_disconnect(context->instance));
    REQUIRE(pthread_join(thread, NULL) == 0);
    freerdp_client_context_free(context);
    if (cancelled) {
        REQUIRE(endError == FREERDP_ERROR_CONNECT_CANCELLED && atomic_load(&pointerNews) == 0 && atomic_load(&failedType) == -1);
    } else if (omitPointer) {
        REQUIRE(!checked && atomic_load(&failedType) == 10 && atomic_load(&resizes) == 1);
        REQUIRE(endError == (existingError ? FREERDP_ERROR_CONNECT_ACCESS_DENIED : FREERDP_ERROR_CONNECT_UNDEFINED));
    } else REQUIRE(checked && atomic_load(&peerDone) && atomic_load(&resizes) == rounds && atomic_load(&failedType) == -1 && endError == FREERDP_ERROR_SUCCESS);
    REQUIRE(atomic_load(&pointerNews) == atomic_load(&pointerFrees));
    REQUIRE(atomic_load(&clientSocket) == -1);
    printf("PASS: %s; resizes=%d pointer-new=%d pointer-set=%d pointer-free=%d diagnostic=%d\n",
           argv[1], atomic_load(&resizes), atomic_load(&pointerNews), atomic_load(&pointerSets),
           atomic_load(&pointerFrees), atomic_load(&failedType));
    free(certificatePEM); OPENSSL_cleanse(privateKeyPEM, strlen(privateKeyPEM)); free(privateKeyPEM); X509_free(expectedCertificate);
    alarm(0);
    return 0;
}
