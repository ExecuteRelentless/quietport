package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSemverNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.14", "0.1.13", true},
		{"0.1.13", "0.1.14", false},
		{"0.1.13", "0.1.13", false},
		{"0.2.0", "0.1.99", true},
		{"1.0.0", "0.9.9", true},
		{"v0.1.14", "0.1.13", true},
		{"0.1.1", "dev", true}, // a dev build takes any release
	}
	for _, c := range cases {
		if got := semverNewer(c.a, c.b); got != c.want {
			t.Errorf("semverNewer(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

// The marker keeps bisync away from its empty-listing abort (docs/adr/0004): it is created in an existing folder,
// left alone when present, and never created for a folder that does not exist (that would resurrect a removed one).
func TestEnsureMarker(t *testing.T) {
	dir := t.TempDir()
	ensureMarker(dir)
	p := filepath.Join(dir, MarkerFile)
	st, err := os.Stat(p)
	if err != nil || st.Size() == 0 {
		t.Fatalf("marker not created: %v", err)
	}
	_ = os.WriteFile(p, []byte("kept"), 0o644)
	ensureMarker(dir)
	b, _ := os.ReadFile(p)
	if string(b) != "kept" {
		t.Fatal("existing marker was overwritten")
	}
	gone := filepath.Join(dir, "removed-folder")
	ensureMarker(gone)
	if _, err := os.Stat(gone); err == nil {
		t.Fatal("marker created a folder that did not exist")
	}
}

// The default excludes drop ".qp-*"; the marker must not fall under any of them or it never reaches the hub.
func TestMarkerIsNotExcluded(t *testing.T) {
	for _, pat := range defaultExcludes {
		if ok, _ := filepath.Match(pat, MarkerFile); ok {
			t.Fatalf("exclude %q matches the marker %q", pat, MarkerFile)
		}
	}
}
