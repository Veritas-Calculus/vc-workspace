/* The production Clipboard.m is compiled into this test. Only its view class
 * name is substituted to expose the otherwise hidden ivar symbols. No window
 * or system pasteboard is used; conversion and callbacks are real FreeRDP. */
#import "MRDPView.h"
#import "Clipboard.h"
#import "../VCWClipboardDelivery.h"
#include <assert.h>
#include <stdio.h>

static unsigned pasteAdvances;

#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wincomplete-implementation"
@implementation VCWFixtureRDPView
@synthesize is_connected;
- (void)vcwSetClipboardDelivery:(id)delivery { (void)delivery; }
- (void)vcwClearClipboardDelivery:(id)delivery { (void)delivery; }
- (void)vcwAdvancePaste { assert([NSThread isMainThread]); pasteAdvances++; }
@end
#pragma clang diagnostic pop

@interface VCWPrivateWriteBoard : NSObject
@property(nonatomic) unsigned writes;
@property(nonatomic) NSInteger revision;
@property(nonatomic, copy) NSString *value;
@end
@implementation VCWPrivateWriteBoard
- (NSInteger)changeCount { return self.revision; }
- (NSInteger)declareTypes:(NSArray *)types owner:(id)owner
{
	(void)types; (void)owner;
	return ++self.revision;
}
- (BOOL)setString:(NSString *)value forType:(NSString *)type
{
	assert([type isEqualToString:NSPasteboardTypeString]);
	self.value = value;
	self.writes++;
	self.revision++;
	return YES;
}
- (void)dealloc { [_value release]; [super dealloc]; }
@end

static void drain_main_queue(void)
{
	__block BOOL done = NO;
	dispatch_async(dispatch_get_main_queue(), ^{ done = YES; });
	NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:5];
	while (!done && deadline.timeIntervalSinceNow > 0)
		CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.01, true);
	assert(done);
}

static UINT accept_offer(CliprdrClientContext *channel, const CLIPRDR_FORMAT_LIST_RESPONSE *response)
{
	(void)channel;
	assert(response->common.msgFlags == CB_RESPONSE_OK);
	return CHANNEL_RC_OK;
}

