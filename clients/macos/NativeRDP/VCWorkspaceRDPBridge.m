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

#include <freerdp/client.h>
#include <freerdp/client/cmdline.h>
#include <freerdp/error.h>
#include <freerdp/freerdp.h>
#include <winpr/crt.h>
#include <winpr/synch.h>

#include "mfreerdp.h"

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
	char** argv;
	int argc;
	BOOL started;
	BOOL stopping;
	BOOL viewResumed;
	char error[512];
} VCWRDPSession;

#define VCW_EXPORT __attribute__((visibility("default")))

extern int RdpClientEntry(RDP_CLIENT_ENTRY_POINTS* entryPoints);

static void vcw_set_error(VCWRDPSession* session, const char* message)
{
	if (!session)
		return;
	const char* value = message ? message : "Unknown RDP error";
	snprintf(session->error, sizeof(session->error), "%s", value);
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

static BOOL vcw_build_arguments(VCWRDPSession* session, int quality)
{
	NSMutableArray<NSString*>* values = [NSMutableArray arrayWithObjects:
		@"vc-workspace",
		@"/cert:tofu",
		@"+auto-reconnect",
		@"/auto-reconnect-max-retries:20",
		@"/timeout:30000",
		@"/log-level:WARN",
		@"+clipboard",
		@"+smart-sizing",
		@"/size:1440x900",
		nil];

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
	return 1;
}

VCW_EXPORT void* vcw_rdp_session_create(const char* host, uint16_t port, const char* username,
	                                     const char* password, int quality)
{
	if (!host || !host[0] || port == 0 || !username || !username[0] || !password || !password[0])
		return NULL;

	VCWRDPSession* session = calloc(1, sizeof(VCWRDPSession));
	if (!session)
		return NULL;

	if (!vcw_build_arguments(session, quality))
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

	session->view = [[viewClass alloc] initWithFrame:NSMakeRect(0, 0, 1440, 900)];
	session->view.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
	mfContext* macContext = (mfContext*)session->context;
	macContext->view = session->view;
	macContext->view_ownership = FALSE;

	const int startStatus = freerdp_client_start(session->context);
	if (startStatus != 0)
	{
		vcw_set_error(session, "FreeRDP could not start its connection thread");
		[session->view release];
		session->view = nil;
		macContext->view = nil;
		freerdp_client_context_free(session->context);
		vcw_free_arguments(session);
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
		return VCW_RDP_CONNECTED;
	}
	/* A successful automatic reconnect runs the Mac post-connect lifecycle
	 * again. Let the next connected poll restore clipboard/focus tracking. */
	session->viewResumed = FALSE;

	HANDLE thread = freerdp_client_get_thread(session->context);
	if (thread && WaitForSingleObject(thread, 0) == WAIT_OBJECT_0)
	{
		if (session->stopping)
			return VCW_RDP_CLOSED;

		const UINT32 code = freerdp_get_last_error(session->context);
		if (code == FREERDP_ERROR_SUCCESS || code == FREERDP_ERROR_CONNECT_CANCELLED)
			return VCW_RDP_CLOSED;

		vcw_set_error(session, freerdp_get_last_error_string(code));
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
	vcw_free_arguments(session);
	free(session);
}
