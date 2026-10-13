#import <AppKit/AppKit.h>
#import <ApplicationServices/ApplicationServices.h>

int br_activate(int pid) {
    __block BOOL result=NO;
    void (^activate)(void)=^{
        @autoreleasepool {
            NSRunningApplication *app=[NSRunningApplication runningApplicationWithProcessIdentifier:pid];
            if (!app) return;
            if (@available(macOS 14.0, *)) {
                [NSApp yieldActivationToApplication:app];
                result=[app activateFromApplication:NSRunningApplication.currentApplication options:NSApplicationActivateAllWindows];
            } else {
                result=[app activateWithOptions:NSApplicationActivateIgnoringOtherApps];
            }
            // Open is an explicit user action. Target the exact process when
            // cooperative activation is declined (including background clicks).
            if (!result) {
                ProcessSerialNumber psn;
                if (GetProcessForPID(pid,&psn)==noErr)
                    result=SetFrontProcessWithOptions(&psn,kSetFrontProcessCausedByUser)==noErr;
            }
        }
    };
    if (NSThread.isMainThread) activate();
    else dispatch_sync(dispatch_get_main_queue(),activate);
    return result;
}
int br_terminate(int pid) {
    @autoreleasepool {
        NSRunningApplication *app=[NSRunningApplication runningApplicationWithProcessIdentifier:pid];
        return app && [app terminate];
    }
}