static void run_case(BOOL enabled, BOOL disconnectBeforeDelivery, BOOL supersedeBeforeDelivery, BOOL localCopyBeforeDelivery)
{
	mfContext mac = {0};
	CliprdrClientContext channel = {0};
	VCWFixtureRDPView *view = [[VCWFixtureRDPView alloc] initWithFrame:NSZeroRect];
	VCWPrivateWriteBoard *board = [VCWPrivateWriteBoard new];
	view->pasteboard_wr = (NSPasteboard *)board;
	mac.view = view;
	mac.common.context.settings = freerdp_settings_new(0);
	assert(mac.common.context.settings);
	assert(freerdp_settings_set_bool(mac.common.context.settings, FreeRDP_RedirectClipboard, enabled));
	mac_cliprdr_init(&mac, &channel);
	drain_main_queue(); /* Establish the local baseline before remote traffic. */
	const char *outboundText = "local-only";
	UINT32 outboundFormat = ClipboardRegisterFormat(mac.clipboard, "text/plain");
	assert(ClipboardSetData(mac.clipboard, outboundFormat, outboundText, strlen(outboundText) + 1));
	mac.serverFormats = calloc(1, sizeof(CLIPRDR_FORMAT));
	assert(mac.serverFormats);
	mac.serverFormats[0].formatId = CF_UNICODETEXT;
	mac.numServerFormats = 1;
	mac.requestedFormatId = CF_UNICODETEXT;
	mac.clipboardRequestPending = TRUE;
	const BYTE text[] = { 'x', 0, '_', 0, '|', 0, 0, 0 };
	CLIPRDR_FORMAT_DATA_RESPONSE response = {0};
	response.common.msgFlags = CB_RESPONSE_OK;
	response.common.dataLen = sizeof(text);
	response.requestedFormatData = text;
	const UINT16 rejectedFlags[] = { 0, CB_RESPONSE_FAIL, CB_RESPONSE_OK | CB_RESPONSE_FAIL };
	for (unsigned index = 0; index < sizeof(rejectedFlags) / sizeof(rejectedFlags[0]); index++)
	{
		response.common.msgFlags = rejectedFlags[index];
		assert(channel.ServerFormatDataResponse(&channel, &response) == ERROR_INTERNAL_ERROR);
	}
	response.common.msgFlags = CB_RESPONSE_OK;
	response.requestedFormatData = NULL;
	assert(channel.ServerFormatDataResponse(&channel, &response) == ERROR_INTERNAL_ERROR);
	response.requestedFormatData = text;
	assert(channel.ServerFormatDataResponse(&channel, &response) == CHANNEL_RC_OK);
	UINT32 outboundSize = 0;
	char *outbound = ClipboardGetData(mac.clipboard, outboundFormat, &outboundSize);
	assert(outbound && outboundSize == strlen(outboundText) + 1 &&
	    memcmp(outbound, outboundText, outboundSize) == 0);
	free(outbound); /* Incoming conversion must not replace the outbound snapshot. */
	assert(board.writes == 0); /* Must be asynchronous, not on protocol thread. */
	if (localCopyBeforeDelivery) { board.value = @"new local content"; board.revision++; }
	if (supersedeBeforeDelivery)
	{
		channel.ClientFormatListResponse = accept_offer;
		CLIPRDR_FORMAT_LIST empty = {0};
		assert(channel.ServerFormatList(&channel, &empty) == CHANNEL_RC_OK);
	}
	free(mac.serverFormats); mac.serverFormats = NULL; mac.numServerFormats = 0;
	if (disconnectBeforeDelivery)
	{
		mac_cliprdr_uninit(&mac, &channel);
		freerdp_settings_free(mac.common.context.settings);
		memset(&mac, 0xA5, sizeof(mac)); /* A queued delivery cannot read the old context. */
	}
	drain_main_queue();
	assert(board.writes == ((enabled && !disconnectBeforeDelivery && !supersedeBeforeDelivery && !localCopyBeforeDelivery) ? 1U : 0U));
	if (localCopyBeforeDelivery) assert([board.value isEqualToString:@"new local content"]);
	if (board.writes) assert([board.value isEqualToString:@"x_|"]);
	if (!disconnectBeforeDelivery)
	{
		mac_cliprdr_uninit(&mac, &channel);
		freerdp_settings_free(mac.common.context.settings);
	}
	view->pasteboard_wr = nil;
	[view release];
	[board release];
}

