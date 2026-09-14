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

// A shared invite link gets a real preview card in Messages, WhatsApp and Slack: the invite page must declare the
// same Open Graph image as the site, name the inviter in the title, and never name the folder (unfurl bots see it).
func TestInvitePageSharePreview(t *testing.T) {
	var buf bytes.Buffer
	d := invitePageData{OS: "mac", Host: "quietport.app", Code: "abc", Inviter: "Alex", Folder: "Tax papers", MacApp: true}
	if err := tmpl.ExecuteTemplate(&buf, "invite.html", d); err != nil {
		t.Fatal(err)
	}
	page := buf.String()
	head := page[:strings.Index(page, "</head>")]
	for _, want := range []string{
		`<meta property="og:title" content="Alex has shared a private folder with you">`,
		`<meta property="og:image" content="https://quietport.app/assets/og-card.png">`,
		`<meta property="og:image:width" content="1200">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<meta property="og:description" content="`,
	} {
		if !strings.Contains(head, want) {
			t.Errorf("invite page head lacks %s", want)
		}
	}
	if strings.Contains(head, "Tax papers") {
		t.Error("the folder name must not appear in the preview tags")
	}
	buf.Reset()
	// the card follows the host the hub answers on, the same as the invite page above: a name compiled into the
	// template points a test hub, or a second deployment, at the live site's image
	if err := tmpl.ExecuteTemplate(&buf, "gone.html", map[string]string{"Operator": "Quietport", "Host": "hub.example"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `<meta property="og:image" content="https://hub.example/assets/og-card.png">`) {
		t.Error("gone page lacks the share card, or names a host of its own")
	}
}

// A link opened from a computer that already has Quietport joins the person that computer belongs to (docs/adr/0019).
// Before this, every link minted a new person and could only be redeemed by a fresh install, which also wiped that
// computer's other folders. The join must: make the existing person a member, hand back the keys the inviter sealed
// under the code, name only the joiner's own other computers (so they can be granted the key), remove the person the
// link minted, keep the invite row as a record of who redeemed it, and refuse the same link a second time.
func TestJoinAddsAnExistingPersonWithoutMintingOne(t *testing.T) {
	db, err := hubdb.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &Hub{db: db, cfg: Config{OperatorName: "Quietport", TailnetIP: "100.64.0.1", AgentAPIPort: "8443", S3Port: "3900", PolicyDir: t.TempDir()}}
	// Sam owns "Trip" and made a link for Pat from the Share page, which minted a placeholder person
	sam, _ := db.PersonAdd(model.Person{Name: "sam-k3q7", DisplayName: "Sam", HSUser: "sam-k3q7"})
	trip, _ := db.CircleCreate(model.Circle{Slug: "trip-9x2a", DisplayName: "Trip", BucketPrefix: "qp-trip-9x2a", OwnerPersonID: sam.ID, InvitePolicy: model.InviteByMembers, SyncMode: model.ModeBidirectional}, "TRIP-AK", "TRIP-SK")
	_ = db.MemberAdd(sam.ID, trip.ID, "member")
	_, _ = db.DeviceAdd(model.Device{PersonID: sam.ID, Hostname: "sams-mac", OS: "darwin", PubKey: "SAMPUB"}, cryptobox.HashToken("sam-token"))
	placeholder, _ := db.PersonAdd(model.Person{Name: "pat-ab3d", DisplayName: "Pat", HSUser: "pat-ab3d"})
	_ = db.MemberAdd(placeholder.ID, trip.ID, "member")
	code := cryptobox.NewInviteCode()
	key := model.CircleKey{Slug: trip.Slug, Generation: 1, Password: "trip-password", Salt: "trip-salt"}
	sealed, _ := cryptobox.SealWithCode(code, []model.CircleKey{key})
	inv, err := db.InviteAdd(hubdb.InviteRow{Invite: model.Invite{InviterName: "Sam", CodeHash: cryptobox.HashToken(code), PersonID: placeholder.ID, CircleIDs: []int64{trip.ID}, ExpiresAt: time.Now().Add(time.Hour), Prefix: code[:6]}, SealedKeys: sealed, MintedPerson: true})
	if err != nil {
		t.Fatal(err)
	}
	// Pat already has Quietport on two computers and a folder of his own
	pat, _ := db.PersonAdd(model.Person{Name: "pat", DisplayName: "Pat Lee", HSUser: "pat"})
	family, _ := db.CircleCreate(model.Circle{Slug: "family", DisplayName: "Family", BucketPrefix: "qp-family", OwnerPersonID: pat.ID}, "FAM-AK", "FAM-SK")
	_ = db.MemberAdd(pat.ID, family.ID, "member")
	mac, _ := db.DeviceAdd(model.Device{PersonID: pat.ID, Hostname: "pats-mac", OS: "darwin", PubKey: "MACPUB"}, cryptobox.HashToken("mac-token"))
	pc, _ := db.DeviceAdd(model.Device{PersonID: pat.ID, Hostname: "pats-pc", OS: "windows", PubKey: "PCPUB"}, cryptobox.HashToken("pc-token"))

	join := func(code string) (*httptest.ResponseRecorder, model.JoinResponse) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/join", strings.NewReader(`{"code":"`+code+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "100.64.0.7:5000"
		h.handleJoin(rec, req, mac)
		var out model.JoinResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}
	rec, out := join(code)
	if rec.Code != 200 {
		t.Fatalf("join: %d %s", rec.Code, rec.Body)
	}
	if len(out.Circles) != 1 || out.Circles[0].Slug != trip.Slug || out.Circles[0].S3AccessKey != "TRIP-AK" || out.Circles[0].S3SecretKey != "TRIP-SK" {
		t.Fatalf("the joined folder with its storage credentials must come back: %+v", out.Circles)
	}
	if out.InviterName != "Sam" {
		t.Errorf("inviter: %q", out.InviterName)
	}
	var keys []model.CircleKey
	if err := cryptobox.OpenWithCode(code, out.SealedKeys, &keys); err != nil || len(keys) != 1 || keys[0] != key {
		t.Fatalf("the keys sealed under the code must come back unchanged: %v %+v", err, keys)
	}
	if len(out.OtherDevices) != 1 || out.OtherDevices[0].ID != pc.ID || out.OtherDevices[0].PubKey != "PCPUB" {
		t.Fatalf("only the joiner's other computer may be listed: %+v", out.OtherDevices)
	}
	for _, leak := range []string{"sams-mac", "SAMPUB", "sam-k3q7", "pats-mac"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("the reply names %q, which the joiner has no business seeing", leak)
		}
	}
	mems, _ := db.MembersOf(trip.ID)
	names := map[string]string{}
	for _, m := range mems {
		names[m.PersonName] = m.Role
	}
	if names["pat"] != "member" {
		t.Errorf("pat is not a member of Trip: %v", names)
	}
	if _, ok := names["pat-ab3d"]; ok {
		t.Errorf("the placeholder is still a member: %v", names)
	}
	if _, err := db.PersonByID(placeholder.ID); err == nil {
		t.Error("the person the link minted still exists")
	}
	if own, _ := db.CirclesOf(pat.ID); len(own) != 2 {
		t.Errorf("pat's own folder must be untouched; memberships: %+v", own)
	}
	after, err := db.InviteByHash(cryptobox.HashToken(code))
	if err != nil || after.ConsumedAt == nil || after.PersonID != pat.ID || after.ID != inv.ID {
		t.Errorf("the invite must survive, consumed, and record who redeemed it: %v %+v", err, after)
	}
	if rec, _ := join(code); rec.Code != 404 {
		t.Errorf("a used link must be refused: %d %s", rec.Code, rec.Body)
	}
	// a link whose folder no longer exists joins nothing and says so, instead of answering 200 with no folder
	gone := cryptobox.NewInviteCode()
	_, _ = db.InviteAdd(hubdb.InviteRow{Invite: model.Invite{InviterName: "Sam", CodeHash: cryptobox.HashToken(gone), PersonID: sam.ID, CircleIDs: []int64{424242}, ExpiresAt: time.Now().Add(time.Hour), Prefix: gone[:6]}, SealedKeys: "x"})
	if rec, out := join(gone); rec.Code != 410 || len(out.Circles) != 0 {
		t.Errorf("a link for a folder that is gone: %d %s", rec.Code, rec.Body)
	}
}

// After a join, the joining computer holds the folder's key and the person's other computers do not. Only a device
// that holds a key can seal it to another (the hub has none), so a member's device may store grants for the
// computers of its own person, and for no one else's (docs/adr/0019). Until now only the owner could store grants,
// as part of a re-key.
func TestMemberDeviceGrantsOnlyToItsOwnComputers(t *testing.T) {
	db, err := hubdb.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &Hub{db: db}
	owner, _ := db.PersonAdd(model.Person{Name: "sam-k3q7", DisplayName: "Sam", HSUser: "sam-k3q7"})
	trip, _ := db.CircleCreate(model.Circle{Slug: "trip-9x2a", DisplayName: "Trip", BucketPrefix: "qp-trip-9x2a", OwnerPersonID: owner.ID}, "AK", "SK")
	_ = db.MemberAdd(owner.ID, trip.ID, "member")
	samDev, _ := db.DeviceAdd(model.Device{PersonID: owner.ID, Hostname: "sams-mac", OS: "darwin", PubKey: "SAMPUB"}, cryptobox.HashToken("sam-token"))
	pat, _ := db.PersonAdd(model.Person{Name: "pat", DisplayName: "Pat Lee", HSUser: "pat"})
	_ = db.MemberAdd(pat.ID, trip.ID, "member")
	mac, _ := db.DeviceAdd(model.Device{PersonID: pat.ID, Hostname: "pats-mac", OS: "darwin", PubKey: "MACPUB"}, cryptobox.HashToken("mac-token"))
	pc, _ := db.DeviceAdd(model.Device{PersonID: pat.ID, Hostname: "pats-pc", OS: "windows", PubKey: "PCPUB"}, cryptobox.HashToken("pc-token"))
	stranger, _ := db.PersonAdd(model.Person{Name: "kim", DisplayName: "Kim", HSUser: "kim"})
	kimDev, _ := db.DeviceAdd(model.Device{PersonID: stranger.ID, Hostname: "kims-pc", OS: "windows", PubKey: "KIMPUB"}, cryptobox.HashToken("kim-token"))

	put := func(as model.Device, grants []model.KeyGrant) *httptest.ResponseRecorder {
		body, _ := json.Marshal(grants)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/circles/"+strconv.FormatInt(trip.ID, 10)+"/grants", bytes.NewReader(body))
		req.SetPathValue("id", strconv.FormatInt(trip.ID, 10))
		req.Header.Set("Content-Type", "application/json")
		h.handleCircleGrantsDevice(rec, req, as)
		return rec
	}
	if rec := put(mac, []model.KeyGrant{{DeviceID: pc.ID, Generation: 1, SealedBox: "box-for-pc"}}); rec.Code != 200 {
		t.Fatalf("a member granting to their own other computer: %d %s", rec.Code, rec.Body)
	}
	if !db.GrantExists(pc.ID, trip.ID, 1) {
		t.Fatal("the grant for the person's other computer was not stored")
	}
	if rec := put(mac, []model.KeyGrant{{DeviceID: samDev.ID, Generation: 1, SealedBox: "box-for-sam"}}); rec.Code != 403 {
		t.Fatalf("a member granting to the owner's computer must be refused: %d %s", rec.Code, rec.Body)
	}
	if rec := put(mac, []model.KeyGrant{{DeviceID: pc.ID, Generation: 1, SealedBox: "ok"}, {DeviceID: samDev.ID, Generation: 1, SealedBox: "not ok"}}); rec.Code != 403 {
		t.Fatalf("one foreign device in the batch must refuse the whole batch: %d %s", rec.Code, rec.Body)
	}
	if db.GrantExists(samDev.ID, trip.ID, 1) {
		t.Fatal("a grant for another person's computer was stored")
	}
	if rec := put(kimDev, []model.KeyGrant{{DeviceID: kimDev.ID, Generation: 1, SealedBox: "box"}}); rec.Code != 403 {
		t.Fatalf("a device outside the folder must be refused: %d %s", rec.Code, rec.Body)
	}
	// the owner's re-key path is unchanged: grants for everyone still in
	if rec := put(samDev, []model.KeyGrant{{DeviceID: mac.ID, Generation: 2, SealedBox: "b1"}, {DeviceID: pc.ID, Generation: 2, SealedBox: "b2"}, {DeviceID: samDev.ID, Generation: 2, SealedBox: "b3"}}); rec.Code != 200 {
		t.Fatalf("owner: %d %s", rec.Code, rec.Body)
	}
}

// The invite page tells someone who already has Quietport the path that exists for them (docs/adr/0019): paste the
// link on the Share page, no download. It must describe that path and not promise the folder appears by itself,
// which is what a dropped earlier change asserted and the code did not do.
func TestInvitePageTellsExistingMembersWhereToPasteTheLink(t *testing.T) {
	for _, os := range []string{"mac", "win", "other"} {
		var buf bytes.Buffer
		d := invitePageData{OS: os, Host: "quietport.app", Code: "abc", Inviter: "Alex", Folder: "Trip", MacApp: true, WinApp: true}
		if err := tmpl.ExecuteTemplate(&buf, "invite.html", d); err != nil {
			t.Fatal(err)
		}
		page := buf.String()
		for _, want := range []string{"Already have Quietport on this computer?", "<b>Share a folder</b>", "Have a link from someone?"} {
			if !strings.Contains(page, want) {
				t.Errorf("%s page lacks %q", os, want)
			}
		}
		if strings.Contains(page, "appears on its own") {
			t.Errorf("%s page promises a mechanism that does not exist", os)
		}
	}
}

// headscale 0.29's `preauthkeys expire` takes the key id and nothing else (its usage: "-i, --id uint   Authkey ID").
// The wrapper also passed --user, so every call failed with "unknown flag: --user", and the failure was discarded
// by `qpctl invite revoke` and offboarding until a join logged it (2026-09-13). A revoked invite's pre-auth key
// therefore stayed usable on the mesh until its own expiry.
func TestPreAuthKeyExpireUsesOnlyTheIDFlag(t *testing.T) {
	got := strings.Join(preAuthKeyExpireArgs(80), " ")
	if got != "preauthkeys expire --id 80" {
		t.Fatalf("args: %q", got)
	}
}
