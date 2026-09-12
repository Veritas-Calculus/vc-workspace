/*
 * VC Workspace native RDP bridge.
 *
 * This file intentionally exposes a small C ABI. The Swift client loads it
 * from the application bundle at runtime, so ordinary Swift builds and unit
 * tests do not require FreeRDP headers or Homebrew.
 */

#import <AppKit/AppKit.h>
#import <objc/message.h>
#import <objc/runtime.h>
#import <os/log.h>

#include <freerdp/client.h>
#include <freerdp/client/cmdline.h>
#include <freerdp/client/disp.h>
#include <freerdp/channels/disp.h>
#include <freerdp/error.h>
#include <freerdp/event.h>
#include <freerdp/freerdp.h>
#include <freerdp/gdi/gdi.h>
#include <freerdp/transport_io.h>
#include <openssl/pem.h>
#include <openssl/ssl.h>
#include <openssl/x509.h>
#include <fcntl.h>
#include <sys/socket.h>
#include <unistd.h>
#include <stdatomic.h>
#include <winpr/crt.h>
#include <winpr/synch.h>
#include <winpr/sysinfo.h>
#include <winpr/wlog.h>

#include "mfreerdp.h"
#include "VCWDisplayPresentation.h"
#include "VCWRuntimeDiagnostic.h"

static void vcw_remote_view_draw_rect(id self, SEL command, NSRect rect);