static unsigned dataReplies;
static UINT responseResult = CHANNEL_RC_OK;
static UINT reply(CliprdrClientContext *channel, const CLIPRDR_FORMAT_DATA_RESPONSE *response)
{
	BOOL accepted = [(VCWClipboardDelivery *)((mfContext *)channel->custom)->clipboardDelivery formatsAccepted];
	assert(response->common.msgFlags == (accepted ? CB_RESPONSE_OK : CB_RESPONSE_FAIL));
	assert(accepted ? (response->common.dataLen > 0 && response->requestedFormatData) :
	    (response->common.dataLen == 0 && !response->requestedFormatData));
	dataReplies++;
	return responseResult;
}
static UINT list_reply(CliprdrClientContext *channel, const CLIPRDR_FORMAT_LIST_RESPONSE *response)
{
	assert(channel->custom && response->common.msgFlags == CB_RESPONSE_OK && response->common.dataLen == 0);
	return responseResult;
}
static UINT advertise(CliprdrClientContext *channel, const CLIPRDR_FORMAT_LIST *list)
{
	assert(channel->custom && list->numFormats > 0);
	return CHANNEL_RC_OK;
}
static void run_acknowledgments(void)
{
	mfContext mac = {0};
	CliprdrClientContext channel = {0};
	mac.common.context.settings = freerdp_settings_new(0);
	assert(freerdp_settings_set_bool(mac.common.context.settings, FreeRDP_RedirectClipboard, TRUE));
	mac_cliprdr_init(&mac, &channel);
	channel.ClientFormatList = advertise;
	channel.ClientFormatDataResponse = reply;
	channel.ClientFormatListResponse = list_reply;
	VCWClipboardDelivery *delivery = (VCWClipboardDelivery *)mac.clipboardDelivery;
	assert(ClipboardSetData(mac.clipboard, ClipboardRegisterFormat(mac.clipboard, "text/plain"), "approved", 9));
	CLIPRDR_FORMAT_DATA_REQUEST request = { .requestedFormatId = CF_UNICODETEXT };
	assert(channel.ServerFormatDataRequest(&channel, &request) == CHANNEL_RC_OK);
	assert(!delivery.formatsAccepted); /* No list sent is not acceptance. */
	const UINT16 flags[] = { CB_RESPONSE_OK, CB_RESPONSE_FAIL, 0, CB_RESPONSE_OK | CB_RESPONSE_FAIL, CB_RESPONSE_OK };
	for (unsigned index = 0; index < sizeof(flags) / sizeof(flags[0]); index++)
	{
		assert(mac_cliprdr_send_client_format_list(&channel) == 1);
		assert(!delivery.formatsAccepted);
		assert(mac_cliprdr_send_client_format_list(&channel) == -1); /* One in flight. */
		assert(channel.ServerFormatDataRequest(&channel, &request) == CHANNEL_RC_OK);
		CLIPRDR_FORMAT_LIST_RESPONSE ack = {0};
		ack.common.msgFlags = flags[index];
		ack.common.dataLen = index == 4 ? 1 : 0;
		assert(channel.ServerFormatListResponse(&channel, &ack) == CHANNEL_RC_OK);
		assert(delivery.formatsAccepted == (index == 0));
		assert(channel.ServerFormatDataRequest(&channel, &request) == CHANNEL_RC_OK);
	}
	CLIPRDR_FORMAT_LIST_RESPONSE unsolicited = {0};
	unsolicited.common.msgFlags = CB_RESPONSE_OK;
	assert(channel.ServerFormatListResponse(&channel, &unsolicited) == CHANNEL_RC_OK);
	assert(!delivery.formatsAccepted); /* An unsolicited ACK cannot revive rejection. */
	assert(dataReplies == 11);
	responseResult = ERROR_INTERNAL_ERROR;
	assert(channel.ServerFormatDataRequest(&channel, &request) == ERROR_INTERNAL_ERROR);
	CLIPRDR_FORMAT_LIST empty = {0};
	assert(channel.ServerFormatList(&channel, &empty) == ERROR_INTERNAL_ERROR);
	responseResult = CHANNEL_RC_OK;
	assert(channel.ServerFormatList(&channel, &empty) == CHANNEL_RC_OK);
	mac_cliprdr_uninit(&mac, &channel);
	freerdp_settings_free(mac.common.context.settings);
	drain_main_queue();
}

static void run_paste_ack(BOOL closeBeforeCallback)
{
	mfContext mac = {0};
	CliprdrClientContext channel = {0};
	VCWFixtureRDPView *view = [[VCWFixtureRDPView alloc] initWithFrame:NSZeroRect];
	mac.view = view;
	mac.common.context.settings = freerdp_settings_new(0);
	assert(freerdp_settings_set_bool(mac.common.context.settings, FreeRDP_RedirectClipboard, TRUE));
	mac_cliprdr_init(&mac, &channel);
	VCWClipboardDelivery *delivery = (VCWClipboardDelivery *)mac.clipboardDelivery;
	assert([delivery beginPasteVersion:1 atTime:100]);
	assert([delivery beginAdvertisement]);
	CLIPRDR_FORMAT_LIST_RESPONSE ack = {0};
	ack.common.msgFlags = CB_RESPONSE_OK;
	unsigned before = pasteAdvances;
	assert(channel.ServerFormatListResponse(&channel, &ack) == CHANNEL_RC_OK);
	assert(pasteAdvances == before); /* No AppKit work on the protocol callback. */
	if (closeBeforeCallback) mac_cliprdr_uninit(&mac, &channel);
	drain_main_queue();
	assert(pasteAdvances == before + (closeBeforeCallback ? 0 : 1));
	if (!closeBeforeCallback) mac_cliprdr_uninit(&mac, &channel);
	freerdp_settings_free(mac.common.context.settings);
	drain_main_queue();
	[view release];
}

