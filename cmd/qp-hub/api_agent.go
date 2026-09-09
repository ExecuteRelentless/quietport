package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/hubdb"
	"quietport.app/quietport/internal/model"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 4<<20)
	return json.NewDecoder(r.Body).Decode(v)
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func (h *Hub) routesAgent(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/enrol", h.handleEnrol)
	mux.HandleFunc("POST /v1/heartbeat", h.withDevice(h.handleHeartbeat))
	mux.HandleFunc("GET /v1/update/{os}/{arch}", h.withDevice(h.handleUpdateDownload))
	mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"ok": true, "time": time.Now().UTC()}) })
}

func (h *Hub) withDevice(next func(http.ResponseWriter, *http.Request, model.Device)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" {
			writeErr(w, 401, "missing device token")
			return
		}
		d, err := h.db.DeviceByToken(cryptobox.HashToken(tok))
		if err != nil {
			writeErr(w, 401, "unknown device")
			return
		}
		if d.Status == model.StatusRevoked {
			writeErr(w, 403, "device revoked")
			return
		}
		next(w, r, d)
	}
}

// handleEnrol: the installer has already fetched the payload (consuming the invite). Now the device is on the mesh
// and registers itself. We verify the invite hash, that the mesh node with this tailnet IP exists under the person's
// headscale user, and issue a device token.
func (h *Hub) handleEnrol(w http.ResponseWriter, r *http.Request) {
	var req model.EnrolRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	inv, err := h.db.InviteByHash(cryptobox.HashToken(req.InviteCode))
	if err != nil || inv.Revoked || inv.ConsumedAt == nil || time.Since(*inv.ConsumedAt) > 2*time.Hour {
		writeErr(w, 403, "invite is not valid for enrolment")
		return
	}
	person, err := h.db.PersonByID(inv.PersonID)
	if err != nil || person.Status != model.StatusActive {
		writeErr(w, 403, "person not active")
		return
	}
	// tie the enrolment to the mesh identity: the request must arrive from the tailnet IP it claims
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if req.TailnetIP == "" || (remoteIP != req.TailnetIP && remoteIP != "127.0.0.1") {
		writeErr(w, 403, "enrolment must come over the mesh")
		return
	}
	var hsNodeID int64
	if n, ok := h.hs.NodeByIP(req.TailnetIP); ok {
		if n.User.Name != person.HSUser {
			writeErr(w, 403, "mesh node belongs to a different person")
			return
		}
		hsNodeID, _ = n.ID.Int64()
	} else {
		writeErr(w, 403, "mesh node not found for this address")
		return
	}
	// one device row per (person, mesh node): a reinstall on the same machine reuses the row
	tok := cryptobox.NewToken()
	devs, _ := h.db.Devices(person.ID)
	var dev model.Device
	for _, d := range devs {
		if d.HSNodeID == hsNodeID && d.Status != model.StatusRevoked {
			dev = d
		}
	}
	if dev.ID == 0 {
		dev, err = h.db.DeviceAdd(model.Device{PersonID: person.ID, Hostname: req.Hostname, OS: req.OS, Arch: req.Arch, AgentVersion: req.AgentVersion, HSNodeID: hsNodeID, TailnetIP: req.TailnetIP, PubKey: req.PubKey}, cryptobox.HashToken(tok))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	} else {
		// re-enrolment: new token, new pubkey; old grants are useless with the new key
		_, _ = h.db.Exec(`UPDATE device SET token_hash=?, pubkey=?, hostname=?, os=?, arch=?, agent_version=?, tailnet_ip=?, status='active' WHERE id=?`,
			cryptobox.HashToken(tok), req.PubKey, req.Hostname, req.OS, req.Arch, req.AgentVersion, req.TailnetIP, dev.ID)
		_ = h.db.GrantsDeleteForDevice(dev.ID)
		dev, _ = h.db.DeviceByID(dev.ID)
	}
	_ = h.db.DeviceTouch(dev.ID, req.AgentVersion, req.TailnetIP, hsNodeID)
	h.db.Event("device.enrolled", person.ID, dev.ID, fmt.Sprintf("%s (%s/%s) %s", req.Hostname, req.OS, req.Arch, req.TailnetIP))
	h.db.Audit("system", "device.enrol", person.Name, req.Hostname)
	// the invite's preauth key was single-use; nothing to revoke. Circle keys arrived inside the payload.
	cfg, err := h.configBundle(person, dev)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, model.EnrolResponse{DeviceID: dev.ID, DeviceToken: tok, Config: cfg})
}

