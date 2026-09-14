package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// On Windows the marker is a hidden file (docs/adr/0004). A marker that arrives from the hub in a sync is written
// by rclone as an ordinary file, so the agent hides whatever marker it finds, without touching its time: a changed
// time is a change bisync counts (docs/adr/0022).
func TestAMarkerFromTheHubIsHiddenOnWindows(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, MarkerFile)
	if err := os.WriteFile(p, []byte(markerText), 0o644); err != nil {
		t.Fatal(err)
	}
	synced := time.Date(2026, 9, 13, 4, 36, 41, 0, time.UTC)
	_ = os.Chtimes(p, synced, synced)
	ensureMarker(dir)
	u, _ := windows.UTF16PtrFromString(p)
	attrs, err := windows.GetFileAttributes(u)
	if err != nil || attrs&windows.FILE_ATTRIBUTE_HIDDEN == 0 {
		t.Errorf("the marker is not hidden: attributes %#x, %v", attrs, err)
	}
	if st, _ := os.Stat(p); !st.ModTime().Equal(synced) {
		t.Errorf("hiding the marker changed its time: %v", st.ModTime())
	}
}
