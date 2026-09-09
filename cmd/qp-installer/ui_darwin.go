package main

import (
	"os/exec"
	"strings"

	"quietport.app/quietport/internal/agent"
)

// macOS dialogs through osascript: no Cocoa, no cgo, and they run fine from a notarized app bundle.

func dialog(script string) (string, error) {
	out, err := exec.Command("/usr/bin/osascript", "-e", script).Output()
	return strings.TrimSpace(string(out)), err
}

func esc(s string) string { return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) }

func confirm(msg string) bool {
	_, err := dialog(`display dialog "` + esc(msg) + `" with title "Quietport" buttons {"Cancel", "Continue"} default button "Continue" with icon note`)
	return err == nil
}

func askLink() string {
	out, err := dialog(`text returned of (display dialog "Paste your Quietport invite link:" with title "Quietport" default answer "" buttons {"Cancel", "Connect"} default button "Connect")`)
	if err != nil {
		return ""
	}
	return out
}

func done() {
	out, _ := dialog(`button returned of (display dialog "Quietport is connected. Your shared folder is called QPSync and is pinned in the Finder sidebar." with title "Quietport" buttons {"Open Folder", "OK"} default button "Open Folder" with icon note)`)
	if out == "Open Folder" {
		_ = exec.Command("/usr/bin/open", agent.SyncRoot()).Run()
	}
}

func fail(msg, support string) {
	if support == "" {
		support = "the person who invited you"
	}
	_, _ = dialog(`display dialog "Quietport could not be installed: ` + esc(msg) + ` Please contact ` + esc(support) + `." with title "Quietport" buttons {"OK"} default button "OK" with icon stop`)
	exit(1)
}
