package main

import (
	"path/filepath"
	"strings"
)

// imageCode reads `hdiutil info` and returns the invite code in the name of the image mounted at mountPoint, the
// volume the installer's own executable runs from. Any other Quietport image that happens to be mounted, such as a
// used invite's image left open from an earlier install, is never consulted. An executable that is
// not on an image, or on an image whose name carries no code, gets no code and the installer asks its questions.
func imageCode(hdiutilInfo, mountPoint string) string {
	if mountPoint == "" {
		return ""
	}
	for _, section := range strings.Split(hdiutilInfo, "\n====") {
		image, mounted := "", false
		for _, line := range strings.Split(section, "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "image-path" {
				image = strings.TrimSpace(v)
			}
			if strings.HasPrefix(line, "/dev/") {
				if f := strings.Split(line, "\t"); len(f) >= 3 && strings.TrimSpace(f[2]) == mountPoint {
					mounted = true
				}
			}
		}
		if mounted {
			return codeRe.FindString(strings.ToLower(filepath.Base(image)))
		}
	}
	return ""
}