static unsigned incomingRequests;
static UINT incomingResult = CHANNEL_RC_OK;
static UINT incoming_request(CliprdrClientContext *channel, const CLIPRDR_FORMAT_DATA_REQUEST *request)
{
	(void)channel;
	assert(request->requestedFormatId != 0); /* No request exists for unsupported offers. */
	incomingRequests++;
	return incomingResult;
}
static void run_unsupported_offer(void)
{
	mfContext mac = {0};
	CliprdrClientContext channel = {0};
	mac.common.context.settings = freerdp_settings_new(0);
	assert(freerdp_settings_set_bool(mac.common.context.settings, FreeRDP_RedirectClipboard, TRUE));
	mac_cliprdr_init(&mac, &channel);
	channel.ClientFormatListResponse = list_reply;
	channel.ClientFormatDataRequest = incoming_request;
	CLIPRDR_FORMAT format = { .formatId = CF_DIB };
	CLIPRDR_FORMAT_LIST list = { .numFormats = 1, .formats = &format };
	assert(channel.ServerFormatList(&channel, &list) == CHANNEL_RC_OK);
	assert(incomingRequests == 0);
	format.formatId = CF_UNICODETEXT;
	assert(channel.ServerFormatList(&channel, &list) == CHANNEL_RC_OK);
	assert(incomingRequests == 1);
	mac_cliprdr_uninit(&mac, &channel);
	freerdp_settings_free(mac.common.context.settings);
}

