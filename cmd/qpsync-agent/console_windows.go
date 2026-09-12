package main

import "golang.org/x/sys/windows"

// hideConsole hides the console window Windows gives this program when the Scheduled Task starts it with an
// interactive token. The process keeps its console, so nothing that writes to stdout breaks; only the window goes.
// A member who closes that window stops their own syncing, and the task's only trigger is logon, so nothing starts
// the agent again. x/sys/windows carries no binding for either call, so both go through the system DLLs.
func hideConsole() {
	getConsoleWindow := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")
	showWindow := windows.NewLazySystemDLL("user32.dll").NewProc("ShowWindow")
	if getConsoleWindow.Find() != nil || showWindow.Find() != nil {
		return
	}
	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd == 0 {
		return // no console of its own: nothing to hide
	}
	const swHide = 0
	_, _, _ = showWindow.Call(hwnd, swHide)
}