func (h *Hub) handleHeartbeat(w http.ResponseWriter, r *http.Request, dev model.Device) {
	var hb model.Heartbeat
	if err := readJSON(r, &hb); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	hb.DeviceID = dev.ID
	hb.Timestamp = time.Now().UTC()
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	_ = h.db.HeartbeatAdd(dev.ID, hb)
	_ = h.db.DeviceTouch(dev.ID, hb.AgentVersion, remoteIP, 0)
	if hb.ClientTime.IsZero() == false && absDur(time.Since(hb.ClientTime)) > 5*time.Minute {
		h.db.Event("clock_skew", dev.PersonID, dev.ID, fmt.Sprintf("client clock off by %s", time.Since(hb.ClientTime).Round(time.Second)))
	}
	for _, c := range hb.Conditions {
		h.db.Event("condition:"+c, dev.PersonID, dev.ID, "")
	}
	person, err := h.db.PersonByID(dev.PersonID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	cfg, err := h.configBundle(person, dev)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, cfg)
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// configBundle: everything a device needs except key material (FR-63). Keys arrive as sealed grants.
func (h *Hub) configBundle(person model.Person, dev model.Device) (model.ConfigBundle, error) {
	b := model.ConfigBundle{ServerTime: time.Now().UTC(), S3Endpoint: "http://" + h.cfg.TailnetIP + ":" + h.cfg.S3Port,
		SyncInterval: model.DefaultSyncSeconds, DeviceStatus: dev.Status, SupportContact: h.cfg.SupportContact}
	if v := h.db.Setting("sync_interval"); v != "" {
		fmt.Sscanf(v, "%d", &b.SyncInterval)
	}
	mems, err := h.db.CirclesOf(person.ID)
	if err != nil {
		return b, err
	}
	if person.Status != model.StatusActive {
		mems = nil
	}
	for _, m := range mems {
		c, err := h.db.CircleByID(m.CircleID)
		if err != nil {
			continue
		}
		ak, sk, _ := h.db.CircleS3(c.ID)
		cc := model.CircleConfig{ID: c.ID, Slug: c.Slug, DisplayName: c.DisplayName, Bucket: c.BucketPrefix, Generation: c.Generation,
			SyncMode: c.SyncMode, Role: m.Role, QuotaBytes: c.QuotaBytes, VersionRetentionDays: c.VersionRetentionDays, Excludes: c.Excludes, BwLimit: c.BwLimit,
			S3AccessKey: ak, S3SecretKey: sk}
		if h.gar != nil {
			if bi, err := h.gar.BucketInfo(c.BucketPrefix); err == nil {
				cc.UsedBytes = bi.Bytes
			}
		}
		b.Circles = append(b.Circles, cc)
	}
	b.Grants, _ = h.db.GrantsFor(dev.ID)
	if rel, ok := h.db.ReleaseGet(dev.OS, dev.Arch); ok && rel.Version != "" && rel.Version != dev.AgentVersion && semverNewer(rel.Version, dev.AgentVersion) {
		b.Update = &model.UpdateInfo{Version: rel.Version, URL: fmt.Sprintf("http://%s:%s/v1/update/%s/%s", h.cfg.TailnetIP, h.cfg.AgentAPIPort, dev.OS, dev.Arch), SHA256: rel.SHA256, Sig: rel.Sig}
	}
	return b, nil
}

func semverNewer(a, b string) bool {
	pa, pb := semverParts(a), semverParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}
func semverParts(s string) [3]int {
	var p [3]int
	s = strings.TrimPrefix(s, "v")
	fmt.Sscanf(s, "%d.%d.%d", &p[0], &p[1], &p[2])
	return p
}

func (h *Hub) handleUpdateDownload(w http.ResponseWriter, r *http.Request, dev model.Device) {
	rel, ok := h.db.ReleaseGet(r.PathValue("os"), r.PathValue("arch"))
	if !ok {
		writeErr(w, 404, "no release")
		return
	}
	f := filepath.Join(h.cfg.ReleasesDir, filepath.Base(rel.File))
	if _, err := os.Stat(f); err != nil {
		writeErr(w, 404, "release file missing")
		return
	}
	w.Header().Set("X-Quietport-Version", rel.Version)
	w.Header().Set("X-Quietport-SHA256", rel.SHA256)
	w.Header().Set("X-Quietport-Sig", rel.Sig)
	http.ServeFile(w, r, f)
}

var errNoOperator = errors.New("operator token not initialised")

func (h *Hub) checkOperator(r *http.Request) error {
	want := h.db.Setting("operator_token_hash")
	if want == "" {
		return errNoOperator
	}
	if cryptobox.HashToken(bearer(r)) != want {
		return errors.New("bad operator token")
	}
	return nil
}

func logf(format string, a ...any) { log.Printf(format, a...) }

var _ = hubdb.ErrNotFound
