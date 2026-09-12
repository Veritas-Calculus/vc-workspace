// SPDX-License-Identifier: Apache-2.0
// Narrow in-process PAM bridge. Account authority and scope ownership remain
// in the Rust Guest; this module holds logind's reference in the actual PAM
// caller and supplies the environment which pam_systemd's SessionBusy path
// does not populate. No password or arbitrary command crosses this bridge.
#define _GNU_SOURCE
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <security/pam_ext.h>
#include <security/pam_modules.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/socket.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <systemd/sd-bus.h>
#include <unistd.h>

#define GUEST "/usr/local/sbin/vc-workspace-guest-agent"
#ifdef VC_WORKSPACE_NATIVE_PAM
#define USER_PREFIX "vcw"
#define DOMAIN_COMMAND "computer-v2-native-pam-domain"
#else
#define USER_PREFIX "vca"
#define DOMAIN_COMMAND "computer-v2-pam-domain"
#endif
#define DATA_KEY "vc-workspace-logind-reference-v1"
#define EXPORT PAM_EXTERN __attribute__((visibility("default")))

struct session_reference {
    int fd;
    uint32_t uid;
    char username[32];
    char id[65];
    char runtime[64];
};

struct exchange {
    int socket;
    int pidfd;
    pid_t child;
};

static int session_id_valid(const char *id) {
    size_t size = strnlen(id, 65);
    if (size == 0 || size >= 65) return 0;
    for (size_t i = 0; i < size; i++) {
        unsigned char c = (unsigned char)id[i];
        if (!((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'))) return 0;
    }
    return strcmp(id, "auto") != 0 && strcmp(id, "self") != 0;
}

static void reference_cleanup(pam_handle_t *pamh, void *data, int status) {
    (void)pamh; (void)status;
    struct session_reference *reference = data;
    if (reference) {
        if (reference->fd >= 0) close(reference->fd);
        free(reference);
    }
}

static int user(pam_handle_t *pamh, int argc, const char **username) {
    const void *value = NULL, *service = NULL;
    if (pam_get_item(pamh, PAM_USER, &value) != PAM_SUCCESS || !value) return PAM_USER_UNKNOWN;
    *username = value;
    if (strncmp(*username, USER_PREFIX, 3) != 0) return PAM_IGNORE;
    if (argc != 0 || geteuid() != 0 || strnlen(*username, 32) != 15) return PAM_AUTH_ERR;
    for (size_t i = 3; i < 15; i++) {
        char c = (*username)[i];
        if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))) return PAM_AUTH_ERR;
    }
    if (pam_get_item(pamh, PAM_SERVICE, &service) != PAM_SUCCESS || !service || strcmp(service, "xrdp-sesman") != 0) return PAM_AUTH_ERR;
    return PAM_SUCCESS;
}

static void exchange_close(struct exchange *exchange) {
    if (exchange->socket >= 0) close(exchange->socket);
    if (exchange->pidfd >= 0) {
        struct pollfd p = {.fd = exchange->pidfd, .events = POLLIN};
        if (poll(&p, 1, 0) == 0) (void)syscall(SYS_pidfd_send_signal, exchange->pidfd, SIGKILL, NULL, 0);
        (void)poll(&p, 1, 1000);
        close(exchange->pidfd);
    }
    if (exchange->child > 0) (void)waitpid(exchange->child, NULL, WNOHANG);
    *exchange = (struct exchange){.socket = -1, .pidfd = -1, .child = -1};
}

static ssize_t receive(int fd, void *data, size_t size) {
    struct pollfd p = {.fd = fd, .events = POLLIN};
    int result;
    do result = poll(&p, 1, 5000); while (result < 0 && errno == EINTR);
    if (result != 1 || !(p.revents & POLLIN)) return -1;
    // MSG_TRUNC reports the full packet length so oversized packets cannot
    // masquerade as a valid prefix. The peer is our fixed root child.
    return recv(fd, data, size, MSG_TRUNC);
}

