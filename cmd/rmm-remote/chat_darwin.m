//go:build darwin && cgo

#import <Cocoa/Cocoa.h>
#import <ApplicationServices/ApplicationServices.h>
#import <CoreGraphics/CoreGraphics.h>
extern void rmmChatSend(char *text);
// How the window names the person here: their account, or "You" (selfLabel).
static NSString *selfLabel;

@interface RMMChatController : NSObject <NSWindowDelegate>
@property(strong) NSWindow *window;
@property(strong) NSTextView *history;
@property(strong) NSTextField *input;
- (void)send:(id)sender;
- (void)append:(NSString *)text role:(int)role;
@end

@implementation RMMChatController
- (instancetype)init {
    self = [super init];
    if (!self) return nil;
    self.window = [[[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, 440, 360)
        styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable
        backing:NSBackingStoreBuffered defer:NO] autorelease];
    self.window.title = @"OpenRMM — Chat with technician";
    self.window.delegate = self;
    self.window.releasedWhenClosed = NO;
    NSView *content = self.window.contentView;
    NSScrollView *scroll = [[[NSScrollView alloc] initWithFrame:NSMakeRect(12, 54, 416, 294)] autorelease];
    scroll.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    scroll.hasVerticalScroller = YES;
    self.history = [[[NSTextView alloc] initWithFrame:scroll.contentView.bounds] autorelease];
    self.history.editable = NO;
    self.history.selectable = YES;
    self.history.richText = YES;
    self.history.verticallyResizable = YES;
    self.history.autoresizingMask = NSViewWidthSizable;
    self.history.textContainer.widthTracksTextView = YES;
    self.history.backgroundColor = [NSColor colorWithSRGBRed:0.10 green:0.10 blue:0.10 alpha:1];
    scroll.documentView = self.history;
    [content addSubview:scroll];
    self.input = [[[NSTextField alloc] initWithFrame:NSMakeRect(12, 12, 328, 30)] autorelease];
    self.input.autoresizingMask = NSViewWidthSizable;
    self.input.placeholderString = @"Digite uma mensagem…";
    self.input.target = self;
    self.input.action = @selector(send:);
    [content addSubview:self.input];
    NSButton *button = [[[NSButton alloc] initWithFrame:NSMakeRect(348, 10, 80, 34)] autorelease];
    button.title = @"Enviar";
    button.bezelStyle = NSBezelStyleRounded;
    button.autoresizingMask = NSViewMinXMargin;
    button.target = self;
    button.action = @selector(send:);
    [content addSubview:button];
    [self.window center];
    [self.window orderFront:nil]; // do not steal focus from the remote desktop
    return self;
}
- (void)dealloc { self.window.delegate = nil; [_window release]; [_history release]; [_input release]; [super dealloc]; }
- (BOOL)windowShouldClose:(NSWindow *)sender { [sender orderOut:nil]; return NO; }
- (void)send:(id)sender {
    NSString *text = [self.input.stringValue stringByTrimmingCharactersInSet:NSCharacterSet.whitespaceAndNewlineCharacterSet];
    if (!text.length) return;
    rmmChatSend((char *)text.UTF8String); // Go copies before returning
    [self append:text role:0];
    self.input.stringValue = @"";
}
// role: 0 the person here (selfLabel), 1 the technician, 2 a guest, whose text
// already starts with their label (chatLine in chat.go).
- (void)append:(NSString *)text role:(int)role {
    NSColor *color = role == 1 ? [NSColor colorWithSRGBRed:0.85 green:0.71 blue:1 alpha:1]
                   : role == 2 ? [NSColor colorWithSRGBRed:0.96 green:0.76 blue:0.42 alpha:1]
                               : [NSColor colorWithSRGBRed:0.64 green:0.91 blue:0.71 alpha:1];
    NSString *line = role == 2 ? [NSString stringWithFormat:@"%@\n", text]
                   : [NSString stringWithFormat:@"%@: %@\n", role == 1 ? @"Technician" : (selfLabel ? selfLabel : @"You"), text];
    NSDictionary *attributes = @{NSForegroundColorAttributeName:color, NSFontAttributeName:[NSFont systemFontOfSize:13]};
    [self.history.textStorage appendAttributedString:[[[NSAttributedString alloc] initWithString:line attributes:attributes] autorelease]];
    [self.history scrollRangeToVisible:NSMakeRange(self.history.string.length, 0)];
    [self.window orderFront:nil];
}
@end

