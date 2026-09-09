package main

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// originHost reads the download URL macOS records on the bundle (kMDItemWhereFroms), so a rebuilt hub host
// never needs a new notarized installer. Falls back to DefaultHost when the attribute is absent.
func originHost(exe string) string {
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	for _, p := range []string{bundle, filepath.Dir(bundle)} {
		out, err := exec.Command("/usr/bin/xattr", "-p", "com.apple.metadata:kMDItemWhereFroms", p).Output()
		if err != nil {
			continue
		}
		if m := regexp.MustCompile(`https?://([A-Za-z0-9.\-]+)/j/`).FindStringSubmatch(string(out)); m != nil {
			return strings.ToLower(m[1])
		}
	}
	return ""
}

// mountedImageCode: when the app runs from a disk image named Quietport-<code>.dmg, hdiutil knows the image path
// even though the app itself only sees /Volumes/Quietport Installer (or a translocated copy of it).
func mountedImageCode() string {
	out, err := exec.Command("/usr/bin/hdiutil", "info").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "image-path") && strings.Contains(strings.ToLower(line), "quietport") {
			if m := codeRe.FindString(strings.ToLower(line)); m != "" {
				return m
			}
		}
	}
	return ""
}
