package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/hubdb"
	"quietport.app/quietport/internal/model"
)

func TestPersonLabelPrefersTypedName(t *testing.T) {
	if got := personLabel(model.Person{Name: "sam-k3q7", DisplayName: "Sam Lee"}); got != "Sam Lee" {
		t.Fatalf("got %q", got)
	}
	if got := personLabel(model.Person{Name: "sam-k3q7"}); got != "sam" {
		t.Fatalf("suffix not stripped: %q", got)
	}
	if got := personLabel(model.Person{Name: "sam"}); got != "sam" {
		t.Fatalf("operator-made name changed: %q", got)
	}
	if got := displayName("austin-lee-2b7q"); got != "austin-lee" {
		t.Fatalf("got %q", got)
	}
}

func TestDetectOS(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) Safari":    "mac",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome":       "win",
		"Mozilla/5.0 (X11; CrOS x86_64) Chrome":                  "other",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)": "mac", // the page then offers the Mac download; phones are not supported
	}
	for ua, want := range cases {
		if got := detectOS(ua); got != want {
			t.Errorf("detectOS(%q)=%q want %q", ua, got, want)
		}
	}
}

func TestHubSemverNewer(t *testing.T) {
	if !semverNewer("0.1.14", "0.1.13") || semverNewer("0.1.13", "0.1.13") || semverNewer("0.1.9", "0.1.10") {
		t.Fatal("semver comparison wrong")
	}
}

// Members start folders from their own device (docs/adr/0003). The handler must refuse an empty name, an inactive
// person and a hub where the operator turned member_circles off, before it ever touches storage.
func TestDeviceCircleCreateGates(t *testing.T) {
	db, err := hubdb.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &Hub{db: db}
	p, _ := db.PersonAdd(model.Person{Name: "sam-k3q7", DisplayName: "Sam", HSUser: "sam-k3q7"})
	dev := model.Device{ID: 7, PersonID: p.ID}
	call := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/circles", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h.handleDeviceCircleCreate(rec, req, dev)
		return rec
	}
	if rec := call(`{"name":"   "}`); rec.Code != 400 {
		t.Fatalf("empty name: %d %s", rec.Code, rec.Body)
	}
	// valid request on a hub with no storage configured gets past every gate and fails only at storage
	if rec := call(`{"name":"Trip"}`); rec.Code != 500 || !strings.Contains(rec.Body.String(), "storage") {
		t.Fatalf("expected the storage error after the gates: %d %s", rec.Code, rec.Body)
	}
	_ = db.SetSetting("member_circles", "0")
	if rec := call(`{"name":"Trip"}`); rec.Code != 403 {
		t.Fatalf("member_circles=0: %d %s", rec.Code, rec.Body)
	}
	_ = db.SetSetting("member_circles", "1")
	_ = db.PersonSetStatus(p.ID, model.StatusOffboarded)
	if rec := call(`{"name":"Trip"}`); rec.Code != 403 {
		t.Fatalf("offboarded person: %d %s", rec.Code, rec.Body)
	}
}

