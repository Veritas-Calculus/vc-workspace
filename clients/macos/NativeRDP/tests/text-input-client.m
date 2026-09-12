#import "../VCWTextInputClient.h"
#include <assert.h>
#include <stdio.h>
int main(void) {
    @autoreleasepool {
        __block BOOL focused = YES;
        __block unsigned calls = 0;
        __block NSString *sent = nil;
        VCWTextInputClient *(^make)(void) = ^{
            return [[VCWTextInputClient alloc] initWithCurrent:^{ return focused; }
                commit:^(NSString *text){ calls++; [sent release]; sent=[text copy]; }
                anchor:^{ return NSMakeRect(10,20,1,18); }];
        };
        VCWTextInputClient *client = make();
        assert([client conformsToProtocol:@protocol(NSTextInputClient)]);
        assert(!client.hasMarkedText && NSEqualRanges(client.selectedRange,NSMakeRange(0,0)));
        [client setMarkedText:@"zhong" selectedRange:NSMakeRange(5,0) replacementRange:NSMakeRange(NSNotFound,0)];
        assert(!calls && client.hasMarkedText);
        [client setMarkedText:@"wen" selectedRange:NSMakeRange(3,0) replacementRange:NSMakeRange(5,0)];
        NSRange actual;
        assert([[[client attributedSubstringForProposedRange:NSMakeRange(0,8) actualRange:&actual] string] isEqualToString:@"zhongwen"]);
        assert(NSEqualRanges(client.selectedRange,NSMakeRange(8,0)));
        assert(NSEqualRects([client firstRectForCharacterRange:NSMakeRange(0,8) actualRange:&actual],NSMakeRect(10,20,1,18)));
        [client insertText:[[[NSAttributedString alloc] initWithString:@"中文"] autorelease] replacementRange:NSMakeRange(NSNotFound,0)];
        assert(calls==1 && [sent isEqualToString:@"中文"] && !client.hasMarkedText);
        [client insertText:@"late" replacementRange:NSMakeRange(NSNotFound,0)]; assert(calls==1);
        [client release]; client=make();
        [client setMarkedText:@"draft" selectedRange:NSMakeRange(0,5) replacementRange:NSMakeRange(NSNotFound,0)];
        focused=NO;
        [client unmarkText]; assert(calls==1 && !client.hasMarkedText);
        focused=YES;
        [client insertText:@"stale focus" replacementRange:NSMakeRange(NSNotFound,0)]; assert(calls==1);
        [client release]; client=make();
        [client setMarkedText:@"x" selectedRange:NSMakeRange(1,0) replacementRange:NSMakeRange(NSNotFound,0)];
        [client doCommandBySelector:@selector(cancelOperation:)]; [client unmarkText]; assert(calls==1);
        [client release]; client=make();
        [client insertText:@"remote replacement" replacementRange:NSMakeRange(50,1)]; assert(calls==1);
        [client release]; client=make();
        [client setMarkedText:@"😀" selectedRange:NSMakeRange(2,0) replacementRange:NSMakeRange(NSNotFound,0)];
        [client setMarkedText:@"x" selectedRange:NSMakeRange(1,0) replacementRange:NSMakeRange(1,0)];
        assert(!client.hasMarkedText && calls==1);
        [client release]; client=make();
        [client setMarkedText:@"确认" selectedRange:NSMakeRange(2,0) replacementRange:NSMakeRange(NSNotFound,0)];
        [client unmarkText]; assert(calls==2 && [sent isEqualToString:@"确认"]);
        [client release]; [sent release];
        __block VCWTextInputClient *reentrant = nil;
        __block unsigned checks = 0;
        reentrant = [[VCWTextInputClient alloc] initWithCurrent:^{
            checks++;
            [reentrant invalidate];
            return YES;
        } commit:^(NSString *value){ (void)value; assert(0 && "invalidated owner cannot commit"); }
            anchor:^{ return NSZeroRect; }];
        assert(NSEqualRanges(reentrant.selectedRange, NSMakeRange(NSNotFound,0)));
        assert(checks == 1);
        assert(NSEqualRanges(reentrant.selectedRange, NSMakeRange(NSNotFound,0)));
        [reentrant insertText:@"late" replacementRange:NSMakeRange(NSNotFound,0)];
        assert(checks == 1);
        [reentrant release];
        checks = 0;
        reentrant = [[VCWTextInputClient alloc] initWithCurrent:^{
            if (++checks == 2) [reentrant invalidate];
            return YES;
        } commit:^(NSString *value){ (void)value; assert(0 && "focus lost during commit"); }
            anchor:^{ return NSZeroRect; }];
        [reentrant insertText:@"confirmed" replacementRange:NSMakeRange(NSNotFound,0)];
        assert(checks == 2);
        [reentrant release];
        puts("AppKit text client: preedit, replacement, commit, cancellation and focus lifetime passed");
    }
}
