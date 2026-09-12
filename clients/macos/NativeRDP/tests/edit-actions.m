/* Real MRDPView responder methods and FreeRDP input encoder, with a private
 * in-memory active context and recording callback. No window or OS input. */
#import <AppKit/AppKit.h>
#import <objc/runtime.h>
#import "MRDPView.h"
#import "../VCWClipboardDelivery.h"
#include "../VCWTextInput.h"
#include <assert.h>
#include <dlfcn.h>
#include <stdio.h>

extern void vcw_test_set_active(rdpContext *context, BOOL active);

@interface MRDPView (VCWEditTest)
- (BOOL)vcwCanPerformEdit:(SEL)action;
- (BOOL)validateUserInterfaceItem:(id<NSValidatedUserInterfaceItem>)item;
- (void)copy:(id)sender;
- (void)cut:(id)sender;
- (void)selectAll:(id)sender;
- (void)paste:(id)sender;
@end

@interface VCWEditPasteboard : NSObject
@property(nonatomic) NSInteger revision;
@property(nonatomic) unsigned reads;
@property(nonatomic, copy) NSArray *items;
@end
@implementation VCWEditPasteboard
- (NSInteger)changeCount { self.reads++; return self.revision; }
- (NSArray *)pasteboardItems { self.reads++; return self.items; }
- (void)dealloc { [_items release]; [super dealloc]; }
@end

static BOOL focused = YES;
static unsigned rejected;
static unsigned advertisements;
static BOOL has_focus(id self, SEL cmd) { (void)self; (void)cmd; return focused; }
static void reject_paste(id self, SEL cmd) { (void)self; (void)cmd; rejected++; }
static UINT advertise(CliprdrClientContext *channel, const CLIPRDR_FORMAT_LIST *list)
{
	(void)channel; (void)list; advertisements++; return CHANNEL_RC_OK;
}

