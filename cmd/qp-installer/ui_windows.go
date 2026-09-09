package main

import (
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"quietport.app/quietport/internal/agent"
)

// Windows dialogs through user32 MessageBoxW (no console window: built with -H windowsgui).

const (
	mbOK          = 0x0
	mbOKCancel    = 0x1
	mbYesNo       = 0x4
	mbIconInfo    = 0x40
	mbIconError   = 0x10
	idOK          = 1
	idYes         = 6
)

func msgbox(text, caption string, flags uint32) int32 {
	t, _ := syscall.UTF16PtrFromString(text)
	c, _ := syscall.UTF16PtrFromString(caption)
	r, _ := windows.MessageBox(0, t, c, flags)
	return r
}

func confirm(msg string) bool {
	return msgbox(msg, "Quietport", mbOKCancel|mbIconInfo) == idOK
}

func askLink() string {
	ps := `Add-Type -AssemblyName Microsoft.VisualBasic; [Microsoft.VisualBasic.Interaction]::InputBox('Paste your Quietport invite link:', 'Quietport', '')`
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func done() {
	r := msgbox("Quietport is connected. Your shared folder is called QPSync and is pinned in File Explorer. Open it now?", "Quietport", mbYesNo|mbIconInfo)
	if r == idYes {
		_ = exec.Command("explorer.exe", agent.SyncRoot()).Start()
	}
}

func fail(msg, support string) {
	if support == "" {
		support = "the person who invited you"
	}
	msgbox("Quietport could not be installed: "+msg+" Please contact "+support+".", "Quietport", mbOK|mbIconError)
	exit(1)
}

func askStartOrJoin() bool {
	// Yes = paste a link (the common case), No = start a new folder
	r := msgbox("Do you have an invite link?\n\nYes: paste the link you were sent.\nNo: start a new folder of your own.", "Quietport", mbYesNo|mbIconInfo)
	return r != idYes
}

func askText(prompt, def string) string {
	ps := `Add-Type -AssemblyName Microsoft.VisualBasic; [Microsoft.VisualBasic.Interaction]::InputBox('` + strings.ReplaceAll(prompt, "'", "''") + `', 'Quietport', '` + strings.ReplaceAll(def, "'", "''") + `')`
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
