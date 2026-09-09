// qp-sidebar: pin a folder to the Finder sidebar Favourites (FR-15). Uses LSSharedFileList, deprecated but functional,
// and the only non-admin route. Usage: qp-sidebar add|remove /path/to/folder
#import <Foundation/Foundation.h>
#import <CoreServices/CoreServices.h>
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
int main(int argc, const char *argv[]) {
    @autoreleasepool {
        if (argc < 3) { fprintf(stderr, "usage: qp-sidebar add|remove <path>\n"); return 2; }
        NSString *op = [NSString stringWithUTF8String:argv[1]];
        NSURL *url = [NSURL fileURLWithPath:[NSString stringWithUTF8String:argv[2]]];
        LSSharedFileListRef list = LSSharedFileListCreate(NULL, kLSSharedFileListFavoriteItems, NULL);
        if (!list) { fprintf(stderr, "no favourites list\n"); return 1; }
        UInt32 seed;
        NSArray *items = CFBridgingRelease(LSSharedFileListCopySnapshot(list, &seed));
        for (id item in items) {
            LSSharedFileListItemRef ref = (__bridge LSSharedFileListItemRef)item;
            CFURLRef itemURL = LSSharedFileListItemCopyResolvedURL(ref, 0, NULL);
            if (itemURL) {
                BOOL same = [[(__bridge NSURL *)itemURL path] isEqualToString:[url path]];
                CFRelease(itemURL);
                if (same) {
                    if ([op isEqualToString:@"remove"]) { LSSharedFileListItemRemove(list, ref); CFRelease(list); return 0; }
                    CFRelease(list); return 0; // already pinned
                }
            }
        }
        if ([op isEqualToString:@"add"]) {
            LSSharedFileListItemRef added = LSSharedFileListInsertItemURL(list, kLSSharedFileListItemLast, NULL, NULL, (__bridge CFURLRef)url, NULL, NULL);
            if (added) CFRelease(added); else { CFRelease(list); fprintf(stderr, "insert failed\n"); return 1; }
        }
        CFRelease(list);
        return 0;
    }
}
