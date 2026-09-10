package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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

func TestWriteErrShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, 403, "no")
	if rec.Code != 403 || rec.Header().Get("Content-Type") != "application/json" || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("writeErr: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	_ = http.StatusOK
}
