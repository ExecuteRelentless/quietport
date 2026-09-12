package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// attachConsole gives this GUI-subsystem program the console of the process that ran it, so status, install,
// version and the usage line still print in the terminal someone typed them into. Windows gives a GUI program no
// standard handles of its own, so they are opened on CONOUT$ and set on the process too, for the child processes
// install starts. A caller that redirected our output to a pipe or a file (PowerShell's "| Out-Host",
// "> log.txt", cmd's shim) does hand us valid handles, and those are left exactly as they are.
//
// x/sys/windows has no binding for AttachConsole, so it goes through kernel32 the same way the rest of this file's
// calls do. ATTACH_PARENT_PROCESS fails when there is no console to attach to at all (started from Explorer, or by
// the Scheduled Task); that is not an error, there is simply nobody watching.
func attachConsole() {
	if stdoutIsUsable() {
		return
	}
	attach := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")
	if attach.Find() != nil {
		return
	}
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	if ok, _, _ := attach.Call(attachParentProcess); ok == 0 {
		return
	}
	h, err := windows.CreateFile(windows.StringToUTF16Ptr("CONOUT$"),
		windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return
	}
	f := os.NewFile(uintptr(h), "CONOUT$")
	os.Stdout, os.Stderr = f, f
	_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, h)
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, h)
}

// stdoutIsUsable reports whether the caller already gave this process somewhere to write. A GUI program launched
// with no redirection has no standard handle at all (0), and a stale one answers no file type.
func stdoutIsUsable() bool {
	h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil || h == 0 || h == windows.InvalidHandle {
		return false
	}
	t, err := windows.GetFileType(h)
	return err == nil && t != windows.FILE_TYPE_UNKNOWN
}
