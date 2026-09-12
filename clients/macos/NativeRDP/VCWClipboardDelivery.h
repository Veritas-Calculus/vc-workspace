/* A channel-owned, retained cancellation gate for queued main-thread writes.
 * Never capture a raw mfContext in a block which can outlive the channel. */
#pragma once
#import <Foundation/Foundation.h>

typedef NS_ENUM(NSInteger, VCWPasteDecision) {
	VCWPasteIdle, VCWPasteWaiting, VCWPasteReady, VCWPasteRejected
};

@interface VCWClipboardDelivery : NSObject
{
	BOOL active;
	BOOL awaitingAdvertisement;
	BOOL acceptedAdvertisement;
	BOOL pastePending;
	NSInteger pasteVersion;
	NSTimeInterval pasteDeadline;
	NSUInteger remoteGeneration;
	NSUInteger localGeneration;
	NSInteger localVersion;
	BOOL localVersionKnown;
	BOOL remoteOrigin;
}
- (instancetype)initWithEnabled:(BOOL)enabled;
- (void)performIfActive:(void (^)(void))action;
- (void)invalidate;
- (void)advanceRemoteGeneration;
- (NSUInteger)remoteGeneration;
- (void)performForRemoteGeneration:(NSUInteger)generation action:(void (^)(void))action;
- (void)observeLocalVersion:(NSInteger)version;
- (void)recordRemoteWriteVersion:(NSInteger)version;
- (BOOL)isRemoteVersion:(NSInteger)version;
- (NSUInteger)localGeneration;
- (BOOL)mayWriteForLocalGeneration:(NSUInteger)generation version:(NSInteger)version;
- (BOOL)beginAdvertisement;
- (BOOL)canPublishAdvertisement;
- (void)advertisementSendFailed;
- (BOOL)acknowledgeAdvertisement:(BOOL)accepted;
- (BOOL)formatsAccepted;
- (BOOL)beginPasteVersion:(NSInteger)version atTime:(NSTimeInterval)now;
- (BOOL)hasPendingPaste;
- (void)cancelPaste;
- (VCWPasteDecision)takePasteForVersion:(NSInteger)current published:(NSInteger)published atTime:(NSTimeInterval)now;
@end

#ifdef VCW_CLIPBOARD_DELIVERY_IMPLEMENTATION
@implementation VCWClipboardDelivery
- (instancetype)initWithEnabled:(BOOL)enabled
{
	self = [super init];
	if (self) active = enabled;
	return self;
}
- (void)performIfActive:(void (^)(void))action
{
	/* Invalidation waits for an already executing write. No main-queue wait
	 * occurs under this lock, and queued writes observe the closed state. */
	@synchronized(self)
	{
		if (active && action) action();
	}
}
- (void)invalidate
{
	@synchronized(self) { active = NO; awaitingAdvertisement = NO; acceptedAdvertisement = NO; pastePending = NO; }
}
- (void)advanceRemoteGeneration
{
	@synchronized(self)
	{
		if (!active) return;
		if (remoteGeneration == NSUIntegerMax) { [self invalidate]; return; }
		remoteGeneration++;
	}
}
- (NSUInteger)remoteGeneration
{
	@synchronized(self) { return remoteGeneration; }
}
- (void)performForRemoteGeneration:(NSUInteger)generation action:(void (^)(void))action
{
	@synchronized(self)
	{
		if (active && remoteGeneration == generation && action) action();
	}
}
- (BOOL)beginAdvertisement
{
	@synchronized(self)
	{
		/* The response has no request ID. Never have two advertisements in
		 * flight and mistake an older acknowledgment for the current content. */
		if (!active || awaitingAdvertisement) return NO;
		awaitingAdvertisement = YES;
		acceptedAdvertisement = NO;
		return YES;
	}
}
- (void)observeLocalVersion:(NSInteger)version
{
	@synchronized(self)
	{
		if (!active) return;
		if (localVersionKnown && version != localVersion)
		{
			if (localGeneration == NSUIntegerMax) { [self invalidate]; return; }
			localGeneration++;
			remoteOrigin = NO;
		}
		localVersion = version; localVersionKnown = YES;
	}
}
- (void)recordRemoteWriteVersion:(NSInteger)version
{
	@synchronized(self) { if (active) { localVersion = version; localVersionKnown = YES; remoteOrigin = YES; } }
}
- (BOOL)isRemoteVersion:(NSInteger)version
{
	@synchronized(self) { return active && localVersionKnown && remoteOrigin && localVersion == version; }
}
- (NSUInteger)localGeneration
{
	@synchronized(self) { return localGeneration; }
}
- (BOOL)mayWriteForLocalGeneration:(NSUInteger)generation version:(NSInteger)version
{
	@synchronized(self)
	{
		if (!active || !localVersionKnown) return NO;
		[self observeLocalVersion:version];
		return active && localGeneration == generation;
	}
}
- (BOOL)canPublishAdvertisement
{
	@synchronized(self) { return active && !awaitingAdvertisement; }
}
- (void)advertisementSendFailed
{
	@synchronized(self) { awaitingAdvertisement = NO; acceptedAdvertisement = NO; }
}
- (BOOL)acknowledgeAdvertisement:(BOOL)accepted
{
	@synchronized(self)
	{
		if (!active || !awaitingAdvertisement) return NO;
		awaitingAdvertisement = NO;
		acceptedAdvertisement = accepted;
		return YES;
	}
}
- (BOOL)formatsAccepted
{
	@synchronized(self) { return active && !awaitingAdvertisement && acceptedAdvertisement; }
}
- (BOOL)beginPasteVersion:(NSInteger)version atTime:(NSTimeInterval)now
{
	@synchronized(self)
	{
		if (!active || pastePending) return NO;
		pastePending = YES;
		pasteVersion = version;
		pasteDeadline = now + 8.0;
		return YES;
	}
}
- (BOOL)hasPendingPaste
{
	@synchronized(self) { return active && pastePending; }
}
- (void)cancelPaste
{
	@synchronized(self) { pastePending = NO; }
}
- (VCWPasteDecision)takePasteForVersion:(NSInteger)current published:(NSInteger)published atTime:(NSTimeInterval)now
{
	@synchronized(self)
	{
		if (!active || !pastePending) return VCWPasteIdle;
		if (current != pasteVersion || now >= pasteDeadline)
		{
			pastePending = NO;
			return VCWPasteRejected;
		}
		if (published != pasteVersion || awaitingAdvertisement) return VCWPasteWaiting;
		pastePending = NO;
		return acceptedAdvertisement ? VCWPasteReady : VCWPasteRejected;
	}
}
@end
#endif
