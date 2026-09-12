#import "VCWTextInputHost.h"

@implementation VCWTextInputHost {
    VCWTextInputClient *client;
    id<VCWInputContext> input;
    BOOL suppressed[256];
    BOOL (^current)(void);
    void (^commit)(NSString *);
    NSRect (^anchor)(void);
    id<VCWInputContext> (^factory)(VCWTextInputClient *);
}
- (instancetype)initWithCurrent:(BOOL (^)(void))check commit:(void (^)(NSString *))send
                         anchor:(NSRect (^)(void))rect factory:(id<VCWInputContext> (^)(VCWTextInputClient *))make {
    self = [super init];
    if (self) { current=[check copy]; commit=[send copy]; anchor=[rect copy]; factory=[make copy]; }
    return self;
}
- (void)dealloc {
    [self cancel]; [current release]; [commit release]; [anchor release]; [factory release]; [super dealloc];
}
- (void)cancel {
    NSAssert([NSThread isMainThread], @"IME requires the main thread");
    VCWTextInputClient *oldClient=client; client=nil;
    id<VCWInputContext> oldInput=input; input=nil;
    /* Discard can synchronously call unmark/insert: invalidate first. */
    [oldClient invalidate];
    [oldInput discardMarkedText]; [oldInput deactivate];
    [oldInput release]; [oldClient release];
}
- (BOOL)handleEvent:(NSEvent *)event localIME:(BOOL)localIME {
    NSAssert([NSThread isMainThread], @"IME requires the main thread");
    unsigned short key=event.keyCode;
    if (event.type==NSEventTypeKeyUp) {
        if (key<256 && suppressed[key]) { suppressed[key]=NO; return YES; }
        return NO;
    }
    if (event.type!=NSEventTypeKeyDown) return NO;
    if (key<256) suppressed[key]=NO;
    if (!current || !current()) { [self cancel]; return NO; }
    if (!localIME || (event.modifierFlags & (NSEventModifierFlagCommand|NSEventModifierFlagControl))) {
        [self cancel]; return NO;
    }
    if (!client.isActive) {
        [self cancel];
        client=[[VCWTextInputClient alloc] initWithCurrent:current commit:commit anchor:anchor];
        input=factory ? [factory(client) retain] : (id<VCWInputContext>)[[NSTextInputContext alloc] initWithClient:client];
        [input activate];
    }
    /* Owner teardown is permitted during synchronous AppKit callbacks. */
    VCWTextInputClient *transaction=[client retain];
    id<VCWInputContext> context=[input retain];
    BOOL handled=[context handleEvent:event];
    BOOL physical=[transaction takePhysicalCommand];
    if (handled && !physical && key<256) suppressed[key]=YES;
    [context release]; [transaction release];
    return handled && !physical;
}
@end
