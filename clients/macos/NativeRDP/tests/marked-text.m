#define VCW_MARKED_TEXT_IMPLEMENTATION
#import "../VCWMarkedText.h"
#include <assert.h>
#include <stdio.h>

int main(void) {
    @autoreleasepool {
        VCWMarkedText *state = [VCWMarkedText new];
        NSUInteger first = [state activate];
        NSMutableString *preedit = [NSMutableString stringWithString:@"zhong"];
        assert([state update:preedit selection:NSMakeRange(5,0) generation:first]);
        [preedit appendString:@"changed"];
        assert([state.text isEqualToString:@"zhong"]);
        assert([state update:@"zhongwen" selection:NSMakeRange(0,8) generation:first]);
        assert([[state commit:@"中文😀" generation:first] isEqualToString:@"中文😀"]);
        assert(![state commit:@"duplicate" generation:first]);
        assert(!state.text.length && state.selection.location == NSNotFound);
        NSUInteger second = [state activate];
        assert(second != first);
        assert([state update:@"new" selection:NSMakeRange(3,0) generation:second]);
        assert(![state update:@"late" selection:NSMakeRange(0,0) generation:first]);
        assert(![state commit:@"late" generation:first]);
        assert([state.text isEqualToString:@"new"]);
        [state cancel];
        assert(![state commit:@"wrong focus" generation:second]);
        NSUInteger third = [state activate];
        assert(![state update:@"abc" selection:NSMakeRange(NSUIntegerMax,2) generation:third]);
        assert(![state commit:@"after invalid range" generation:third]);
        NSUInteger fourth = [state activate];
        assert(![state commit:@"a\nb" generation:fourth]);
        NSUInteger fifth = [state activate];
        NSString *large = [@"a" stringByPaddingToLength:4097 withString:@"a" startingAtIndex:0];
        assert(![state update:large selection:NSMakeRange(0,0) generation:fifth]);
        NSUInteger sixth = [state activate];
        assert([state update:@"" selection:NSMakeRange(0,0) generation:sixth]);
        assert(![state commit:@"" generation:sixth]);
        NSUInteger seventh = [state activate];
        assert(![state update:@"😀" selection:NSMakeRange(1,0) generation:seventh]);
        NSUInteger eighth = [state activate];
        assert(![state update:@"😀" selection:NSMakeRange(0,1) generation:eighth]);
        NSUInteger ninth = [state activate];
        assert([state update:@"😀" selection:NSMakeRange(0,2) generation:ninth]);
        unichar broken[] = {0xd800};
        assert(![state commit:[NSString stringWithCharacters:broken length:1] generation:ninth]);
        [state release];
        puts("marked text: immutable preedit, exact-once commit, stale focus and bounds passed");
    }
}
