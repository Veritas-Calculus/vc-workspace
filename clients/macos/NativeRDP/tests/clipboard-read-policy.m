/* Calls the real FreeRDP timer implementation with a private fake pasteboard.
 * No windows, system pasteboard, input injection, network or OS login. */
#import <AppKit/AppKit.h>
#import <objc/runtime.h>
#import "MRDPView.h"
#import "../VCWClipboardDelivery.h"
#include <assert.h>
#include <dlfcn.h>
#include <stdio.h>
#include <string.h>

@interface VCWCountingPasteboard : NSObject
@property(nonatomic) NSUInteger reads;
@property(nonatomic) NSInteger revision;
@property(nonatomic, copy) NSArray *items;
@end
@implementation VCWCountingPasteboard
- (NSInteger)changeCount { self.reads++; return self.revision; }
- (NSArray *)pasteboardItems { self.reads++; return self.items; }
- (void)dealloc { [_items release]; [super dealloc]; }
@end

static unsigned advertisements;
static UINT32 advertisedFormats;
static UINT advertisementResult = CHANNEL_RC_OK;
static BOOL acknowledgeImmediately = YES;
static UINT advertise(CliprdrClientContext *channel, const CLIPRDR_FORMAT_LIST *formats)
{
	assert(channel->custom);
	advertisements++;
	advertisedFormats = formats->numFormats;
	if (advertisementResult == CHANNEL_RC_OK && acknowledgeImmediately)
		[(VCWClipboardDelivery *)((mfContext *)channel->custom)->clipboardDelivery acknowledgeAdvertisement:YES];
	return advertisementResult;
}

static void set_pointer(id object, const char *name, void *value)
{
	Ivar ivar = class_getInstanceVariable(NSClassFromString(@"MRDPView"), name);
	assert(ivar);
	memcpy((char *)(void *)object + ivar_getOffset(ivar), &value, sizeof(value));
}

static id get_pointer(id object, const char *name)
{
	Ivar ivar = class_getInstanceVariable(NSClassFromString(@"MRDPView"), name);
	assert(ivar);
	id value = nil;
	memcpy(&value, (char *)(void *)object + ivar_getOffset(ivar), sizeof(value));
	return value;
}