static UINT16 flagsSeen[16];
static UINT16 keysSeen[16];
static unsigned count;
static int failAt = -1;
static BOOL record_key(rdpInput *input, UINT16 flags, UINT8 key)
{
	(void)input;
	assert(count < 16);
	flagsSeen[count] = flags;
	keysSeen[count] = key;
	return (int)count++ != failAt;
}
static BOOL record_unicode(rdpInput *input, UINT16 flags, UINT16 code)
{
	(void)input; assert(count < 16);
	flagsSeen[count] = flags; keysSeen[count] = code;
	return (int)count++ != failAt;
}
static bool unicode_current(void *context) { return context && focused; }
static bool unicode_emit(void *context, uint16_t code, bool release)
{
	return freerdp_input_send_unicode_keyboard_event(context, release ? KBD_FLAGS_RELEASE : 0, code);
}
static void set_field(id object, const char *name, const void *value, size_t size)
{
	Ivar field = class_getInstanceVariable(NSClassFromString(@"MRDPView"), name);
	assert(field);
	memcpy((char *)(void *)object + ivar_getOffset(field), value, size);
}
static void invoke(MRDPView *view, SEL action)
{
	((void (*)(id, SEL, id))[view methodForSelector:action])(view, action, nil);
}
static NSEvent *edit_key(NSEventType type, NSString *text, unsigned short code, NSEventModifierFlags flags)
{
	return [NSEvent keyEventWithType:type location:NSZeroPoint modifierFlags:flags timestamp:0
	    windowNumber:0 context:nil characters:text charactersIgnoringModifiers:text isARepeat:NO keyCode:code];
}
int main(int argc, const char **argv)
{
	@autoreleasepool
	{
		assert(argc == 2 && dlopen(argv[1], RTLD_NOW | RTLD_LOCAL));
		Class type = NSClassFromString(@"MRDPView");
		if (![type instancesRespondToSelector:@selector(vcwCanPerformEdit:)]) {
			fputs("FAIL: native edit responder is missing\n", stderr);
			return 1;
		}
		if (![type instancesRespondToSelector:@selector(vcwAdvancePaste)]) {
			fputs("FAIL: confirmed native paste responder is missing\n", stderr);
			return 1;
		}
		Class fixture = objc_allocateClassPair(type, "VCWEditFixtureView", 0);
		assert(fixture);
		class_addMethod(fixture, @selector(vcwHasPasteFocus), (IMP)has_focus, "c@:");
		class_addMethod(fixture, @selector(vcwPasteRejected), (IMP)reject_paste, "v@:");
		objc_registerClassPair(fixture);
		MRDPView *view = [[fixture alloc] initWithFrame:NSZeroRect];
		freerdp *instance = freerdp_new();
		assert(instance && freerdp_context_new(instance));
		vcw_test_set_active(instance->context, TRUE);
		instance->context->input->KeyboardEvent = record_key;
		instance->context->input->UnicodeKeyboardEvent = record_unicode;
		const uint16_t committed[] = {0x4e2d, 0x6587, 0xd83d, 0xde00, '|', '\\'};
		assert(vcw_send_committed_text(committed,6,instance->context->input,unicode_current,unicode_emit)==VCWTextSent);
		assert(count==12);
		for(unsigned i=0;i<12;i++) {
			assert(keysSeen[i]==committed[i/2]);
			assert(flagsSeen[i]==(i%2 ? KBD_FLAGS_RELEASE : 0));
		}
		count=0; failAt=3;
		assert(vcw_send_committed_text(committed,6,instance->context->input,unicode_current,unicode_emit)==VCWTextFailed);
		assert(count==4); count=0; failAt=-1; focused=NO;
		assert(vcw_send_committed_text(committed,6,instance->context->input,unicode_current,unicode_emit)==VCWTextUnavailable);
		assert(count==0); focused=YES;
		puts("committed text: real FreeRDP Unicode callback, focus rejection and send failure passed");
		mfContext mac = {0};
		CliprdrClientContext channel = {0};
		mac.clipboardSync = TRUE;
		mac.cliprdr = &channel;
		mfContext *macPointer = &mac;
		set_field(view, "mfc", &macPointer, sizeof(macPointer));
		set_field(view, "instance", &instance, sizeof(instance));
		view.is_connected = YES;
		assert(freerdp_settings_set_bool(instance->context->settings, FreeRDP_RedirectClipboard, TRUE));
		SEL actions[] = { @selector(copy:), @selector(cut:), @selector(selectAll:) };
		UINT16 keys[] = { 0x2e, 0x2d, 0x1e };
		for (size_t i = 0; i < 3; i++) {
			assert([view vcwCanPerformEdit:actions[i]]);
			NSMenuItem *item = [[[NSMenuItem alloc] initWithTitle:@"Edit" action:actions[i] keyEquivalent:@""] autorelease];
			assert([view validateUserInterfaceItem:item]);
			count = 0;
			invoke(view, actions[i]);
			assert(count == 4);
			assert(keysSeen[0] == 0x1d && flagsSeen[0] == 0);
			assert(keysSeen[1] == keys[i] && flagsSeen[1] == 0);
			assert(keysSeen[2] == keys[i] && flagsSeen[2] == KBD_FLAGS_RELEASE);
			assert(keysSeen[3] == 0x1d && flagsSeen[3] == KBD_FLAGS_RELEASE);
		}
		DWORD modifiers = NSEventModifierFlagCommand | NSEventModifierFlagShift | NSEventModifierFlagCapsLock;
		set_field(view, "kbdModFlags", &modifiers, sizeof(modifiers));
		count = 0;
		[view copy:nil];
		assert(count == 6); /* Release Shift/Win, not Caps Lock, before Ctrl+C. */
		assert(keysSeen[0] == 0x2a && flagsSeen[0] == KBD_FLAGS_RELEASE);
		assert(keysSeen[1] == 0x5b && flagsSeen[1] == (KBD_FLAGS_RELEASE | KBD_FLAGS_EXTENDED));
		for (int failed = 0; failed < 4; failed++) {
			count = 0; failAt = failed;
			[view copy:nil];
			assert(count == (failed == 0 ? 2 : 4));
			assert(keysSeen[count - 1] == 0x1d && flagsSeen[count - 1] == KBD_FLAGS_RELEASE);
		}
		failAt = -1;
		modifiers = NSEventModifierFlagCommand | NSEventModifierFlagShift;
		set_field(view, "kbdModFlags", &modifiers, sizeof(modifiers));
		count = 0; failAt = 0;
		[view copy:nil];
		assert(count == 2); /* Do not issue Ctrl+C after a failed modifier release. */
		modifiers = 0;
		set_field(view, "kbdModFlags", &modifiers, sizeof(modifiers));
		failAt = -1;
		assert(freerdp_settings_set_bool(instance->context->settings, FreeRDP_RedirectClipboard, FALSE));
		count = 0;
		[view copy:nil]; [view cut:nil];
		assert(count == 0 && ![view vcwCanPerformEdit:@selector(copy:)]);
		NSMenuItem *copyItem = [[[NSMenuItem alloc] initWithTitle:@"Copy" action:@selector(copy:) keyEquivalent:@"c"] autorelease];
		assert(![view validateUserInterfaceItem:copyItem]);
		NSString *blockedLetters[] = { @"c", @"x", @"v" };
		unsigned short blockedCodes[] = { 8, 7, 9 };
		for (unsigned i = 0; i < 3; i++) {
			count = 0;
			[view keyDown:edit_key(NSEventTypeKeyDown, blockedLetters[i], blockedCodes[i], NSEventModifierFlagCommand)];
			assert(count == 0); /* Disabled menu actions must not fall through as literal letters. */
			[view keyUp:edit_key(NSEventTypeKeyUp, blockedLetters[i], blockedCodes[i], 0)];
			assert(count == 0); /* Even if Command was released before the letter. */
		}
		[view keyDown:edit_key(NSEventTypeKeyDown, @"v", 9, 0)];
		[view keyUp:edit_key(NSEventTypeKeyUp, @"v", 9, 0)];
		assert(count == 2 && keysSeen[0] == 0x2f && keysSeen[1] == 0x2f);
		count = 0;
		[view keyDown:edit_key(NSEventTypeKeyDown, @"v", 9, NSEventModifierFlagControl)];
		[view keyUp:edit_key(NSEventTypeKeyUp, @"v", 9, 0)];
		assert(count == 4 && keysSeen[0] == 0x1d && keysSeen[1] == 0x2f && keysSeen[3] == 0x2f);
		modifiers = NSEventModifierFlagCommand;
		set_field(view, "kbdModFlags", &modifiers, sizeof(modifiers));
		count = 0;
		[view keyDown:edit_key(NSEventTypeKeyDown, @"v", 9, NSEventModifierFlagCommand)];
		[view keyUp:edit_key(NSEventTypeKeyUp, @"v", 9, 0)];
		assert(count == 1 && keysSeen[0] == 0x5b && (flagsSeen[0] & KBD_FLAGS_RELEASE));
		/* Exercise real MRDPView physical-key translation, independently of
		 * accessibility text insertion and clipboard synthesis. */
		NSString *symbols[] = { @"\\", @"|", @"_", @"+", @"{", @"}", @":", @"\"", @"<", @">", @"?", @"~" };
		unsigned short appleCodes[] = {42, 42, 27, 24, 33, 30, 41, 39, 43, 47, 44, 50};
		UINT16 scanCodes[] = {0x2b, 0x2b, 0x0c, 0x0d, 0x1a, 0x1b, 0x27, 0x28, 0x33, 0x34, 0x35, 0x29};
		for (unsigned i = 0; i < sizeof(appleCodes)/sizeof(appleCodes[0]); i++) {
			for (unsigned releaseFirst = 0; releaseFirst < 2; releaseFirst++) {
				modifiers = 0;
				set_field(view, "kbdModFlags", &modifiers, sizeof(modifiers));
				count = 0;
				NSEventModifierFlags shift = i ? NSEventModifierFlagShift : 0;
				[view keyDown:edit_key(NSEventTypeKeyDown, symbols[i], appleCodes[i], shift)];
				[view keyUp:edit_key(NSEventTypeKeyUp, symbols[i], appleCodes[i], releaseFirst ? 0 : shift)];
				[view flagsChanged:edit_key(NSEventTypeFlagsChanged, @"", 56, 0)];
				assert(count == (i ? 4 : 2));
				unsigned down = i ? 1 : 0;
				unsigned up = i ? (releaseFirst ? 3 : 2) : 1;
				assert(keysSeen[down] == scanCodes[i] && !(flagsSeen[down] & KBD_FLAGS_RELEASE));
				assert(keysSeen[up] == scanCodes[i] && (flagsSeen[up] & KBD_FLAGS_RELEASE));
				if (i) {
					unsigned release = releaseFirst ? 2 : 3;
					assert(keysSeen[0] == 0x2a && !(flagsSeen[0] & KBD_FLAGS_RELEASE));
					assert(keysSeen[release] == 0x2a && (flagsSeen[release] & KBD_FLAGS_RELEASE));
				}
			}
		}
		puts("native keyboard: 12 ANSI symbols, modifier-first and key-first release passed");
		count = 0;
		assert([view vcwCanPerformEdit:@selector(selectAll:)]);
		[view selectAll:nil]; assert(count == 4);
		assert(freerdp_settings_set_bool(instance->context->settings, FreeRDP_RedirectClipboard, TRUE));
		VCWClipboardDelivery *delivery = [[NSClassFromString(@"VCWClipboardDelivery") alloc] initWithEnabled:YES];
		VCWEditPasteboard *board = [VCWEditPasteboard new];
		NSPasteboardItem *item = [[[NSPasteboardItem alloc] init] autorelease];
		[item setString:@"a_b|c 中文" forType:NSPasteboardTypeString];
		board.items = @[item]; board.revision = 21;
		[delivery recordRemoteWriteVersion:board.revision]; /* Explicit paste of remote-origin text still confirms and works. */
		mac.clipboard = ClipboardCreate(); mac.clipboardDelivery = delivery;
		channel.custom = &mac; channel.ClientFormatList = advertise;
		rdpContext *context = instance->context;
		set_field(view, "context", &context, sizeof(context));
		set_field(view, "clipboard_delivery", &delivery, sizeof(delivery));
		set_field(view, "pasteboard_rd", &board, sizeof(board));
		count = 0;
		focused = NO;
		unsigned unfocusedReads = board.reads;
		[view paste:nil];
		assert(count == 0 && advertisements == 0 && board.reads == unfocusedReads && ![delivery hasPendingPaste]);
		focused = YES;
		[view paste:nil];
		assert(count == 0 && advertisements == 1 && [delivery hasPendingPaste]);
		[view paste:nil]; assert(advertisements == 1); /* Busy action is idempotent. */
		assert([delivery acknowledgeAdvertisement:YES]);
		[view vcwAdvancePaste];
		assert(count == 4 && keysSeen[1] == 0x2f && ![delivery hasPendingPaste]);
		[view vcwAdvancePaste]; assert(count == 4);
		UINT32 textSize = 0;
		char *text = ClipboardGetData(mac.clipboard, ClipboardGetFormatId(mac.clipboard, "text/plain"), &textSize);
		assert(text && strcmp(text, "a_b|c 中文") == 0); free(text);
		/* A new local version supersedes a waiting action, never pastes old text. */
		board.revision = 22; count = 0;
		[view paste:nil]; board.revision = 23;
		assert([delivery acknowledgeAdvertisement:YES]);
		[view vcwAdvancePaste];
		assert(count == 0 && rejected == 1 && ![delivery hasPendingPaste]);
		assert([delivery acknowledgeAdvertisement:YES]); /* New version was published, not pasted. */
		board.revision = 24;
		[view paste:nil]; focused = NO;
		assert([delivery acknowledgeAdvertisement:YES]);
		[view vcwAdvancePaste]; assert(count == 0 && ![delivery hasPendingPaste]);
		focused = YES;
		board.items = @[]; board.revision = 25;
		[view paste:nil]; assert([delivery acknowledgeAdvertisement:YES]);
		[view vcwAdvancePaste]; assert(count == 0); /* Empty text never invokes stale guest paste. */
		assert(freerdp_settings_set_bool(context->settings, FreeRDP_RedirectClipboard, FALSE));
		unsigned reads = board.reads;
		[view paste:nil]; assert(board.reads == reads && count == 0);
		[delivery invalidate];
		ClipboardDestroy(mac.clipboard);
		void *cleared = NULL;
		set_field(view, "clipboard_delivery", &cleared, sizeof(cleared));
		set_field(view, "pasteboard_rd", &cleared, sizeof(cleared));
		set_field(view, "context", &cleared, sizeof(cleared));
		[board release]; [delivery release];
		view.is_connected = NO;
		count = 0;
		for (size_t i = 0; i < 3; i++) {
			assert(![view vcwCanPerformEdit:actions[i]]);
			invoke(view, actions[i]);
		}
		assert(count == 0 && ![view vcwCanPerformEdit:@selector(paste:)]);
		vcw_test_set_active(instance->context, FALSE);
		freerdp_context_free(instance);
		freerdp_free(instance);
		void *empty = NULL;
		set_field(view, "instance", &empty, sizeof(empty));
		set_field(view, "mfc", &empty, sizeof(empty));
		[view release];
		puts("native edit: copy/cut/selectAll, confirmed paste, version/focus cancellation, empty content, policy and failed sends passed");
	}
	return 0;
}