// The Windows installer is downloaded inside a zip (docs/adr/0008). The exe inside must keep the name that carries the
// invite code, its bytes must be the published exe, and Content-Length must be exact so the browser shows progress.
func TestWindowsInstallerZip(t *testing.T) {
	dir := t.TempDir()
	exe := bytes.Repeat([]byte("MZ quietport installer "), 4096)
	if err := os.WriteFile(filepath.Join(dir, "quietport-installer-windows-amd64.exe"), exe, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := hubdb.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, _ := db.PersonAdd(model.Person{Name: "sam-k3q7", HSUser: "sam-k3q7"})
	code := "abcdefghijklmnopqrstuvwxyz"
	if _, err := db.InviteAdd(hubdb.InviteRow{Invite: model.Invite{CodeHash: cryptobox.HashToken(code), Prefix: code[:6], PersonID: p.ID, ExpiresAt: time.Now().Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	h := &Hub{db: db, cfg: Config{ReleasesDir: dir}, site: fstest.MapFS{}}
	mux := http.NewServeMux()
	h.routesPublic(mux)

	for path, inner := range map[string]string{
		"/j/" + code + "/Quietport-" + code + ".zip": "Quietport-" + code + ".exe",
		"/dl/Quietport-Windows.zip":                  "Quietport.exe",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" {
			t.Fatalf("%s: %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		body := rec.Body.Bytes()
		if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(body)) {
			t.Fatalf("%s: Content-Length %s, body %d bytes", path, cl, len(body))
		}
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(zr.File) != 1 || zr.File[0].Name != inner {
			t.Fatalf("%s: want one entry %q, got %d", path, inner, len(zr.File))
		}
		if zr.File[0].Modified.Year() < 2020 {
			t.Fatalf("%s: entry date %v", path, zr.File[0].Modified)
		}
		rc, err := zr.File[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(rc) // the reader checks the CRC at EOF
		if err != nil || !bytes.Equal(got, exe) {
			t.Fatalf("%s: inner exe differs from the published one (%v)", path, err)
		}

		head := httptest.NewRecorder()
		mux.ServeHTTP(head, httptest.NewRequest("HEAD", path, nil))
		if head.Header().Get("Content-Length") != strconv.Itoa(len(body)) || head.Body.Len() != 0 {
			t.Fatalf("%s HEAD: Content-Length %q, body %d bytes", path, head.Header().Get("Content-Length"), head.Body.Len())
		}
	}
}

// Search engines and AI crawlers are let in (docs/adr/0009). Before the site had its own robots.txt the request fell
// through to Headscale, whose robots.txt disallows everything, so quietport.app was invisible to all of them.
func TestCrawlerFiles(t *testing.T) {
	site, err := fs.Sub(webFS, "web/site")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Hub{site: site}).routesPublic(mux)
	get := func(path string) string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}

	robots := get("/robots.txt")
	for _, want := range []string{"User-agent: *", "User-agent: GPTBot", "User-agent: OAI-SearchBot", "User-agent: ClaudeBot",
		"User-agent: PerplexityBot", "User-agent: Google-Extended", "User-agent: CCBot", "Sitemap: https://quietport.app/sitemap.xml"} {
		if !strings.Contains(robots, want) {
			t.Errorf("robots.txt lacks %q", want)
		}
	}
	for _, line := range strings.Split(robots, "\n") {
		if strings.TrimSpace(line) == "Disallow: /" {
			t.Fatal("robots.txt disallows the whole site")
		}
	}
	// a crawler that matches a named group ignores the * group, so every group must repeat the private paths
	if groups, private := strings.Count(robots, "Allow: /\n"), strings.Count(robots, "Disallow: /j/\n"); groups < 2 || private != groups {
		t.Errorf("robots.txt: %d groups, %d keep /j/ out", groups, private)
	}

	sitemap := get("/sitemap.xml")
	for _, loc := range []string{"https://quietport.app/", "https://quietport.app/privacy"} {
		if !strings.Contains(sitemap, "<loc>"+loc+"</loc>") {
			t.Errorf("sitemap.xml lacks %s", loc)
		}
	}
	if llms := get("/llms.txt"); !strings.HasPrefix(llms, "# Quietport\n") || !strings.Contains(llms, "\n> ") ||
		!strings.Contains(llms, "https://github.com/ExecuteRelentless/quietport") {
		t.Error("llms.txt is not in the llmstxt.org shape (title, summary, links)")
	}
	if key := strings.TrimSpace(get("/a89e84bee4b5d7d0521a8993f76f94e1.txt")); key != "a89e84bee4b5d7d0521a8993f76f94e1" {
		t.Errorf("IndexNow key file holds %q", key)
	}
	get("/assets/og-card.png")

	home := get("/")
	for _, want := range []string{`<link rel="canonical" href="https://quietport.app/">`,
		`<meta property="og:image" content="https://quietport.app/assets/og-card.png">`, `<meta name="twitter:card" content="summary_large_image">`} {
		if !strings.Contains(home, want) {
			t.Errorf("home page lacks %s", want)
		}
	}
	_, ld, ok := strings.Cut(home, `<script type="application/ld+json">`)
	ld, _, _ = strings.Cut(ld, "</script>")
	var app struct{ Name, URL string }
	if err := json.Unmarshal([]byte(ld), &app); !ok || err != nil || app.Name != "Quietport" || app.URL != "https://quietport.app/" {
		t.Errorf("home page JSON-LD: found=%v err=%v %+v", ok, err, app)
	}
}

func TestWriteErrShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, 403, "no")
	if rec.Code != 403 || rec.Header().Get("Content-Type") != "application/json" || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("writeErr: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	_ = http.StatusOK
}
