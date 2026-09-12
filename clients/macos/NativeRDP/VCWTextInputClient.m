#import "VCWTextInputClient.h"
#define VCW_MARKED_TEXT_IMPLEMENTATION
#import "VCWMarkedText.h"

@implementation VCWTextInputClient {
    VCWMarkedText *marked;
    NSUInteger generation;
    BOOL physicalCommand;
    BOOL (^current)(void);
    void (^submit)(NSString *);
    NSRect (^anchor)(void);
}
- (instancetype)initWithCurrent:(BOOL (^)(void))check commit:(void (^)(NSString *))commit anchor:(NSRect (^)(void))rect {
    self = [super init];
    if (self) {
        marked = [VCWMarkedText new]; generation = [marked activate];
        current = [check copy]; submit = [commit copy]; anchor = [rect copy];
    }
    return self;
}
- (void)dealloc {
    [marked release]; [current release]; [submit release]; [anchor release]; [super dealloc];
}
- (void)invalidate {
    [marked cancel]; generation = 0;
    [current release]; current = nil; [submit release]; submit = nil; [anchor release]; anchor = nil;
}
- (BOOL)available {
    NSAssert([NSThread isMainThread], @"Text input requires the main thread");
    NSUInteger expected = generation;
    BOOL (^check)(void) = [current copy];
    BOOL allowed = expected && check && check();
    [check release];
    // A focus check may synchronously tear down its owner. Hold the callback
    // until it returns and recheck identity before exposing any state.
    if (allowed && expected == generation && current) return YES;
    [self invalidate]; return NO;
}
- (NSString *)plain:(id)value {
    if ([value isKindOfClass:NSAttributedString.class]) value = [value string];
    return [value isKindOfClass:NSString.class] ? value : nil;
}
- (BOOL)localRange:(NSRange)range {
    NSString *text = marked.text;
    if (range.location > text.length || range.length > text.length - range.location) return NO;
    NSUInteger ends[] = {range.location, range.location + range.length};
    for (unsigned i=0;i<2;i++) if (ends[i] < text.length) {
        unichar c = [text characterAtIndex:ends[i]];
        if (c >= 0xdc00 && c <= 0xdfff) return NO;
    }
    return YES;
}
- (NSString *)replace:(id)value range:(NSRange)range offset:(NSUInteger *)offset {
    NSString *text = [self plain:value];
    if (!text || text.length > 4096) return nil;
    if (range.location == NSNotFound) {
        if (range.length) return nil;
        *offset = 0; return text;
    }
    if (![self localRange:range] || text.length > 4096 - (marked.text.length - range.length)) return nil;
    *offset = range.location;
    return [marked.text stringByReplacingCharactersInRange:range withString:text];
}
- (void)setMarkedText:(id)value selectedRange:(NSRange)selection replacementRange:(NSRange)replacement {
    if (![self available]) return;
    NSUInteger offset = 0;
    NSString *inserted = [self plain:value];
    NSString *next = [self replace:value range:replacement offset:&offset];
    if (!next || selection.location > inserted.length || selection.length > inserted.length - selection.location ||
        ![marked update:next selection:NSMakeRange(offset + selection.location, selection.length) generation:generation])
        [self invalidate];
}
- (void)insertText:(id)value replacementRange:(NSRange)replacement {
    if (![self available]) return;
    NSUInteger offset = 0;
    NSString *next = [self replace:value range:replacement offset:&offset];
    NSString *committed = [marked commit:next generation:generation];
    void (^action)(NSString *) = [submit copy];
    BOOL allowed = committed && [self available];
    [self invalidate];
    if (allowed && action) action(committed);
    [action release];
}
- (void)unmarkText {
    if (![self available] || !marked.text.length) return;
    [self insertText:marked.text replacementRange:NSMakeRange(NSNotFound,0)];
}
- (void)doCommandBySelector:(SEL)selector {
    /* Do not execute arbitrary selectors or forward an unconfirmed Return. */
    if (![self available]) return;
    if (selector == @selector(cancelOperation:) && marked.text.length) [self invalidate];
    else if (!marked.text.length) physicalCommand = YES;
}
- (BOOL)isActive { return [self available]; }
- (BOOL)takePhysicalCommand { BOOL value = physicalCommand; physicalCommand = NO; return value; }
- (BOOL)hasMarkedText { return [self available] && marked.text.length > 0; }
- (NSRange)markedRange { return [self hasMarkedText] ? NSMakeRange(0,marked.text.length) : NSMakeRange(NSNotFound,0); }
- (NSRange)selectedRange {
    if (![self available]) return NSMakeRange(NSNotFound,0);
    return marked.text.length ? marked.selection : NSMakeRange(0,0);
}
- (NSArray *)validAttributesForMarkedText { return @[]; }
- (NSAttributedString *)attributedSubstringForProposedRange:(NSRange)range actualRange:(NSRangePointer)actual {
    if (actual) *actual = NSMakeRange(NSNotFound,0);
    if (![self available] || ![self localRange:range]) return nil;
    if (actual) *actual = range;
    return [[[NSAttributedString alloc] initWithString:[marked.text substringWithRange:range]] autorelease];
}
- (NSRect)firstRectForCharacterRange:(NSRange)range actualRange:(NSRangePointer)actual {
    if (actual) *actual = NSMakeRange(NSNotFound,0);
    if (![self available] || !anchor || ![self localRange:range]) return NSZeroRect;
    if (actual) *actual = range;
    return anchor();
}
- (NSUInteger)characterIndexForPoint:(NSPoint)point { (void)point; return NSNotFound; }
@end
