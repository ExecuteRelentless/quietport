package main

import (
	"os"
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

// askStartOrJoin: true = start a new folder, false = paste a link.
func askStartOrJoin() bool {
	out, err := dialog(`button returned of (display dialog "Do you have an invite link?" with title "Quietport" buttons {"Start a new folder", "Paste a link"} default button "Paste a link" with icon note)`)
	if err != nil {
		os.Exit(0)
	}
	return out == "Start a new folder"
}

func askText(prompt, def string) string {
	out, err := dialog(`text returned of (display dialog "` + esc(prompt) + `" with title "Quietport" default answer "` + esc(def) + `" buttons {"Cancel", "Continue"} default button "Continue")`)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// askInstalled: Quietport is already here. remove | join | cancel
func askInstalled() string {
	out, err := dialog(`button returned of (display dialog "Quietport is already set up on this computer." with title "Quietport" buttons {"Remove Quietport", "Use a new link", "Cancel"} default button "Cancel" with icon note)`)
	if err != nil {
		return "cancel"
	}
	switch out {
	case "Remove Quietport":
		return "remove"
	case "Use a new link":
		return "join"
	}
	return "cancel"
}

func askKeepFolder() bool {
	out, err := dialog(`button returned of (display dialog "Keep your files in the QPSync folder? They will stop updating." with title "Quietport" buttons {"Delete the folder too", "Keep my files"} default button "Keep my files" with icon caution)`)
	if err != nil {
		return true
	}
	return out != "Delete the folder too"
}

func removed(keep bool) {
	msg := "Quietport has been removed. Your files are still in the QPSync folder."
	if !keep {
		msg = "Quietport and the QPSync folder have been removed."
	}
	_, _ = dialog(`display dialog "` + esc(msg) + `" with title "Quietport" buttons {"OK"} default button "OK" with icon note`)
}
