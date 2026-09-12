#define VCW_CLIPBOARD_DELIVERY_IMPLEMENTATION
#import "../VCWClipboardDelivery.h"
#include <assert.h>
#include <stdatomic.h>
#include <stdio.h>

static void wait_signal(dispatch_semaphore_t signal)
{
	assert(dispatch_semaphore_wait(signal, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC)) == 0);
}

int main(void)
{
	@autoreleasepool
	{
		__block unsigned writes = 0;
		VCWClipboardDelivery *denied = [[VCWClipboardDelivery alloc] initWithEnabled:NO];
		[denied performIfActive:^{ writes++; }];
		assert(writes == 0);
		[denied release];

		dispatch_queue_t queue = dispatch_queue_create("vcw.clipboard.test", DISPATCH_QUEUE_SERIAL);
		dispatch_suspend(queue);
		VCWClipboardDelivery *old = [[VCWClipboardDelivery alloc] initWithEnabled:YES];
		dispatch_async(queue, ^{ [old performIfActive:^{ writes++; }]; });
		[old invalidate];
		[old invalidate]; /* Idempotent teardown. */
		[old release]; /* The queued block must own the gate, not a raw context. */
		dispatch_resume(queue);
		dispatch_sync(queue, ^{});
		assert(writes == 0);

		VCWClipboardDelivery *current = [[VCWClipboardDelivery alloc] initWithEnabled:YES];
		assert(![current mayWriteForLocalGeneration:0 version:5]);
		[current observeLocalVersion:5];
		assert([current mayWriteForLocalGeneration:0 version:5]);
		[current recordRemoteWriteVersion:7];
		assert([current mayWriteForLocalGeneration:0 version:7]);
		assert(![current mayWriteForLocalGeneration:0 version:8]);
		assert([current localGeneration] == 1);
		assert([current mayWriteForLocalGeneration:1 version:8]);
		[current observeLocalVersion:9];
		assert(![current mayWriteForLocalGeneration:1 version:9]);
		__block unsigned remoteWrites = 0;
		NSUInteger firstGeneration = [current remoteGeneration];
		[current advanceRemoteGeneration];
		[current performForRemoteGeneration:firstGeneration action:^{ remoteWrites++; }];
		assert(remoteWrites == 0);
		[current performForRemoteGeneration:[current remoteGeneration] action:^{ remoteWrites++; }];
		assert(remoteWrites == 1);
		assert([current beginPasteVersion:10 atTime:100]);
		assert(![current beginPasteVersion:10 atTime:101]);
		assert([current beginAdvertisement]);
		assert([current takePasteForVersion:10 published:10 atTime:101] == VCWPasteWaiting);
		assert([current acknowledgeAdvertisement:YES]);
		assert([current takePasteForVersion:10 published:10 atTime:102] == VCWPasteReady);
		assert([current takePasteForVersion:10 published:10 atTime:102] == VCWPasteIdle);
		assert([current beginPasteVersion:11 atTime:110]);
		assert([current takePasteForVersion:12 published:11 atTime:111] == VCWPasteRejected);
		assert([current beginPasteVersion:12 atTime:120]);
		assert([current beginAdvertisement]);
		assert([current takePasteForVersion:12 published:12 atTime:128] == VCWPasteRejected);
		assert(![current canPublishAdvertisement]); /* Timeout cannot release an untagged protocol request. */
		assert([current acknowledgeAdvertisement:YES]);
		assert([current takePasteForVersion:12 published:12 atTime:129] == VCWPasteIdle);
		assert([current beginPasteVersion:13 atTime:130]);
		assert([current takePasteForVersion:13 published:12 atTime:131] == VCWPasteWaiting);
		assert([current beginAdvertisement]);
		assert([current acknowledgeAdvertisement:NO]);
		assert([current takePasteForVersion:13 published:13 atTime:132] == VCWPasteRejected);
		assert([current beginPasteVersion:14 atTime:140]);
		[current cancelPaste];
		assert(![current hasPendingPaste]);
		[current performIfActive:^{ writes++; }];
		assert(writes == 1); /* An old channel cannot close a new channel's gate. */

		dispatch_semaphore_t entered = dispatch_semaphore_create(0);
		dispatch_semaphore_t finish = dispatch_semaphore_create(0);
		dispatch_semaphore_t closing = dispatch_semaphore_create(0);
		dispatch_semaphore_t closed = dispatch_semaphore_create(0);
		__block atomic_bool invalidated = false;
		dispatch_async(queue, ^{
			[current performIfActive:^{
				dispatch_semaphore_signal(entered);
				wait_signal(finish);
				assert(!atomic_load(&invalidated));
				writes++;
			}];
		});
		wait_signal(entered);
		dispatch_async(dispatch_get_global_queue(QOS_CLASS_DEFAULT, 0), ^{
			dispatch_semaphore_signal(closing);
			[current invalidate];
			atomic_store(&invalidated, true);
			dispatch_semaphore_signal(closed);
		});
		wait_signal(closing);
		dispatch_semaphore_signal(finish);
		wait_signal(closed);
		dispatch_sync(queue, ^{});
		[current performIfActive:^{ writes++; }];
		assert(writes == 2 && atomic_load(&invalidated));
		[current performForRemoteGeneration:[current remoteGeneration] action:^{ remoteWrites++; }];
		assert(remoteWrites == 1);
		[current release];
		dispatch_release(entered);
		dispatch_release(finish);
		dispatch_release(closing);
		dispatch_release(closed);
		dispatch_release(queue);
		puts("clipboard delivery: denied, queued cancellation, retained lifetime, new channel and in-flight close passed");
	}
	return 0;
}
