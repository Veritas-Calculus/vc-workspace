#pragma once
#import <Foundation/Foundation.h>

/* Main-thread-only preedit state. Each focus owner gets a fresh token; late
 * callbacks must retain their original token, never acquire the current one. */
@interface VCWMarkedText : NSObject
@property(nonatomic, readonly) NSUInteger generation;
@property(nonatomic, readonly, copy) NSString *text;
@property(nonatomic, readonly) NSRange selection;
- (NSUInteger)activate;
- (void)cancel;
- (BOOL)update:(NSString *)text selection:(NSRange)selection generation:(NSUInteger)generation;
- (NSString *)commit:(NSString *)text generation:(NSUInteger)generation;
@end

#ifdef VCW_MARKED_TEXT_IMPLEMENTATION
@implementation VCWMarkedText {
    BOOL active;
    NSUInteger generation;
    NSString *text;
    NSRange selection;
}
@synthesize generation, text, selection;
- (instancetype)init {
    self = [super init];
    if (self) { text = [@"" copy]; selection = NSMakeRange(NSNotFound, 0); }
    return self;
}
- (void)dealloc { [text release]; [super dealloc]; }
- (void)clear {
    [text release]; text = [@"" copy]; selection = NSMakeRange(NSNotFound, 0);
}
- (NSUInteger)activate {
    NSAssert([NSThread isMainThread], @"IME state requires the main thread");
    [self cancel];
    if (generation == NSUIntegerMax) return 0;
    active = YES;
    return generation;
}
- (void)cancel {
    NSAssert([NSThread isMainThread], @"IME state requires the main thread");
    active = NO;
    [self clear];
    if (generation != NSUIntegerMax) generation++;
}
- (BOOL)owns:(NSUInteger)value {
    return active && value != 0 && value == generation;
}
- (BOOL)validText:(NSString *)value {
    if (![value isKindOfClass:[NSString class]] || value.length > 4096) return NO;
    for (NSUInteger i = 0; i < value.length; i++) {
        unichar c = [value characterAtIndex:i];
        if (c < 0x20 || c == 0x7f) return NO;
        if (c >= 0xd800 && c <= 0xdbff) {
            if (++i == value.length) return NO;
            c = [value characterAtIndex:i];
            if (c < 0xdc00 || c > 0xdfff) return NO;
        } else if (c >= 0xdc00 && c <= 0xdfff) return NO;
    }
    return YES;
}
- (BOOL)update:(NSString *)value selection:(NSRange)range generation:(NSUInteger)valueGeneration {
    NSAssert([NSThread isMainThread], @"IME state requires the main thread");
    if (![self owns:valueGeneration]) return NO;
    if (![self validText:value] || range.location > value.length || range.length > value.length - range.location) {
        [self cancel]; return NO;
    }
    NSUInteger boundaries[] = {range.location, range.location + range.length};
    for (unsigned i = 0; i < 2; i++) {
        if (boundaries[i] < value.length) {
            unichar c = [value characterAtIndex:boundaries[i]];
            if (c >= 0xdc00 && c <= 0xdfff) { [self cancel]; return NO; }
        }
    }
    NSString *snapshot = [value copy];
    [text release]; text = snapshot; selection = range;
    return YES;
}
- (NSString *)commit:(NSString *)value generation:(NSUInteger)valueGeneration {
    NSAssert([NSThread isMainThread], @"IME state requires the main thread");
    if (![self owns:valueGeneration]) return nil;
    NSString *snapshot = [self validText:value] && value.length ? [[value copy] autorelease] : nil;
    [self cancel];
    return snapshot;
}
@end
#endif
