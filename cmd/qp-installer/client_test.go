package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The Mac installer no longer carries the client (docs/adr/0027): the disk image was 128 MB because every program
// in it was built for both chips. It downloads the bundle for the chip it runs on, and installs it only when the
// bytes are the ones its notarized binary was built with, so a host that serves anything else, a look-alike link's
// host for one, gets nothing installed and the installer never runs code the release did not ship.
func TestTheMacInstallerInstallsOnlyTheBundleItWasBuiltFor(t *testing.T) {
	shipped := tarGz(t, map[string]string{"qpsync-agent": "the 0.1.29 agent", "VERSION": "0.1.29\n"})
	served := map[string][]byte{"/dl/quietport-darwin-arm64-0.1.29.tar.gz": shipped}
	var asked []string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		b, ok := served[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "", fileTime, bytes.NewReader(b))
	}))
	defer hub.Close()
	defer func(v, arm, x string) { Version, clientSumArm64, clientSumAmd64 = v, arm, x }(Version, clientSumArm64, clientSumAmd64)
	sum := sha256.Sum256(shipped)
	Version, clientSumArm64, clientSumAmd64 = "0.1.29", hex.EncodeToString(sum[:]), "a different chip's client"

	app := t.TempDir()
	if err := downloadClient("darwin", "arm64", hub.URL, app); err != nil {
		t.Fatalf("the shipped bundle was refused: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(app, "qpsync-agent")); string(got) != "the 0.1.29 agent" {
		t.Fatalf("installed agent = %q", got)
	}

	served["/dl/quietport-darwin-arm64-0.1.29.tar.gz"] = tarGz(t, map[string]string{"qpsync-agent": "someone else's program"})
	other := t.TempDir()
	if err := downloadClient("darwin", "arm64", hub.URL, other); err == nil {
		t.Fatal("a bundle the installer was not built with was accepted")
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Fatalf("a refused bundle left %d files in the application folder", len(entries))
	}

	served["/dl/quietport-darwin-arm64-0.1.29.tar.gz"] = shipped
	clientSumArm64 = ""
	unpinned := t.TempDir()
	if err := downloadClient("darwin", "arm64", hub.URL, unpinned); err == nil {
		t.Fatal("an installer built without a pin installed what it downloaded")
	}
	if entries, _ := os.ReadDir(unpinned); len(entries) != 0 {
		t.Fatalf("an installer without a pin left %d files in the application folder", len(entries))
	}

	asked = nil
	served["/dl/quietport-darwin-amd64-0.1.29.tar.gz"] = shipped
	_ = downloadClient("darwin", "amd64", hub.URL, t.TempDir())
	if len(asked) == 0 || asked[0] != "/dl/quietport-darwin-amd64-0.1.29.tar.gz" {
		t.Fatalf("an Intel Mac asked for %q, want its own chip's bundle of this installer's version", asked)
	}
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "./" + name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

var fileTime = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

// Opening a used or expired link cost nothing when the client came inside the installer. Now that a Mac downloads
// the client first, a dead link is asked about before the download (the invite page answers 404 without using the
// link up), so nobody waits for 50 MB to learn the link no longer works.
func TestADeadLinkIsKnownBeforeTheClientIsDownloaded(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/j/usedusedusedusedusedusedus" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("<html>someone has shared a folder with you</html>"))
	}))
	defer hub.Close()
	if linkLive(hub.URL, "usedusedusedusedusedusedus") {
		t.Fatal("a link the hub answers 404 for was taken as live")
	}
	if !linkLive(hub.URL, "livelivelivelivelivelivel2") {
		t.Fatal("a live link was taken as dead")
	}
}
