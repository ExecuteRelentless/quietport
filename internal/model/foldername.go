package model

import (
	"strings"
	"unicode"
)

// FolderName is the directory a folder's name becomes on a member's computer, or "" when nothing in the name can be
// one (docs/adr/0020). The hub stores names in this form and every computer applies it again, so a name from an older
// hub cannot reach outside the sync root either. Path separators and the other characters Windows refuses become
// "-", control characters are dropped, dots and spaces at either end are dropped (so no name is "." or "..", none is
// hidden, and Windows, which drops trailing ones itself, makes the same directory as macOS), a Windows device name
// gets "Folder " in front, and the result is at most 40 characters, cut between characters.
func FolderName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "") {
		switch {
		case unicode.IsControl(r):
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	name := strings.Trim(b.String(), ". ")
	if r := []rune(name); len(r) > 40 {
		name = strings.TrimRight(string(r[:40]), ". ")
	}
	if name == "" {
		return ""
	}
	stem, _, _ := strings.Cut(name, ".")
	if windowsDevice(strings.ToUpper(strings.TrimRight(stem, " "))) {
		name = "Folder " + name
	}
	return name
}

// windowsDevice: names Windows opens as a device rather than a file, with or without an extension.
func windowsDevice(stem string) bool {
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(stem) >= 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) {
		switch stem[3:] {
		case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
			return true
		}
	}
	return false
}
