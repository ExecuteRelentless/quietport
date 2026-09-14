package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
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
// even though the app itself only sees /Volumes/Quietport Installer. The code is read from that image alone,
// found by the volume the executable is on.
func mountedImageCode() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	out, err := exec.Command("/usr/bin/hdiutil", "info").Output()
	if err != nil {
		return ""
	}
	return imageCode(string(out), volumeOf(exe))
}

// volumeOf returns the mount point of the file system holding path. A path under a Gatekeeper translocation is
// a nullfs mount whose source is a directory on the real volume; that one hop is followed so the executable's
// image is still found. (Not seen on macOS 26 with the notarized image, where the app runs from /Volumes directly.)
func volumeOf(path string) string {
	for hop := 0; hop < 2; hop++ {
		var st syscall.Statfs_t
		if err := syscall.Statfs(path, &st); err != nil {
			return ""
		}
		from := cstr(st.Mntfromname[:])
		if strings.HasPrefix(from, "/dev/") || from == "" {
			return cstr(st.Mntonname[:])
		}
		path = from
	}
	return ""
}

func cstr(b []int8) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}
