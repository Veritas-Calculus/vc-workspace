#pragma once
#import "VCWTextInputClient.h"

@protocol VCWInputContext <NSObject>
- (BOOL)handleEvent:(NSEvent *)event;
- (void)activate;
- (void)deactivate;
- (void)discardMarkedText;
@end

/* Main-thread event owner. Return YES only when the physical event must not
 * also reach the RDP scancode path. cancel must run on focus/session loss. */
@interface VCWTextInputHost : NSObject
- (instancetype)initWithCurrent:(BOOL (^)(void))current commit:(void (^)(NSString *))commit
                         anchor:(NSRect (^)(void))anchor
                        factory:(id<VCWInputContext> (^)(VCWTextInputClient *))factory;
- (BOOL)handleEvent:(NSEvent *)event localIME:(BOOL)localIME;
- (void)cancel;
@end
