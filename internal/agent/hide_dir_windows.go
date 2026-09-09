package agent

import "golang.org/x/sys/windows"

func hideDir(p string) {
	if u, err := windows.UTF16PtrFromString(p); err == nil {
		_ = windows.SetFileAttributes(u, windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_ATTRIBUTE_DIRECTORY)
	}
}