static void run_incoming_sequence(void)
{
	mfContext mac = {0};
	CliprdrClientContext channel = {0};
	VCWFixtureRDPView *view = [[VCWFixtureRDPView alloc] initWithFrame:NSZeroRect];
	VCWPrivateWriteBoard *board = [VCWPrivateWriteBoard new];
	view->pasteboard_wr = (NSPasteboard *)board;
	mac.view = view;
	mac.common.context.settings = freerdp_settings_new(0);
	assert(freerdp_settings_set_bool(mac.common.context.settings, FreeRDP_RedirectClipboard, TRUE));
	mac_cliprdr_init(&mac, &channel);
	channel.ClientFormatListResponse = accept_offer;
	channel.ClientFormatDataRequest = incoming_request;
	incomingRequests = 0;
	CLIPRDR_FORMAT format = { .formatId = CF_UNICODETEXT };
	CLIPRDR_FORMAT_LIST list = { .numFormats = 1, .formats = &format };
	assert(channel.ServerFormatList(&channel, &list) == CHANNEL_RC_OK);
	format.formatId = CF_TEXT;
	assert(channel.ServerFormatList(&channel, &list) == CHANNEL_RC_OK);
	format.formatId = CF_UNICODETEXT;
	assert(channel.ServerFormatList(&channel, &list) == CHANNEL_RC_OK);
	assert(incomingRequests == 1); /* A and B cannot both await an untagged response. */
	const BYTE old[] = { 'o', 0, 'l', 0, 'd', 0, 0, 0 };
	const BYTE latest[] = { 'n', 0, 'e', 0, 'w', 0, 0, 0 };
	CLIPRDR_FORMAT_DATA_RESPONSE response = {0};
	response.common.msgFlags = CB_RESPONSE_OK;
	response.common.dataLen = sizeof(old); response.requestedFormatData = old;
	assert(channel.ServerFormatDataResponse(&channel, &response) == CHANNEL_RC_OK);
	assert(incomingRequests == 2); /* Skip the superseded middle offer. */
	drain_main_queue(); assert(board.writes == 0);
	response.requestedFormatData = latest;
	assert(channel.ServerFormatDataResponse(&channel, &response) == CHANNEL_RC_OK);
	drain_main_queue(); assert(board.writes == 1 && [board.value isEqualToString:@"new"]);
	assert(channel.ServerFormatDataResponse(&channel, &response) == CHANNEL_RC_OK);
	drain_main_queue(); assert(board.writes == 1); /* Ignore unsolicited duplicates. */
	/* A normal negative response drains the request without killing the channel. */
	assert(channel.ServerFormatList(&channel, &list) == CHANNEL_RC_OK);
	response.common.msgFlags = CB_RESPONSE_FAIL;
	response.common.dataLen = 0; response.requestedFormatData = NULL;
	assert(channel.ServerFormatDataResponse(&channel, &response) == CHANNEL_RC_OK);
	assert(!mac.clipboardRequestPending);
	/* An obsolete negative response still advances to the most recent offer. */
	assert(channel.ServerFormatList(&channel, &list) == CHANNEL_RC_OK);
	assert(channel.ServerFormatList(&channel, &list) == CHANNEL_RC_OK);
	unsigned before = incomingRequests;
	assert(channel.ServerFormatDataResponse(&channel, &response) == CHANNEL_RC_OK);
	assert(incomingRequests == before + 1 && mac.clipboardRequestPending);
	CLIPRDR_FORMAT_LIST empty = {0};
	assert(channel.ServerFormatList(&channel, &empty) == CHANNEL_RC_OK);
	before = incomingRequests;
	assert(channel.ServerFormatDataResponse(&channel, &response) == CHANNEL_RC_OK);
	assert(incomingRequests == before && !mac.clipboardRequestPending);
	incomingResult = ERROR_INTERNAL_ERROR;
	assert(channel.ServerFormatList(&channel, &list) == ERROR_INTERNAL_ERROR);
	assert(!mac.clipboardRequestPending);
	incomingResult = CHANNEL_RC_OK;
	assert(channel.ServerFormatList(&channel, &list) == CHANNEL_RC_OK);
	response.common.msgFlags = CB_RESPONSE_OK;
	response.common.dataLen = sizeof(latest); response.requestedFormatData = latest;
	assert(channel.ServerFormatDataResponse(&channel, &response) == CHANNEL_RC_OK);
	drain_main_queue(); assert(board.writes == 2);
	assert(channel.ServerFormatList(&channel, &list) == CHANNEL_RC_OK);
	board.revision++; board.value = @"local wins";
	assert(channel.ServerFormatDataResponse(&channel, &response) == CHANNEL_RC_OK);
	drain_main_queue(); assert(board.writes == 2 && [board.value isEqualToString:@"local wins"]);
	assert(channel.ServerFormatList(&channel, &list) == CHANNEL_RC_OK);
	assert(channel.ServerFormatDataResponse(&channel, &response) == CHANNEL_RC_OK);
	drain_main_queue(); assert(board.writes == 3 && [board.value isEqualToString:@"new"]);
	mac_cliprdr_uninit(&mac, &channel);
	freerdp_settings_free(mac.common.context.settings);
	drain_main_queue();
	[view release]; [board release];
}

int main(void)
{
	@autoreleasepool
	{
		run_case(YES, NO, NO, NO);
		run_case(NO, NO, NO, NO);
		run_case(YES, YES, NO, NO);
		run_case(NO, YES, NO, NO);
		run_case(YES, NO, YES, NO);
		run_case(YES, NO, NO, YES);
		run_acknowledgments();
		run_paste_ack(NO);
		run_paste_ack(YES);
		run_unsupported_offer();
		run_incoming_sequence();
		puts("clipboard incoming: one in flight, latest offer, obsolete/duplicate responses, negative response and send failure passed");
		puts("clipboard offers: unsupported image does not request format zero; subsequent text is requested");
		puts("clipboard callback: real conversion, policy, disconnect, remote supersession and local-copy protection passed");
		puts("clipboard acknowledgment: pending, success, failure, malformed and unsolicited responses passed");
		puts("paste acknowledgment: main-thread handoff and disconnect-before-delivery passed");
	}
	return 0;
}
