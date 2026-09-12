#pragma once
#import <AppKit/AppKit.h>

/* One local composition transaction. The owner must discard this client on
 * focus/session loss and create a fresh one for the next transaction. */
@interface VCWTextInputClient : NSObject <NSTextInputClient>
- (instancetype)initWithCurrent:(BOOL (^)(void))current
                         commit:(void (^)(NSString *))commit
                         anchor:(NSRect (^)(void))anchor;
- (void)invalidate;
- (BOOL)isActive;
- (BOOL)takePhysicalCommand;
@end
