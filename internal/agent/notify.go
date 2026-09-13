package agent

import (
	"runtime"
)

// Notify is the one permitted interruption (FR-57). Callers gate it to the three listed conditions.
func Notify(title, body string) {
	switch runtime.GOOS {
	case "darwin":
		_ = command("/usr/bin/osascript", "-e", `display notification "`+esc(body)+`" with title "`+esc(title)+`"`).Run()
	case "windows":
		ps := `Add-Type -AssemblyName System.Windows.Forms; $n = New-Object System.Windows.Forms.NotifyIcon; $n.Icon = [System.Drawing.SystemIcons]::Information; $n.Visible = $true; $n.ShowBalloonTip(15000, '` + psq(title) + `', '` + psq(body) + `', [System.Windows.Forms.ToolTipIcon]::Warning); Start-Sleep -Seconds 16; $n.Dispose()`
		cmd := command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", ps)
		_ = cmd.Start()
	default:
		_ = command("notify-send", title, body).Run()
	}
}

func esc(s string) string {
	out := []rune{}
	for _, r := range s {
		if r == '"' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}

func psq(s string) string {
	out := []rune{}
	for _, r := range s {
		if r == '\'' {
			out = append(out, '\'')
		}
		out = append(out, r)
	}
	return string(out)
}