int main(int argc, const char **argv)
{
	@autoreleasepool
	{
		assert(argc == 2 && dlopen(argv[1], RTLD_NOW | RTLD_LOCAL));
		Class viewClass = NSClassFromString(@"MRDPView");
		assert(viewClass);
		MRDPView *view = [[viewClass alloc] initWithFrame:NSZeroRect];
		VCWClipboardDelivery *delivery = [[NSClassFromString(@"VCWClipboardDelivery") alloc] initWithEnabled:YES];
		assert(delivery);
		NSTimer *timer = [NSTimer timerWithTimeInterval:0.5 target:view
		    selector:@selector(onPasteboardTimerFired:) userInfo:delivery repeats:YES];
		VCWCountingPasteboard *board = [VCWCountingPasteboard new];
		board.revision = 1;
		board.items = @[];
		set_pointer(view, "pasteboard_rd", board);
		mfContext mac = {0};
		CliprdrClientContext channel = {0};
		rdpContext context = {0};
		context.settings = freerdp_settings_new(0);
		assert(context.settings);
		set_pointer(view, "mfc", &mac);
		set_pointer(view, "context", &context);
		mac.clipboard = ClipboardCreate();
		assert(mac.clipboard);
		mac.cliprdr = &channel;
		mac.clipboardDelivery = delivery;
		channel.custom = &mac;
		channel.ClientFormatList = advertise;
		UINT32 oldFormat = ClipboardRegisterFormat(mac.clipboard, "text/plain");
		assert(ClipboardSetData(mac.clipboard, oldFormat, "old", 4));
		mac.clipboardSync = TRUE;
		view.is_connected = YES;

		assert(freerdp_settings_set_bool(context.settings, FreeRDP_RedirectClipboard, FALSE));
		[view onPasteboardTimerFired:timer];
		assert(board.reads == 0); /* Policy beats a seemingly ready channel. */
		assert(freerdp_settings_set_bool(context.settings, FreeRDP_RedirectClipboard, TRUE));
		view.is_connected = NO;
		[view onPasteboardTimerFired:timer];
		assert(board.reads == 0);
		view.is_connected = YES;
		mac.clipboardSync = FALSE;
		[view onPasteboardTimerFired:timer];
		assert(board.reads == 0);
		mac.clipboardSync = TRUE;
		mac.cliprdr = NULL;
		[view onPasteboardTimerFired:timer];
		assert(board.reads == 0);
		mac.cliprdr = &channel;
		void *clipboard = mac.clipboard;
		mac.clipboard = NULL;
		[view onPasteboardTimerFired:timer];
		assert(board.reads == 0);
		mac.clipboard = clipboard;
		set_pointer(view, "mfc", NULL);
		[view onPasteboardTimerFired:timer];
		assert(board.reads == 0);
		set_pointer(view, "mfc", &mac);
		set_pointer(view, "context", NULL);
		[view onPasteboardTimerFired:timer];
		assert(board.reads == 0);
		set_pointer(view, "context", &context);
		rdpSettings *settings = context.settings;
		context.settings = NULL;
		[view onPasteboardTimerFired:timer];
		assert(board.reads == 0);
		context.settings = settings;
		[view onPasteboardTimerFired:timer];
		assert(board.reads == 2); /* Active allowed path really reaches the board. */
		if (advertisements != 1 || advertisedFormats != 0)
			fprintf(stderr, "empty clipboard advertisements=%u formats=%u\n", advertisements, advertisedFormats);
		assert(advertisements == 1 && advertisedFormats == 0); /* Clearing must revoke old text. */
		NSPasteboardItem *textItem = [NSPasteboardItem new];
		assert([textItem setString:@"new_中文|" forType:NSPasteboardTypeString]);
		board.items = @[textItem];
		board.revision++;
		[view onPasteboardTimerFired:timer];
		assert(advertisements == 2 && advertisedFormats > 0);
		UINT32 textSize = 0;
		char *copied = ClipboardGetData(mac.clipboard,
		    ClipboardRegisterFormat(mac.clipboard, "text/plain"), &textSize);
		const char *expected = "new_中文|";
		assert(copied && textSize == strlen(expected) + 1 && memcmp(copied, expected, textSize) == 0);
		free(copied);
		[view onPasteboardTimerFired:timer];
		assert(advertisements == 2); /* No duplicate advertisement for unchanged content. */
		NSPasteboardItem *unsupported = [NSPasteboardItem new];
		assert([unsupported setData:[NSData data] forType:@"public.data"]);
		board.items = @[unsupported];
		board.revision++;
		[view onPasteboardTimerFired:timer];
		assert(advertisements == 3 && advertisedFormats == 0);
		const unsigned char invalidBytes[] = { 0xFF };
		NSPasteboardItem *invalid = [NSPasteboardItem new];
		assert([invalid setData:[NSData dataWithBytes:invalidBytes length:1] forType:NSPasteboardTypeString]);
		board.items = @[invalid];
		board.revision++;
		[view onPasteboardTimerFired:timer];
		assert(advertisements == 4 && advertisedFormats == 0);
		board.items = @[textItem];
		board.revision++;
		advertisementResult = ERROR_INTERNAL_ERROR;
		[view onPasteboardTimerFired:timer];
		assert(advertisements == 5);
		advertisementResult = CHANNEL_RC_OK;
		[view onPasteboardTimerFired:timer];
		assert(advertisements == 6 && advertisedFormats > 0); /* Failed send does not consume change. */
		acknowledgeImmediately = NO;
		board.revision++;
		[view onPasteboardTimerFired:timer];
		assert(advertisements == 7 && !delivery.formatsAccepted);
		board.items = @[];
		board.revision++;
		NSUInteger pendingReads = board.reads;
		[view onPasteboardTimerFired:timer];
		assert(advertisements == 7 && board.reads == pendingReads);
		copied = ClipboardGetData(mac.clipboard,
		    ClipboardRegisterFormat(mac.clipboard, "text/plain"), &textSize);
		assert(copied && textSize == strlen(expected) + 1 && memcmp(copied, expected, textSize) == 0);
		free(copied); /* In-flight format data stays at the advertised generation. */
		assert([delivery acknowledgeAdvertisement:YES]);
		acknowledgeImmediately = YES;
		[view onPasteboardTimerFired:timer];
		assert(advertisements == 8 && advertisedFormats == 0);
		board.items = @[textItem];
		board.revision++;
		[delivery recordRemoteWriteVersion:board.revision];
		NSUInteger remoteReads = board.reads;
		[view onPasteboardTimerFired:timer];
		assert(advertisements == 8 && board.reads == remoteReads + 1); /* Never echo remote data automatically. */
		[delivery advanceRemoteGeneration];
		[view onPasteboardTimerFired:timer];
		assert(advertisements == 8); /* A newer remote offer does not turn old remote data local. */
		board.revision++;
		[view onPasteboardTimerFired:timer];
		assert(advertisements == 9 && advertisedFormats > 0); /* A fresh local copy still publishes. */
		[textItem release];
		[unsupported release];
		[invalid release];
		const NSUInteger finalReads = board.reads;
		[view vcwSetClipboardDelivery:delivery];
		NSTimer *firstTimer = [get_pointer(view, "pasteboard_timer") retain];
		assert(firstTimer && firstTimer.valid && firstTimer.userInfo == delivery);
		[view vcwResumeClipboard];
		assert(get_pointer(view, "pasteboard_timer") == firstTimer); /* No duplicate timer. */
		VCWClipboardDelivery *successor = [[NSClassFromString(@"VCWClipboardDelivery") alloc] initWithEnabled:YES];
		[view vcwSetClipboardDelivery:successor];
		NSTimer *nextTimer = [get_pointer(view, "pasteboard_timer") retain];
		assert(!firstTimer.valid && nextTimer.valid && nextTimer.userInfo == successor);
		[view vcwClearClipboardDelivery:delivery];
		assert(get_pointer(view, "pasteboard_timer") == nextTimer && nextTimer.valid);
		[view vcwClearClipboardDelivery:successor];
		assert(!nextTimer.valid && !get_pointer(view, "pasteboard_timer"));
		[firstTimer release];
		[nextTimer release];
		[successor invalidate];
		[successor release];
		[delivery invalidate];
		set_pointer(view, "mfc", (void *)1);
		set_pointer(view, "context", (void *)1);
		[view onPasteboardTimerFired:timer];
		assert(board.reads == finalReads); /* Closed timers cannot even dereference context. */
		[timer invalidate];
		[delivery release];

		set_pointer(view, "mfc", NULL);
		set_pointer(view, "context", NULL);
		set_pointer(view, "pasteboard_rd", NULL);
		ClipboardDestroy(mac.clipboard);
		freerdp_settings_free(context.settings);
		[board release];
		[view release];
		puts("clipboard timer: policy/lifecycle, exact text, replacement, send retry, remote echo suppression and fresh local copy passed");
	}
	return 0;
}