static RMMChatController *chat;
static NSAlert *permissionAlert;

// TCC request functions can be called from any thread, but a LaunchAgent's
// consent sheet is only reliable when the request originates on the AppKit main
// thread that owns the host UI.
int rmmRequestScreenCapture(void) {
    __block BOOL allowed = NO;
    void (^request)(void) = ^{
        allowed = CGPreflightScreenCaptureAccess() || CGRequestScreenCaptureAccess();
    };
    if ([NSThread isMainThread]) request();
    else dispatch_sync(dispatch_get_main_queue(), request);
    return allowed ? 1 : 0;
}

// Either grant lets CGEventPost reach the HID tap: Accessibility, which is what
// Privacy & Security ▸ Accessibility lists and what adding the app there by
// hand gives, or the narrower "post events" one.
int rmmInputTrusted(void) {
    return (AXIsProcessTrusted() || CGPreflightPostEventAccess()) ? 1 : 0;
}

// Asks through Accessibility, not CGRequestPostEventAccess. On macOS 15.8 the
// latter puts its consent sheet up, but the app never appears in the
// Accessibility list the sheet sends the person to, so there was nothing to
// switch on and the app had to be added by hand. AXIsProcessTrustedWithOptions
// with the prompt option adds the app to that list (switched off) and shows
// the same kind of sheet. Only one of the two is asked: macOS drops a second
// prompt while the first is on screen.
int rmmRequestPostEvents(void) {
    __block BOOL allowed = NO;
    void (^request)(void) = ^{
        if (rmmInputTrusted()) {
            allowed = YES;
            return;
        }
        NSDictionary *options = @{(id)kAXTrustedCheckOptionPrompt: @YES};
        allowed = AXIsProcessTrustedWithOptions((CFDictionaryRef)options);
    };
    if ([NSThread isMainThread]) request();
    else dispatch_sync(dispatch_get_main_queue(), request);
    return allowed ? 1 : 0;
}

static void rmmOpenPrivacyPane(NSString *pane) {
    NSString *value = [NSString stringWithFormat:@"x-apple.systempreferences:com.apple.preference.security?%@", pane];
    [[NSWorkspace sharedWorkspace] openURL:[NSURL URLWithString:value]];
}

// macOS normally presents its own consent sheet on the first request. It does
// not promise to present it again after a denial, so follow up with an OpenRMM
// alert that tells the person at the Mac what is missing and opens the exact
// Privacy & Security pane. Recheck first in case the native sheet was accepted.
void rmmPermissionNotice(int screenAllowed, int accessibilityAllowed) {
    if (screenAllowed && accessibilityAllowed) return;
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 2 * NSEC_PER_SEC), dispatch_get_main_queue(), ^{
        BOOL screenMissing = !CGPreflightScreenCaptureAccess();
        // The caller says "allowed" also while the Accessibility request is
        // still to come: macOS takes one prompt at a time, and until the sidecar
        // has asked, the app is not in the Accessibility list to be switched on.
        BOOL accessibilityMissing = !accessibilityAllowed && !rmmInputTrusted();
        if (!screenMissing && !accessibilityMissing) return;

        if (!chat || !chat.window || permissionAlert) return;
        NSAlert *alert = [[NSAlert alloc] init];
        permissionAlert = alert;
        alert.alertStyle = NSAlertStyleWarning;
        alert.messageText = @"OpenRMM Remote needs permission";
        if (screenMissing && accessibilityMissing) {
            alert.informativeText = @"Allow Screen & System Audio Recording to share the display and Accessibility to control the keyboard and mouse. Reopen the remote session after enabling them.";
        } else if (screenMissing) {
            alert.informativeText = @"Allow Screen & System Audio Recording for OpenRMM Remote, then reopen the remote session.";
        } else {
            alert.informativeText = @"Allow Accessibility for OpenRMM Remote to use the keyboard, mouse, paste shortcut and Force Quit shortcut.";
        }

        NSMutableArray *panes = [NSMutableArray array];
        if (screenMissing) {
            [alert addButtonWithTitle:@"Screen Recording…"];
            [panes addObject:@"Privacy_ScreenCapture"];
        }
        if (accessibilityMissing) {
            [alert addButtonWithTitle:@"Accessibility…"];
            [panes addObject:@"Privacy_Accessibility"];
        }
        [alert addButtonWithTitle:@"Later"];
        NSArray *paneChoices = [[panes copy] autorelease];
        [NSApp activateIgnoringOtherApps:YES];
        [chat.window makeKeyAndOrderFront:nil];
        [alert beginSheetModalForWindow:chat.window completionHandler:^(NSModalResponse response) {
            NSInteger index = response - NSAlertFirstButtonReturn;
            if (index >= 0 && index < (NSInteger)paneChoices.count) {
                rmmOpenPrivacyPane([paneChoices objectAtIndex:(NSUInteger)index]);
            }
            [permissionAlert release];
            permissionAlert = nil;
        }];
    });
}

