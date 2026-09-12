#import "../VCWTextInputHost.h"
#include <assert.h>
#include <stdio.h>
@interface TestContext : NSObject <VCWInputContext>
@property(nonatomic, retain) VCWTextInputClient *client;
@property(nonatomic) unsigned mode;
@end
@implementation TestContext
- (void)dealloc { [_client release]; [super dealloc]; }
- (void)activate {}
- (void)deactivate {}
- (void)discardMarkedText { [self.client unmarkText]; }
- (BOOL)handleEvent:(NSEvent *)event {
    (void)event;
    if (self.mode==1) [self.client insertText:@"中文" replacementRange:NSMakeRange(NSNotFound,0)];
    else if (self.mode==2) [self.client doCommandBySelector:@selector(insertNewline:)];
    else [self.client setMarkedText:@"zhong" selectedRange:NSMakeRange(5,0) replacementRange:NSMakeRange(NSNotFound,0)];
    return YES;
}
@end
static NSEvent *key(NSEventType type, NSEventModifierFlags flags) {
    return [NSEvent keyEventWithType:type location:NSZeroPoint modifierFlags:flags timestamp:0 windowNumber:0
        context:nil characters:@"a" charactersIgnoringModifiers:@"a" isARepeat:NO keyCode:0];
}
int main(void) {
    @autoreleasepool {
        __block unsigned commits=0, created=0;
        __block TestContext *context=nil;
        VCWTextInputHost *host=[[VCWTextInputHost alloc] initWithCurrent:^{return YES;}
            commit:^(NSString *text){ assert([text isEqualToString:@"中文"]); commits++; }
            anchor:^{return NSZeroRect;}
            factory:^id<VCWInputContext>(VCWTextInputClient *client){
                created++;
                context=[[[TestContext alloc] init] autorelease]; context.client=client; return context;
            }];
        assert(![host handleEvent:key(NSEventTypeKeyDown,0) localIME:NO] && !created);
        assert([host handleEvent:key(NSEventTypeKeyDown,0) localIME:YES] && created==1 && !commits);
        assert([host handleEvent:key(NSEventTypeKeyUp,0) localIME:YES]);
        VCWTextInputClient *old=[context.client retain];
        [host cancel];
        assert(!commits); // discardMarkedText invoked unmark, but was already invalidated.
        [old insertText:@"late" replacementRange:NSMakeRange(NSNotFound,0)]; assert(!commits);
        [old release];
        assert([host handleEvent:key(NSEventTypeKeyDown,0) localIME:YES] && created==2);
        context.mode=1;
        assert([host handleEvent:key(NSEventTypeKeyDown,0) localIME:YES] && commits==1);
        assert([host handleEvent:key(NSEventTypeKeyUp,0) localIME:YES]);
        assert(![host handleEvent:key(NSEventTypeKeyUp,0) localIME:YES]);
        assert(![host handleEvent:key(NSEventTypeKeyDown,NSEventModifierFlagCommand) localIME:YES]);
        assert([host handleEvent:key(NSEventTypeKeyDown,0) localIME:YES] && created==3);
        [host cancel];
        // New context without preedit: command must remain a physical event.
        VCWTextInputHost *commands=[[VCWTextInputHost alloc] initWithCurrent:^{return YES;}
            commit:^(NSString *text){ (void)text; assert(0); } anchor:^{return NSZeroRect;}
            factory:^id<VCWInputContext>(VCWTextInputClient *client){
                TestContext *c=[[[TestContext alloc] init] autorelease]; c.client=client; c.mode=2; return c;
            }];
        assert(![commands handleEvent:key(NSEventTypeKeyDown,0) localIME:YES]);
        assert(![commands handleEvent:key(NSEventTypeKeyUp,0) localIME:YES]);
        [commands release]; [host release];
        puts("IME event host: preedit, key pairs, shortcuts, command fallback and stale discard passed");
    }
}