static int exchange_start(const char *username, const char *type, struct exchange *exchange, uint32_t *uid) {
    int pair[2] = {-1, -1};
    int nullfd = open("/dev/null", O_RDWR | O_CLOEXEC);
    if (nullfd < 0 || socketpair(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0, pair) != 0) {
        if (nullfd >= 0) close(nullfd);
        return -1;
    }
    char user_env[64], type_env[64];
    if (snprintf(user_env, sizeof user_env, "PAM_USER=%s", username) >= (int)sizeof user_env ||
        snprintf(type_env, sizeof type_env, "PAM_TYPE=%s", type) >= (int)sizeof type_env) {
        close(nullfd); close(pair[0]); close(pair[1]); return -1;
    }
    char *const args[] = {GUEST, DOMAIN_COMMAND, NULL};
    char *const environment[] = {"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "PAM_SERVICE=xrdp-sesman", user_env, type_env, NULL};
    pid_t parent = getpid();
    pid_t child = fork();
    if (child == 0) {
        // All allocation/environment work happened before fork. Parent death
        // must also terminate a child which holds the Rust registration gate.
        if (prctl(PR_SET_PDEATHSIG, SIGKILL) != 0 || getppid() != parent) _exit(126);
        if (dup2(nullfd, 1) < 0 || dup2(nullfd, 2) < 0 || dup2(pair[1], 0) < 0) _exit(126);
        if (fcntl(0, F_SETFD, 0) != 0 || syscall(SYS_close_range, 3u, ~0u, 0u) != 0) _exit(126);
        execve(GUEST, args, environment);
        _exit(126);
    }
    close(nullfd); close(pair[1]);
    if (child < 0) { close(pair[0]); return -1; }
    *exchange = (struct exchange){.socket = pair[0], .pidfd = (int)syscall(SYS_pidfd_open, child, 0), .child = child};
    if (exchange->pidfd < 0) return -1;
    unsigned char reply[12];
    if (receive(exchange->socket, reply, sizeof reply) != sizeof reply || memcmp(reply, "VCWPAM2\0", 8) != 0) return -1;
    uint32_t wire;
    memcpy(&wire, reply + 8, sizeof wire);
    *uid = ntohl(wire);
    return *uid >= 1000 && *uid < UINT32_MAX ? 0 : -1;
}

static int exchange_commit(struct exchange *exchange, const char *id) {
    size_t size = strlen(id);
    if (!session_id_valid(id) || send(exchange->socket, id, size, MSG_NOSIGNAL) != (ssize_t)size) return -1;
    unsigned char reply[8];
    if (receive(exchange->socket, reply, sizeof reply) != sizeof reply || memcmp(reply, "VCWACK2\0", 8) != 0) return -1;
    struct pollfd p = {.fd = exchange->pidfd, .events = POLLIN};
    if (poll(&p, 1, 1000) != 1) return -1;
    int status;
    if (waitpid(exchange->child, &status, 0) != exchange->child || !WIFEXITED(status) || WEXITSTATUS(status) != 0) return -1;
    exchange->child = -1;
    return 0;
}

static int create_session(struct session_reference *reference) {
    sd_bus *bus = NULL;
    sd_bus_message *request = NULL, *reply = NULL;
    sd_bus_error error = SD_BUS_ERROR_NULL;
    int result = -1;
    int pidfd = (int)syscall(SYS_pidfd_open, getpid(), 0);
    if (pidfd < 0 || sd_bus_open_system(&bus) < 0) goto done;
    if (sd_bus_message_new_method_call(bus, &request, "org.freedesktop.login1", "/org/freedesktop/login1", "org.freedesktop.login1.Manager", "CreateSessionWithPIDFD") < 0) goto done;
    if (sd_bus_message_append(request, "uhsssssussbss", reference->uid, pidfd, "xrdp-sesman", "x11", "user", "XFCE", "", (uint32_t)0, "", "", 0, "", "") < 0 ||
        sd_bus_message_append(request, "t", (uint64_t)0) < 0 ||
        sd_bus_message_open_container(request, 'a', "(sv)") < 0 || sd_bus_message_close_container(request) < 0) goto done;
    // No numeric-PID fallback and no retry of this side effect.
    if (sd_bus_call(bus, request, 8000000, &error, &reply) < 0) goto done;
    const char *id, *path, *runtime, *seat;
    uint32_t uid, vtnr;
    int fd, existing;
    if (sd_bus_message_read(reply, "soshusub", &id, &path, &runtime, &fd, &uid, &seat, &vtnr, &existing) < 0 || existing || uid != reference->uid || !session_id_valid(id)) goto done;
    (void)path; (void)seat; (void)vtnr;
    char expected[64];
    if (snprintf(expected, sizeof expected, "/run/user/%u", uid) >= (int)sizeof expected || strcmp(runtime, expected) != 0) goto done;
    reference->fd = fcntl(fd, F_DUPFD_CLOEXEC, 4);
    if (reference->fd < 0) goto done;
    memcpy(reference->id, id, strlen(id) + 1);
    memcpy(reference->runtime, runtime, strlen(runtime) + 1);
    result = 0;
done:
    if (pidfd >= 0) close(pidfd);
    sd_bus_error_free(&error);
    sd_bus_message_unref(reply); sd_bus_message_unref(request); sd_bus_unref(bus);
    return result;
}

static int apply_environment(pam_handle_t *pamh, const struct session_reference *reference) {
    char runtime[96], id[96];
    if (snprintf(runtime, sizeof runtime, "XDG_RUNTIME_DIR=%s", reference->runtime) >= (int)sizeof runtime ||
        snprintf(id, sizeof id, "XDG_SESSION_ID=%s", reference->id) >= (int)sizeof id) return PAM_SESSION_ERR;
    if (pam_putenv(pamh, runtime) != PAM_SUCCESS || pam_putenv(pamh, id) != PAM_SUCCESS ||
        pam_putenv(pamh, "XDG_SESSION_TYPE=x11") != PAM_SUCCESS || pam_putenv(pamh, "XDG_SESSION_CLASS=user") != PAM_SUCCESS ||
        pam_putenv(pamh, "XDG_SESSION_DESKTOP=XFCE") != PAM_SUCCESS) return PAM_SESSION_ERR;
    return PAM_SUCCESS;
}

EXPORT int pam_sm_authenticate(pam_handle_t *pamh, int flags, int argc, const char **argv) {
    (void)flags; (void)argv;
    const char *username;
    int result = user(pamh, argc, &username);
    if (result != PAM_SUCCESS) return result;
    const void *existing = NULL;
    if (pam_get_data(pamh, DATA_KEY, &existing) == PAM_SUCCESS) return PAM_AUTH_ERR;
    struct session_reference *reference = calloc(1, sizeof *reference);
    if (!reference) return PAM_BUF_ERR;
    reference->fd = -1;
    memcpy(reference->username, username, strlen(username) + 1);
    struct exchange exchange = {.socket = -1, .pidfd = -1, .child = -1};
    if (exchange_start(username, "auth", &exchange, &reference->uid) != 0 || create_session(reference) != 0 || exchange_commit(&exchange, reference->id) != 0) {
        exchange_close(&exchange); reference_cleanup(pamh, reference, 0); return PAM_AUTH_ERR;
    }
    exchange_close(&exchange);
    if (pam_set_data(pamh, DATA_KEY, reference, reference_cleanup) != PAM_SUCCESS) {
        reference_cleanup(pamh, reference, 0); return PAM_SYSTEM_ERR;
    }
    return apply_environment(pamh, reference);
}

EXPORT int pam_sm_open_session(pam_handle_t *pamh, int flags, int argc, const char **argv) {
    (void)flags; (void)argv;
    const char *username;
    int result = user(pamh, argc, &username);
    if (result != PAM_SUCCESS) return result;
    const void *data = NULL;
    if (pam_get_data(pamh, DATA_KEY, &data) != PAM_SUCCESS || !data) return PAM_SESSION_ERR;
    const struct session_reference *reference = data;
    if (reference->fd < 0 || strcmp(reference->username, username) != 0) return PAM_SESSION_ERR;
    struct exchange exchange = {.socket = -1, .pidfd = -1, .child = -1};
    uint32_t uid;
    result = exchange_start(username, "open_session", &exchange, &uid) == 0 && uid == reference->uid && exchange_commit(&exchange, reference->id) == 0;
    exchange_close(&exchange);
    return result ? apply_environment(pamh, reference) : PAM_SESSION_ERR;
}

EXPORT int pam_sm_close_session(pam_handle_t *pamh, int flags, int argc, const char **argv) {
    (void)flags; (void)argv;
    const char *username;
    int result = user(pamh, argc, &username);
    if (result != PAM_SUCCESS) return result;
    const void *data = NULL;
    if (pam_get_data(pamh, DATA_KEY, &data) != PAM_SUCCESS || !data) return PAM_SESSION_ERR;
    struct session_reference *reference = (struct session_reference *)data;
    if (strcmp(reference->username, username) != 0) return PAM_SESSION_ERR;
    if (reference->fd >= 0) { close(reference->fd); reference->fd = -1; }
    return PAM_SUCCESS;
}

EXPORT int pam_sm_setcred(pam_handle_t *pamh, int flags, int argc, const char **argv) {
    (void)pamh; (void)flags; (void)argc; (void)argv;
    return PAM_IGNORE;
}