static BOOL vcw_runtime_log_message(const wLogMessage* message)
{
	/* Only call-site metadata and an exact allowlisted protocol enum. Never
	 * forward formatted text, packets, credentials, images or endpoints. */
	if (!message || !message->FunctionName)
		return TRUE;
	VCWPointerDiagnostic pointer;
	if (vcw_pointer_diagnostic(message->FunctionName, message->FormatString, message->TextString, &pointer))
		os_log_info(os_log_create("ac.plz.vc-workspace", "rdp-runtime"),
		            "pointer cache op=%{public}u index=%{public}u capacity=%{public}u",
		            pointer.operation, pointer.index, pointer.capacity);
	if (message->Level < WLOG_WARN) return TRUE;
	VCWTCPDiagnostic tcp;
	if (vcw_tcp_diagnostic(message->FunctionName, message->FormatString, message->TextString, &tcp))
		os_log_error(os_log_create("ac.plz.vc-workspace", "rdp-runtime"),
		             "tcp failure stage=%{public}u code=%{public}u", tcp.stage, tcp.code);
	const int pasteReason = vcw_paste_diagnostic(message->FunctionName, message->FormatString, message->TextString);
	if (pasteReason)
		os_log_error(os_log_create("ac.plz.vc-workspace", "rdp-runtime"),
		             "paste cancelled reason=%{public}d", pasteReason);
	const char* name = message->FunctionName;
	const size_t count = strnlen(name, 129);
	if (count == 0 || count > 128 || strspn(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") != count)
		return TRUE;
	os_log_error(os_log_create("ac.plz.vc-workspace", "rdp-runtime"),
	             "runtime diagnostic function=%{public}s line=%{public}zu level=%{public}u",
	             name, message->LineNumber, message->Level);
	const int updateType = vcw_fastpath_failure_type(name, message->FormatString, message->TextString);
	if (updateType >= 0)
		os_log_error(os_log_create("ac.plz.vc-workspace", "rdp-runtime"),
		             "fastpath failed update-type=%{public}d", updateType);
	return TRUE;
}

static void vcw_configure_runtime_logging(void)
{
	static dispatch_once_t once;
	dispatch_once(&once, ^{
		wLog* root = WLog_GetRoot();
		wLogCallbacks callbacks = { .message = vcw_runtime_log_message };
		if (root && WLog_SetLogAppenderType(root, WLOG_APPENDER_CALLBACK))
			(void)WLog_ConfigureAppender(WLog_GetLogAppender(root), "callbacks", &callbacks);
		wLog* pointer = WLog_Get("com.freerdp.cache.pointer");
		if (pointer) (void)WLog_SetLogLevel(pointer, WLOG_INFO);
	});
}

static void vcw_remote_view_mouse_down(id self, SEL command, NSEvent* event)
{
	[[self window] makeFirstResponder:self];
	struct objc_super parent = { self, class_getSuperclass(object_getClass(self)) };
	((void (*)(struct objc_super*, SEL, id))objc_msgSendSuper)(&parent, command, event);
}

static Class vcw_remote_view_class(void)
{
	static Class viewClass = Nil;
	if (viewClass)
		return viewClass;

	Class baseClass = NSClassFromString(@"MRDPView");
	if (!baseClass || ![baseClass isSubclassOfClass:NSView.class])
		return Nil;

	viewClass = objc_allocateClassPair(baseClass, "VCWEmbeddedRDPView", 0);
	if (!viewClass)
		return NSClassFromString(@"VCWEmbeddedRDPView");

	class_addMethod(viewClass, @selector(mouseDown:), (IMP)vcw_remote_view_mouse_down, "v@:@");
	class_addMethod(viewClass, @selector(drawRect:), (IMP)vcw_remote_view_draw_rect,
	                method_getTypeEncoding(class_getInstanceMethod(baseClass, @selector(drawRect:))));
	objc_registerClassPair(viewClass);
	return viewClass;
}

typedef enum
{
	VCW_RDP_CONNECTING = 0,
	VCW_RDP_CONNECTED = 1,
	VCW_RDP_CLOSED = 2,
	VCW_RDP_FAILED = 3
} VCWRDPState;

typedef struct
{
	rdpContext* context;
	NSView* view;
	DispClientContext* display;
	char** argv;
	int argc;
	BOOL started;
	BOOL stopping;
	BOOL viewResumed;
	BOOL displayReady;
	BOOL displayUpdateAcknowledged;
	BOOL displayLockInitialized;
	pConnectCallback originalPreConnect;
	pEndPaint originalEndPaint;
	atomic_bool firstFrameReady;
	atomic_int gatewaySocket;
	atomic_int gatewayFailure;
	unsigned char certificateSHA256[32];
	CRITICAL_SECTION displayLock;
	UINT32 pendingWidth;
	UINT32 pendingHeight;
	UINT32 pendingClientWidth;
	UINT32 pendingClientHeight;
	UINT32 pendingDesktopScale;
	UINT32 lastSentWidth;
	UINT32 lastSentHeight;
	UINT32 lastSentDesktopScale;
	UINT32 displayAttempts;
	UINT64 lastDisplayAttempt;
	UINT64 displayRequestedAt;
	UINT32 paintedWidth;
	UINT32 paintedHeight;
	char error[512];
} VCWRDPSession;

#define VCW_EXPORT __attribute__((visibility("default")))

extern int RdpClientEntry(RDP_CLIENT_ENTRY_POINTS* entryPoints);

static const void* VCW_SESSION_ASSOCIATION_KEY = &VCW_SESSION_ASSOCIATION_KEY;

static int vcw_display_state(VCWRDPSession* session)
{
	if (!session || !session->displayLockInitialized)
		return VCW_DISPLAY_READY;
	EnterCriticalSection(&session->displayLock);
	const int state = vcw_display_presentation(session->pendingWidth, session->pendingHeight,
	                                         session->paintedWidth, session->paintedHeight,
	                                         session->displayRequestedAt, GetTickCount64());
	LeaveCriticalSection(&session->displayLock);
	return state;
}

static void vcw_remote_view_draw_rect(id self, SEL command, NSRect rect)
{
	NSValue* value = objc_getAssociatedObject(self, VCW_SESSION_ASSOCIATION_KEY);
	/* Suppress unpainted/reallocated surfaces before SwiftUI's next state
	 * update, including native zoom and continuous AppKit window resizing. */
	if (value && vcw_display_state(value.pointerValue) == VCW_DISPLAY_ADJUSTING)
	{
		[NSColor.windowBackgroundColor set];
		NSRectFill(rect);
		return;
	}
	struct objc_super parent = { self, class_getSuperclass(object_getClass(self)) };
	((void (*)(struct objc_super*, SEL, NSRect))objc_msgSendSuper)(&parent, command, rect);
}

static VCWRDPSession* vcw_session_for_context(void* context)
{
	if (!context)
		return NULL;
	mfContext* macContext = (mfContext*)context;
	if (!macContext->view)
		return NULL;
	NSValue* value = objc_getAssociatedObject(macContext->view, VCW_SESSION_ASSOCIATION_KEY);
	return value ? value.pointerValue : NULL;
}

static BOOL vcw_first_frame_end_paint(rdpContext* context)
{
	VCWRDPSession* session = vcw_session_for_context(context);
	if (!session || !session->originalEndPaint)
		return FALSE;

	const BOOL hasPixels = context->gdi && context->gdi->primary &&
	                       context->gdi->primary->hdc->hwnd->invalid &&
	                       !context->gdi->primary->hdc->hwnd->invalid->null;
	const BOOL result = session->originalEndPaint(context);
	if (result && hasPixels)
	{
		atomic_store_explicit(&session->firstFrameReady, true, memory_order_release);
		EnterCriticalSection(&session->displayLock);
		session->paintedWidth = freerdp_settings_get_uint32(context->settings, FreeRDP_DesktopWidth);
		session->paintedHeight = freerdp_settings_get_uint32(context->settings, FreeRDP_DesktopHeight);
		LeaveCriticalSection(&session->displayLock);
	}
	return result;
}

static BOOL vcw_pre_connect(freerdp* instance)
{
	if (!instance || !instance->context)
		return FALSE;
	VCWRDPSession* session = vcw_session_for_context(instance->context);
	if (!session || !session->originalPreConnect)
		return FALSE;
	atomic_store_explicit(&session->firstFrameReady, false, memory_order_release);
	if (!session->originalPreConnect(instance))
		return FALSE;

	session->originalEndPaint = instance->context->update->EndPaint;
	instance->context->update->EndPaint = vcw_first_frame_end_paint;
	return TRUE;
}

static UINT32 vcw_device_scale(UINT32 desktopScale)
{
	if (desktopScale >= 160)
		return 180;
	if (desktopScale >= 120)
		return 140;
	return 100;
}

static int vcw_send_pending_display_update(VCWRDPSession* session)
{
	if (!session || !session->displayLockInitialized)
		return -1;

	EnterCriticalSection(&session->displayLock);
	DispClientContext* display = session->display;
	if (!display || !session->displayReady || !display->SendMonitorLayout ||
	    session->pendingWidth == 0 ||
	    session->pendingHeight == 0)
	{
		LeaveCriticalSection(&session->displayLock);
		return 0;
	}
	if (session->pendingWidth == session->lastSentWidth &&
	    session->pendingHeight == session->lastSentHeight &&
	    session->pendingDesktopScale == session->lastSentDesktopScale &&
	    session->displayUpdateAcknowledged)
	{
		LeaveCriticalSection(&session->displayLock);
		return 0;
	}
	const UINT64 now = GetTickCount64();
	if (!vcw_display_retry_due(session->displayRequestedAt, session->lastDisplayAttempt,
	                           session->displayAttempts, now))
	{
		LeaveCriticalSection(&session->displayLock);
		return 0;
	}
	session->lastDisplayAttempt = now;
	session->displayAttempts++;

	DISPLAY_CONTROL_MONITOR_LAYOUT layout = WINPR_C_ARRAY_INIT;
	layout.Flags = DISPLAY_CONTROL_MONITOR_PRIMARY;
	layout.Left = 0;
	layout.Top = 0;
	layout.Width = session->pendingWidth;
	layout.Height = session->pendingHeight;
	/* Describe the logical viewport at 96 DPI for servers that consume physical
	 * monitor geometry. Windows also consumes the explicit desktop scale below.
	 * Current xrdp releases can discard both values during dynamic Xorg updates,
	 * so Linux DPI is synchronized separately by the control plane. */
	layout.PhysicalWidth = MAX(DISPLAY_CONTROL_MIN_PHYSICAL_MONITOR_WIDTH,
	                           (session->pendingClientWidth * 254 + 480) / 960);
	layout.PhysicalHeight = MAX(DISPLAY_CONTROL_MIN_PHYSICAL_MONITOR_HEIGHT,
	                            (session->pendingClientHeight * 254 + 480) / 960);
	layout.Orientation = freerdp_settings_get_uint16(session->context->settings,
	                                                FreeRDP_DesktopOrientation);
	layout.DesktopScaleFactor = session->pendingDesktopScale;
	layout.DeviceScaleFactor = vcw_device_scale(session->pendingDesktopScale);

	const UINT status = display->SendMonitorLayout(display, 1, &layout);
	if (status == CHANNEL_RC_OK)
	{
		session->lastSentWidth = session->pendingWidth;
		session->lastSentHeight = session->pendingHeight;
		session->lastSentDesktopScale = session->pendingDesktopScale;
	}
	LeaveCriticalSection(&session->displayLock);
	return status == CHANNEL_RC_OK ? 1 : -1;
}

static UINT vcw_display_control_caps(DispClientContext* display, UINT32 maxNumMonitors,
	                                  UINT32 maxMonitorAreaFactorA,
	                                  UINT32 maxMonitorAreaFactorB)
{
	WINPR_UNUSED(maxMonitorAreaFactorA);
	WINPR_UNUSED(maxMonitorAreaFactorB);
	if (!display || !display->custom)
		return CHANNEL_RC_BAD_CHANNEL_HANDLE;

	VCWRDPSession* session = display->custom;
	if (!session->displayLockInitialized)
		return CHANNEL_RC_BAD_CHANNEL_HANDLE;
	EnterCriticalSection(&session->displayLock);
	if (session->display == display)
		session->displayReady = maxNumMonitors > 0;
	LeaveCriticalSection(&session->displayLock);
	(void)vcw_send_pending_display_update(session);
	return CHANNEL_RC_OK;
}

static void vcw_channel_connected(void* context, const ChannelConnectedEventArgs* event)
{
	if (!event || !event->name || strcmp(event->name, DISP_DVC_CHANNEL_NAME) != 0)
		return;
	VCWRDPSession* session = vcw_session_for_context(context);
	if (!session || !session->displayLockInitialized)
		return;

	EnterCriticalSection(&session->displayLock);
	session->display = (DispClientContext*)event->pInterface;
	session->displayReady = FALSE;
	session->displayUpdateAcknowledged = FALSE;
	session->displayAttempts = 0;
	/* The pinned FreeRDP Mac client does not consume Display Control caps.
	 * Wait for them here so the initial viewport is not written before the
	 * server is ready and then silently ignored. */
	if (session->display && !session->display->DisplayControlCaps && !session->display->custom)
	{
		session->display->custom = session;
		session->display->DisplayControlCaps = vcw_display_control_caps;
	}
	else if (session->display)
	{
		session->displayReady = TRUE;
	}
	session->lastDisplayAttempt = 0;
	LeaveCriticalSection(&session->displayLock);
}

static void vcw_channel_disconnected(void* context, const ChannelDisconnectedEventArgs* event)
{
	if (!event || !event->name || strcmp(event->name, DISP_DVC_CHANNEL_NAME) != 0)
		return;
	VCWRDPSession* session = vcw_session_for_context(context);
	if (!session || !session->displayLockInitialized)
		return;

	EnterCriticalSection(&session->displayLock);
	if (session->display == event->pInterface)
	{
		if (session->display && session->display->custom == session &&
		    session->display->DisplayControlCaps == vcw_display_control_caps)
		{
			session->display->custom = NULL;
			session->display->DisplayControlCaps = NULL;
		}
		session->display = NULL;
	}
	session->displayReady = FALSE;
	session->displayUpdateAcknowledged = FALSE;
	session->displayAttempts = 0;
	session->lastSentWidth = 0;
	session->lastSentHeight = 0;
	session->lastSentDesktopScale = 0;
	session->lastDisplayAttempt = 0;
	LeaveCriticalSection(&session->displayLock);
}

static void vcw_remote_desktop_resized(void* context, const ResizeWindowEventArgs* event)
{
	VCWRDPSession* session = vcw_session_for_context(context);
	if (!session || !session->displayLockInitialized)
		return;

	EnterCriticalSection(&session->displayLock);
	/* ResizeWindow acknowledges allocation, not usable pixels. */
	session->paintedWidth = 0;
	session->paintedHeight = 0;
	if (event && event->width == session->pendingWidth && event->height == session->pendingHeight)
		session->displayUpdateAcknowledged = TRUE;
	const UINT32 clientWidth = session->pendingClientWidth;
	const UINT32 clientHeight = session->pendingClientHeight;
	LeaveCriticalSection(&session->displayLock);
	if (clientWidth > 0 && clientHeight > 0)
	{
		mfContext* macContext = (mfContext*)context;
		macContext->client_width = clientWidth;
		macContext->client_height = clientHeight;
	}
}

static void vcw_set_error(VCWRDPSession* session, const char* message)
{
	if (!session)
		return;
	const char* value = message ? message : "Unknown RDP error";
	snprintf(session->error, sizeof(session->error), "%s", value);
}

static int vcw_gateway_connect(rdpContext* context, rdpSettings* settings,
                               const char* hostname, int port, DWORD timeout)
{
	(void)settings; (void)hostname; (void)port; (void)timeout;
	VCWRDPSession* session = freerdp_get_io_callback_context(context);
	/* One owned socket, never an IP dial or a fallback protocol reconnect. */
	return session ? atomic_exchange(&session->gatewaySocket, -1) : -1;
}

static void vcw_close_pending_gateway(VCWRDPSession* session)
{
	const int fd = atomic_exchange(&session->gatewaySocket, -1);
	if (fd >= 0) close(fd);
}

static int vcw_verify_gateway_guest(freerdp* instance, const BYTE* data, size_t length,
                                   const char* hostname, UINT16 port, DWORD flags)
{
	(void)hostname; (void)port;
	VCWRDPSession* session = freerdp_get_io_callback_context(instance->context);
	if (!session) return 0;
	BOOL accepted = FALSE;
	if (data && length > 0 && length <= 65536 &&
	    !(flags & (VERIFY_CERT_FLAG_GATEWAY | VERIFY_CERT_FLAG_REDIRECT)))
	{
		BIO* bio = BIO_new_mem_buf(data, (int)length);
		X509* certificate = bio ? PEM_read_bio_X509(bio, NULL, NULL, NULL) : NULL;
		unsigned char digest[32] = { 0 };
		unsigned int size = 0;
		if (certificate && X509_cmp_current_time(X509_get0_notBefore(certificate)) < 0 &&
		    X509_cmp_current_time(X509_get0_notAfter(certificate)) > 0 &&
		    X509_digest(certificate, EVP_sha256(), digest, &size) == 1 && size == sizeof(digest) &&
		    CRYPTO_memcmp(digest, session->certificateSHA256, sizeof(digest)) == 0)
			accepted = TRUE;
		X509_free(certificate);
		BIO_free(bio);
	}
	if (!accepted) atomic_store(&session->gatewayFailure, 1);
	return accepted ? 1 : 0;
}

static BOOL vcw_reject_gateway_redirect(freerdp* instance)
{
	VCWRDPSession* session = freerdp_get_io_callback_context(instance->context);
	if (session) atomic_store(&session->gatewayFailure, 2);
	return FALSE;
}

static BOOL vcw_gateway_authenticate(freerdp* instance, char** username, char** password,
                                    char** domain, rdp_auth_reason reason)
{
	(void)instance; (void)domain;
	/* Broker-owned credentials only. In particular, a rejected NLA password
	 * must not open the upstream Mac credential dialog or reuse Keychain data. */
	return (reason == AUTH_TLS || reason == AUTH_RDP) && username && *username && **username &&
	       password && *password && **password;
}

static BOOL vcw_parse_certificate_pin(const char* pin, unsigned char* output)
{
	if (!pin || strlen(pin) != 64) return FALSE;
	for (size_t i = 0; i < 32; i++)
	{
		unsigned int value = 0;
		for (size_t j = 0; j < 2; j++)
		{
			const char c = pin[2 * i + j];
			if (c >= '0' && c <= '9') value = value * 16 + (unsigned int)(c - '0');
			else if (c >= 'a' && c <= 'f') value = value * 16 + (unsigned int)(c - 'a' + 10);
			else return FALSE;
		}
		output[i] = (unsigned char)value;
	}
	return TRUE;
}

static char* vcw_argument(NSString* value)
{
	return strdup(value.UTF8String);
}

static void vcw_free_arguments(VCWRDPSession* session)
{
	if (!session || !session->argv)
		return;

	for (int index = 0; index < session->argc; index++)
	{
		if (!session->argv[index])
			continue;
		/* Clear parser inputs before releasing them. Credentials never enter
		 * this array; they are copied directly into FreeRDP settings below. */
		SecureZeroMemory(session->argv[index], strlen(session->argv[index]));
		free(session->argv[index]);
	}
	free(session->argv);
	session->argv = NULL;
	session->argc = 0;
}

static BOOL vcw_build_arguments(VCWRDPSession* session, int quality, int clipboardRedirection,
                                int driveRedirection, BOOL pinned)
{
	NSMutableArray<NSString*>* values = [NSMutableArray arrayWithObjects:
		@"vc-workspace",
		pinned ? @"/cert:deny" : @"/cert:tofu",
		/* Swift owns bounded, freshly authorized recovery. The upstream Mac
		 * loop does not implement client_auto_reconnect_ex. */
		@"-auto-reconnect",
		@"/timeout:30000",
		@"/log-level:WARN",
		@"/dynamic-resolution",
		@"/size:1440x900",
		nil];

	[values addObject:clipboardRedirection ? @"+clipboard" : @"-clipboard"];
	/* The official client does not map a local directory by default. Explicitly
	 * disable the aggregate drive switch when policy denies it; when allowed,
	 * only a future user-selected /drive mapping may opt in. */
	if (!driveRedirection)
		[values addObject:@"-drives"];

	if (quality == 1)
	{
		[values addObjectsFromArray:@[
			@"/network:modem",
			@"/compression-level:2",
			@"/cache:bitmap:on,codec:rfx,glyph:on,offscreen:on",
			@"/gfx:progressive:on,small-cache:on,thin-client:on,frame-ack:on",
			@"/frame-ack:1"
		]];
	}
	else
	{
		[values addObject:@"/network:auto"];
	}

	session->argc = (int)values.count;
	session->argv = calloc((size_t)session->argc, sizeof(char*));
	if (!session->argv)
		return FALSE;

	for (int index = 0; index < session->argc; index++)
	{
		session->argv[index] = vcw_argument(values[(NSUInteger)index]);
		if (!session->argv[index])
			return FALSE;
	}
	return TRUE;
}

VCW_EXPORT int vcw_rdp_runtime_version(void)
{
	return 5;
}

VCW_EXPORT void* vcw_rdp_session_create(const char* host, uint16_t port, const char* username,
	                                     const char* password, int quality, int clipboardRedirection,
	                                     int driveRedirection, int gatewaySocket, const char* certificatePin)
{
	vcw_configure_runtime_logging();
	if (!host || !host[0] || port == 0 || !username || !username[0] || !password || !password[0])
		return NULL;
	if ((clipboardRedirection != 0 && clipboardRedirection != 1) ||
	    (driveRedirection != 0 && driveRedirection != 1))
		return NULL;
	const BOOL pinned = gatewaySocket >= 0;
	unsigned char parsedPin[32] = { 0 };
	if (pinned)
	{
		struct sockaddr_storage address = { 0 };
		socklen_t length = sizeof(address);
		int type = 0;
		socklen_t typeLength = sizeof(type);
		if (!vcw_parse_certificate_pin(certificatePin, parsedPin) ||
		    getpeername(gatewaySocket, (struct sockaddr*)&address, &length) != 0 ||
		    address.ss_family != AF_UNIX ||
		    getsockopt(gatewaySocket, SOL_SOCKET, SO_TYPE, &type, &typeLength) != 0 || type != SOCK_STREAM)
			return NULL;
	}
	else if (certificatePin && certificatePin[0]) return NULL;

	VCWRDPSession* session = calloc(1, sizeof(VCWRDPSession));
	if (!session)
		return NULL;
	atomic_init(&session->firstFrameReady, false);
	atomic_init(&session->gatewaySocket, -1);
	atomic_init(&session->gatewayFailure, 0);
	memcpy(session->certificateSHA256, parsedPin, sizeof(parsedPin));

	if (!vcw_build_arguments(session, quality, clipboardRedirection, driveRedirection, pinned))
	{
		vcw_free_arguments(session);
		free(session);
		return NULL;
	}

	RDP_CLIENT_ENTRY_POINTS entryPoints = { 0 };
	entryPoints.Size = sizeof(RDP_CLIENT_ENTRY_POINTS);
	entryPoints.Version = RDP_CLIENT_INTERFACE_VERSION;
	if (RdpClientEntry(&entryPoints) != 0)
	{
		vcw_free_arguments(session);
		free(session);
		return NULL;
	}

	session->context = freerdp_client_context_new(&entryPoints);
	if (!session->context)
	{
		vcw_free_arguments(session);
		free(session);
		return NULL;
	}

	session->context->argc = session->argc;
	session->context->argv = session->argv;
	const int parseStatus = freerdp_client_settings_parse_command_line(
		session->context->settings, session->argc, session->argv, FALSE);
	if (parseStatus != 0)
	{
		vcw_set_error(session, "Invalid FreeRDP connection settings");
		freerdp_client_context_free(session->context);
		vcw_free_arguments(session);
		free(session);
		return NULL;
	}
	/* Dynamic resolution updates the server-side framebuffer. Smart sizing is
	 * still useful inside an embedded view: it keeps the previous frame usable
	 * while a resize is in flight and preserves pointer mapping until the server
	 * applies the new layout. The command-line parser treats the standalone
	 * modes as exclusive, so enable this transitional behavior after parsing. */
	if (!freerdp_settings_set_bool(session->context->settings, FreeRDP_SmartSizing, TRUE))
	{
		freerdp_client_context_free(session->context);
		vcw_free_arguments(session);
		free(session);
		return NULL;
	}
	if (!freerdp_settings_set_string(session->context->settings, FreeRDP_ServerHostname, host) ||
	    !freerdp_settings_set_uint32(session->context->settings, FreeRDP_ServerPort, port) ||
	    !freerdp_settings_set_string(session->context->settings, FreeRDP_Username, username) ||
	    !freerdp_settings_set_string(session->context->settings, FreeRDP_Password, password))
	{
		freerdp_client_context_free(session->context);
		vcw_free_arguments(session);
		free(session);
		return NULL;
	}

	Class viewClass = vcw_remote_view_class();
	if (!viewClass)
	{
		freerdp_client_context_free(session->context);
		vcw_free_arguments(session);
		free(session);
		return NULL;
	}
	if (pinned)
	{
		rdpTransportIo io = *freerdp_get_io_callbacks(session->context);
		io.TCPConnect = vcw_gateway_connect;
		rdpSettings* settings = session->context->settings;
		BOOL configured = freerdp_set_io_callback_context(session->context, session) &&
		    freerdp_set_io_callbacks(session->context, &io) &&
		    freerdp_settings_set_string(settings, FreeRDP_CertificateAcceptedFingerprints, NULL) &&
		    freerdp_settings_set_bool(settings, FreeRDP_ExternalCertificateManagement, TRUE) &&
		    freerdp_settings_set_bool(settings, FreeRDP_IgnoreCertificate, FALSE) &&
		    freerdp_settings_set_bool(settings, FreeRDP_AutoAcceptCertificate, FALSE) &&
		    freerdp_settings_set_bool(settings, FreeRDP_RdpSecurity, FALSE) &&
		    freerdp_settings_set_bool(settings, FreeRDP_RdstlsSecurity, FALSE) &&
		    freerdp_settings_set_bool(settings, FreeRDP_ExtSecurity, FALSE) &&
		    freerdp_settings_set_bool(settings, FreeRDP_GatewayEnabled, FALSE) &&
		    freerdp_settings_set_bool(settings, FreeRDP_AutoReconnectionEnabled, FALSE) &&
		    freerdp_settings_set_uint16(settings, FreeRDP_TLSMinVersion, TLS1_2_VERSION);
		const int owned = configured ? fcntl(gatewaySocket, F_DUPFD_CLOEXEC, 0) : -1;
		if (owned < 0)
		{
			freerdp_client_context_free(session->context);
			vcw_free_arguments(session);
			free(session);
			return NULL;
		}
		atomic_store(&session->gatewaySocket, owned);
		session->context->instance->VerifyX509Certificate = vcw_verify_gateway_guest;
		session->context->instance->Redirect = vcw_reject_gateway_redirect;
		session->context->instance->AuthenticateEx = vcw_gateway_authenticate;
	}

	session->view = [[viewClass alloc] initWithFrame:NSMakeRect(0, 0, 1440, 900)];
	session->view.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
	session->view.wantsLayer = YES;
	session->view.layer.backgroundColor = NSColor.blackColor.CGColor;
	InitializeCriticalSection(&session->displayLock);
	session->displayLockInitialized = TRUE;
	objc_setAssociatedObject(session->view, VCW_SESSION_ASSOCIATION_KEY,
	                         [NSValue valueWithPointer:session], OBJC_ASSOCIATION_RETAIN_NONATOMIC);
	mfContext* macContext = (mfContext*)session->context;
	macContext->view = session->view;
	macContext->view_ownership = FALSE;
	session->originalPreConnect = session->context->instance->PreConnect;
	session->context->instance->PreConnect = vcw_pre_connect;
	PubSub_SubscribeChannelConnected(session->context->pubSub, vcw_channel_connected);
	PubSub_SubscribeChannelDisconnected(session->context->pubSub, vcw_channel_disconnected);
	PubSub_SubscribeResizeWindow(session->context->pubSub, vcw_remote_desktop_resized);

	const int startStatus = freerdp_client_start(session->context);
	if (startStatus != 0)
	{
		vcw_set_error(session, "FreeRDP could not start its connection thread");
		objc_setAssociatedObject(session->view, VCW_SESSION_ASSOCIATION_KEY, nil,
		                         OBJC_ASSOCIATION_ASSIGN);
		[session->view release];
		session->view = nil;
		macContext->view = nil;
		freerdp_client_context_free(session->context);
		DeleteCriticalSection(&session->displayLock);
		session->displayLockInitialized = FALSE;
		vcw_free_arguments(session);
		vcw_close_pending_gateway(session);
		free(session);
		return NULL;
	}

	session->started = TRUE;
	return session;
}

VCW_EXPORT void* vcw_rdp_session_view(void* handle)
{
	VCWRDPSession* session = handle;
	return session ? session->view : NULL;
}

VCW_EXPORT int vcw_rdp_session_set_viewport(void* handle, UINT32 width, UINT32 height,
	                                         UINT32 desktopScale)
{
	VCWRDPSession* session = handle;
	if (!session || !session->context || !session->displayLockInitialized || width < 1 ||
	    height < 1 || desktopScale < 100 || desktopScale > 500)
		return -1;

	width = MIN(MAX(width, DISPLAY_CONTROL_MIN_MONITOR_WIDTH), DISPLAY_CONTROL_MAX_MONITOR_WIDTH);
	height = MIN(MAX(height, DISPLAY_CONTROL_MIN_MONITOR_HEIGHT), DISPLAY_CONTROL_MAX_MONITOR_HEIGHT);
	width -= width % 2;

	EnterCriticalSection(&session->displayLock);
	const BOOL changed = width != session->pendingWidth || height != session->pendingHeight ||
	                     desktopScale != session->pendingDesktopScale;
	session->pendingWidth = width;
	session->pendingHeight = height;
	session->pendingClientWidth = MAX(1, (width * 100 + desktopScale / 2) / desktopScale);
	session->pendingClientHeight = MAX(1, (height * 100 + desktopScale / 2) / desktopScale);
	session->pendingDesktopScale = desktopScale;
	if (changed || vcw_display_presentation(session->pendingWidth, session->pendingHeight,
	                                      session->paintedWidth, session->paintedHeight,
	                                      session->displayRequestedAt, GetTickCount64()) == VCW_DISPLAY_SCALED)
	{
		session->displayRequestedAt = GetTickCount64();
		session->paintedWidth = 0;
		session->paintedHeight = 0;
		session->lastDisplayAttempt = 0;
		session->displayAttempts = 0;
		session->displayUpdateAcknowledged = FALSE;
	}
	const UINT32 clientWidth = session->pendingClientWidth;
	const UINT32 clientHeight = session->pendingClientHeight;
	LeaveCriticalSection(&session->displayLock);

	/* AppKit reports pointer locations in points. Keep FreeRDP's input scaler
	 * on the logical viewport while the old framebuffer is being resized. */
	mfContext* macContext = (mfContext*)session->context;
	macContext->client_width = clientWidth;
	macContext->client_height = clientHeight;
	return vcw_send_pending_display_update(session);
}

VCW_EXPORT int vcw_rdp_session_display_state(void* handle)
{
	return vcw_display_state(handle);
}

VCW_EXPORT int vcw_rdp_session_state(void* handle)
{
	VCWRDPSession* session = handle;
	if (!session || !session->started || !session->context || !session->view)
		return VCW_RDP_FAILED;

	if ([[session->view valueForKey:@"is_connected"] boolValue])
	{
		/* MRDPView draws the remote surface into its current bounds, but its
		 * standalone client normally keeps client_width/client_height at the
		 * desktop size. An embedded window can be much smaller, so keep the
		 * mouse coordinate scaler in sync with the actual view in points. */
		const NSSize clientSize = session->view.bounds.size;
		if (clientSize.width >= 1 && clientSize.height >= 1)
		{
			mfContext* macContext = (mfContext*)session->context;
			macContext->client_width = (UINT32)clientSize.width;
			macContext->client_height = (UINT32)clientSize.height;
		}

		/* mac_post_connect asks MRDPView to resume before the FreeRDP Mac
		 * client marks it connected. The standalone client retries when its
		 * window activates, but an embedded view has no such lifecycle event.
		 * Resume once here so clipboard polling and focus tracking are active. */
		if (!session->viewResumed && [session->view respondsToSelector:@selector(resume)])
		{
			[session->view performSelector:@selector(resume)];
			session->viewResumed = TRUE;
		}
		(void)vcw_send_pending_display_update(session);
		return atomic_load_explicit(&session->firstFrameReady, memory_order_acquire)
		           ? VCW_RDP_CONNECTED
		           : VCW_RDP_CONNECTING;
	}
	/* A successful automatic reconnect runs the Mac post-connect lifecycle
	 * again. Let the next connected poll restore clipboard/focus tracking. */
	session->viewResumed = FALSE;

	HANDLE thread = freerdp_client_get_thread(session->context);
	if (thread && WaitForSingleObject(thread, 0) == WAIT_OBJECT_0)
	{
		if (session->stopping)
			return VCW_RDP_CLOSED;
		const int gatewayFailure = atomic_load(&session->gatewayFailure);
		if (gatewayFailure != 0)
		{
			vcw_set_error(session, gatewayFailure == 1 ? "VCW_GATEWAY_CERTIFICATE_REJECTED" : "VCW_GATEWAY_REDIRECT_REJECTED");
			return VCW_RDP_FAILED;
		}

		const UINT32 code = freerdp_get_last_error(session->context);
		/* Codes only: FreeRDP text and connection settings can contain user data. */
		os_log_error(os_log_create("ac.plz.vc-workspace", "rdp-transport"),
		             "transport ended error=0x%{public}08x firstFrame=%{public}d", code,
		             atomic_load_explicit(&session->firstFrameReady, memory_order_acquire));
		if (code == FREERDP_ERROR_SUCCESS || code == FREERDP_ERROR_CONNECT_CANCELLED)
			return VCW_RDP_CLOSED;

		vcw_set_error(session, freerdp_get_last_error_name(code));
		return VCW_RDP_FAILED;
	}

	return session->stopping ? VCW_RDP_CLOSED : VCW_RDP_CONNECTING;
}

VCW_EXPORT const char* vcw_rdp_session_error(void* handle)
{
	VCWRDPSession* session = handle;
	if (!session)
		return "The native RDP session is unavailable";
	return session->error;
}

VCW_EXPORT void vcw_rdp_session_stop(void* handle)
{
	VCWRDPSession* session = handle;
	if (!session || !session->context || session->stopping)
		return;

	session->stopping = TRUE;
	vcw_close_pending_gateway(session);
	mfContext* macContext = (mfContext*)session->context;
	if (macContext->stopEvent)
		SetEvent(macContext->stopEvent);
	freerdp_abort_connect_context(session->context);
}

VCW_EXPORT void vcw_rdp_session_destroy(void* handle)
{
	VCWRDPSession* session = handle;
	if (!session)
		return;

	if (session->context)
	{
		vcw_rdp_session_stop(session);
		freerdp_client_stop(session->context);
		if (session->view)
		{
			objc_setAssociatedObject(session->view, VCW_SESSION_ASSOCIATION_KEY, nil,
			                         OBJC_ASSOCIATION_ASSIGN);
		}
		mfContext* macContext = (mfContext*)session->context;
		macContext->view = nil;
		freerdp_client_context_free(session->context);
		session->context = NULL;
	}
	if (session->view)
	{
		if ([session->view respondsToSelector:@selector(releaseResources)])
			[session->view performSelector:@selector(releaseResources)];
		[session->view removeFromSuperview];
		[session->view release];
		session->view = nil;
	}
	if (session->displayLockInitialized)
	{
		DeleteCriticalSection(&session->displayLock);
		session->displayLockInitialized = FALSE;
	}
	vcw_free_arguments(session);
	free(session);
}
