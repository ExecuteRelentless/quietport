package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The installer finds its own disk image by the volume that holds its executable, so volumeOf must name the mount
// point a path lives on, and nothing for a path that does not exist.
func TestVolumeOfNamesTheMountAPathLivesOn(t *testing.T) {
	if got := volumeOf("/"); got != "/" {
		t.Errorf("volume of /: %q", got)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	vol := volumeOf(exe)
	if vol == "" {
		t.Fatalf("no volume for %s", exe)
	}
	// macOS reaches some places through firmlinks (/var is /System/Volumes/Data/private/var), so the path need not
	// start with its mount point; the file system itself must be the same one
	var onExe, onVol syscall.Statfs_t
	if syscall.Statfs(exe, &onExe) != nil || syscall.Statfs(vol, &onVol) != nil || onExe.Fsid != onVol.Fsid {
		t.Errorf("%s is not on %s", exe, vol)
	}
	if vol == "/" {
		t.Errorf("%s is on the data volume, not on /", exe)
	}
	if got := volumeOf(filepath.Join(t.TempDir(), "no", "such", "path")); got != "" {
		t.Errorf("a missing path has a volume: %q", got)
	}
}