void rmmUIRun(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
        [NSApp run];
    }
}
void rmmUIStop(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [NSApp stop:nil];
        [NSApp postEvent:[NSEvent otherEventWithType:NSEventTypeApplicationDefined location:NSZeroPoint
            modifierFlags:0 timestamp:0 windowNumber:0 context:nil subtype:0 data1:0 data2:0] atStart:YES];
    });
}
void rmmChatOpen(const char *selfName) {
    NSString *label = [[NSString alloc] initWithUTF8String:selfName];
    if (!label.length) { [label release]; label = [@"You" copy]; }
    dispatch_sync(dispatch_get_main_queue(), ^{
        [selfLabel release];
        selfLabel = label;
        chat = [[RMMChatController alloc] init];
    });
}
void rmmChatAppend(const char *text, int role) {
    // Copy before dispatching; the caller frees its C string immediately.
    NSString *value = [[NSString alloc] initWithUTF8String:text];
    dispatch_async(dispatch_get_main_queue(), ^{ if (value) [chat append:value role:role]; });
    [value release];
}
void rmmChatClose(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (permissionAlert && chat.window.attachedSheet) {
            [chat.window endSheet:chat.window.attachedSheet returnCode:NSModalResponseCancel];
        }
        [chat.window orderOut:nil];
        [chat release];
        chat = nil;
    });
}

// The technician's clipboard, pushed into this Mac's pasteboard so a paste in
// the remote session produces what they copied on their own machine. Declaring
// a new owner is required before writing: a pasteboard's contents belong to
// whoever cleared it last, and writing without clearing is ignored.
int rmmSetClipboard(const char *text) {
    NSString *value = [[NSString alloc] initWithUTF8String:text];
    if (!value) return 0;
    __block BOOL written = NO;
    void (^writeClipboard)(void) = ^{
        NSPasteboard *board = [NSPasteboard generalPasteboard];
        [board clearContents];
        written = [board setString:value forType:NSPasteboardTypeString];
    };
    if ([NSThread isMainThread]) writeClipboard();
    else dispatch_sync(dispatch_get_main_queue(), writeClipboard);
    [value release];
    return written ? 1 : 0;
}

// The endpoint's clipboard, read for mirroring to the technician. Returns a
// copy the caller frees; NULL when there is no text (an image, or empty).
char *rmmReadClipboard(void) {
    __block char *out = NULL;
    dispatch_sync(dispatch_get_main_queue(), ^{
        NSString *value = [[NSPasteboard generalPasteboard] stringForType:NSPasteboardTypeString];
        if (!value) return;
        const char *utf8 = [value UTF8String];
        if (utf8) out = strdup(utf8);
    });
    return out;
}
