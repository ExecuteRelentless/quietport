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
