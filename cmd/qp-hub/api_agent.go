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
	"regexp"
	"strconv"
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
	mux.HandleFunc("POST /v1/invites", h.withDevice(h.handleDeviceInvite))
	mux.HandleFunc("POST /v1/join", h.withDevice(h.handleJoin))
	mux.HandleFunc("POST /v1/circles", h.withDevice(h.handleDeviceCircleCreate))
	mux.HandleFunc("GET /v1/circles/{id}/people", h.withDevice(h.handleCirclePeople))
	mux.HandleFunc("POST /v1/circles/{id}/remove", h.withDevice(h.handleCircleRemovePerson))
	mux.HandleFunc("POST /v1/circles/{id}/opkey", h.withDevice(h.handleCircleOpKeyDevice))
	mux.HandleFunc("DELETE /v1/circles/{id}/opkey/{ak}", h.withDevice(h.handleCircleOpKeyDeleteDevice))
	mux.HandleFunc("POST /v1/circles/{id}/generation", h.withDevice(h.handleCircleGenerationDevice))
	mux.HandleFunc("POST /v1/circles/{id}/grants", h.withDevice(h.handleCircleGrantsDevice))
	mux.HandleFunc("GET /v1/due", h.withDevice(h.handleDue))
	mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "time": time.Now().UTC()})
	})
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
	if hb.AgentVersion != "" {
		dev.AgentVersion = hb.AgentVersion // the bundle's update check must see what runs now, not the last heartbeat
	}
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

// handleDue is asked every 30 seconds: a key sealed to this device makes it heartbeat now, not at its next interval
// (docs/adr/0028).
func (h *Hub) handleDue(w http.ResponseWriter, r *http.Request, dev model.Device) {
	writeJSON(w, 200, map[string]bool{"heartbeat": h.db.GrantWaiting(dev.ID)})
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
			CanInvite: c.InvitePolicy != model.InviteByOperator && m.Role == "member", Owner: c.OwnerPersonID != 0 && c.OwnerPersonID == person.ID, S3AccessKey: ak, S3SecretKey: sk}
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

var nameSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// handleDeviceInvite: a member device invites someone to a circle it is in. No email, no account: the person record
// is created from the name the inviter typed, the mesh user and pre-auth key are made here, and the circle key
// arrives sealed under the code from the device (the hub keeps the hash only).
func (h *Hub) handleDeviceInvite(w http.ResponseWriter, r *http.Request, dev model.Device) {
	var req model.DeviceInviteRequest
	if err := readJSON(r, &req); err != nil || req.CodeHash == "" || req.SealedKeys == "" || req.CircleID == 0 {
		writeErr(w, 400, "bad request")
		return
	}
	inviter, err := h.db.PersonByID(dev.PersonID)
	if err != nil || inviter.Status != model.StatusActive {
		writeErr(w, 403, "inviter not active")
		return
	}
	c, err := h.db.CircleByID(req.CircleID)
	if err != nil {
		writeErr(w, 404, "no such circle")
		return
	}
	if c.InvitePolicy == model.InviteByOperator {
		writeErr(w, 403, "only the operator can invite people to this folder")
		return
	}
	if role, ok := h.db.MemberRole(inviter.ID, c.ID); !ok || role != "member" {
		writeErr(w, 403, "you are not a read/write member of this folder")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "guest"
	}
	if len(name) > 40 {
		name = name[:40]
	}
	base := strings.Trim(nameSlugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if base == "" {
		base = "guest"
	}
	slug := base + "-" + strings.ToLower(cryptobox.NewInviteCode()[:4])
	uid, err := h.hs.UserCreate(slug)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	p, err := h.db.PersonAdd(model.Person{Name: slug, DisplayName: name, Email: "", Household: inviter.Household, HSUser: slug, HSUserID: uid})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = h.db.MemberAdd(p.ID, c.ID, "member")
	ttl, err := time.ParseDuration(req.TTL)
	if err != nil || ttl <= 0 || ttl > 24*time.Hour {
		ttl = 24 * time.Hour
	}
	pak, err := h.hs.PreAuthKeyCreate(p.HSUserID, ttl, nil)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	pakID, _ := pak.ID.Int64()
	inv, err := h.db.InviteAdd(hubdb.InviteRow{Invite: model.Invite{InviterName: personLabel(inviter), CodeHash: req.CodeHash, PersonID: p.ID, CircleIDs: []int64{c.ID}, ExpiresAt: time.Now().Add(ttl), Prefix: req.Prefix},
		PreAuthKey: pak.Key, PreAuthKeyID: pakID, SealedKeys: req.SealedKeys, MintedPerson: true})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	h.db.Audit("device:"+strconv.FormatInt(dev.ID, 10)+"/"+inviter.Name, "invite.create", p.Name, fmt.Sprintf("circle=%s by member; prefix=%s", c.Slug, inv.Prefix))
	h.db.Event("invite.created", inviter.ID, dev.ID, fmt.Sprintf("%s invited %q to %s", inviter.Name, name, c.Slug))
	_ = h.refreshPolicy()
	writeJSON(w, 201, model.DeviceInviteResponse{URL: "https://" + h.cfg.Host + "/j/", ExpiresAt: inv.ExpiresAt, Person: p.Name})
}

// handleJoin: a computer that already has Quietport redeems an invite link for the person it belongs to
// (docs/adr/0019). The link is the authorisation, exactly as it is for a fresh install: whoever holds the code can
// open the keys sealed under it, so the hub adds the caller's person to the link's folders and hands those keys
// back. Nothing is enrolled and nothing on the device is replaced. The person a member-made link minted, who now
// has no purpose, is removed, and the invite row stays as the record of who redeemed it. The reply names the
// joiner's own other computers and no one else: a member never learns who else is on this Quietport from here.
func (h *Hub) handleJoin(w http.ResponseWriter, r *http.Request, dev model.Device) {
	if !inviteLimiter.allow(clientIP(r)) {
		writeErr(w, 429, "Too many attempts. Please try again in a few minutes.")
		return
	}
	var req model.JoinRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	code := strings.ToLower(strings.TrimSpace(req.Code))
	if !codeRe.MatchString(code) {
		writeErr(w, 400, "that does not look like a Quietport invite link.")
		return
	}
	person, err := h.db.PersonByID(dev.PersonID)
	if err != nil || person.Status != model.StatusActive {
		writeErr(w, 403, "not active")
		return
	}
	inv, ok, err := h.db.InviteConsume(r.Context(), cryptobox.HashToken(code), clientIP(r))
	if err != nil || !ok {
		writeErr(w, 404, "This invitation link is no longer valid. Please ask the person who sent it for a new one.")
		return
	}
	minted, mintedErr := h.db.PersonByID(inv.PersonID)
	// the role the link carried: whatever its person was given in each folder
	roles := map[int64]string{}
	if mintedErr == nil {
		if mems, err := h.db.CirclesOf(minted.ID); err == nil {
			for _, m := range mems {
				roles[m.CircleID] = m.Role
			}
		}
	}
	joined := []string{}
	for _, cid := range inv.CircleIDs {
		c, err := h.db.CircleByID(cid)
		if err != nil {
			continue
		}
		if err := h.db.MemberAdd(person.ID, c.ID, roles[c.ID]); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		joined = append(joined, c.Slug)
	}
	_ = h.db.InviteSetPerson(inv.ID, person.ID)
	if len(joined) == 0 {
		writeErr(w, 410, "The folder in this link no longer exists.")
		return
	}
	if mintedErr == nil && inv.MintedPerson && minted.ID != person.ID {
		h.removeMintedPerson(minted, inv)
	}
	bundle, err := h.configBundle(person, dev)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := model.JoinResponse{InviterName: inv.InviterName, SealedKeys: inv.SealedKeys, Circles: []model.CircleConfig{}, OtherDevices: []model.DeviceKey{}}
	for _, cc := range bundle.Circles {
		for _, cid := range inv.CircleIDs {
			if cc.ID == cid {
				out.Circles = append(out.Circles, cc)
			}
		}
	}
	devs, _ := h.db.Devices(person.ID)
	for _, d := range devs {
		if d.ID != dev.ID && d.Status == model.StatusActive && d.PubKey != "" {
			out.OtherDevices = append(out.OtherDevices, model.DeviceKey{ID: d.ID, PubKey: d.PubKey})
		}
	}
	h.db.Audit("device:"+strconv.FormatInt(dev.ID, 10)+"/"+person.Name, "invite.join", strings.Join(joined, ","), fmt.Sprintf("prefix=%s from %q", inv.Prefix, inv.InviterName))
	h.db.Event("invite.joined", person.ID, dev.ID, fmt.Sprintf("%s joined %s with a link from %q", person.Name, strings.Join(joined, ","), inv.InviterName))
	go h.notifyOperator("Quietport: "+personLabel(person)+" joined a folder", fmt.Sprintf("%s joined %s with a link from %s at %s, from a computer that already had Quietport.", personLabel(person), strings.Join(joined, ", "), inv.InviterName, time.Now().Format(time.RFC1123)))
	_ = h.refreshPolicy()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

// removeMintedPerson takes away the person a member-made link created once someone who already had Quietport
// redeemed that link. They never enrolled a device, so there is nothing on the mesh to revoke beyond the unused
// pre-auth key and the empty mesh user; both are best effort, the record is what must go.
func (h *Hub) removeMintedPerson(p model.Person, inv hubdb.InviteRow) {
	if devs, _ := h.db.Devices(p.ID); len(devs) > 0 {
		return // not a placeholder after all; leave them alone
	}
	if inv.PreAuthKeyID != 0 {
		if err := h.hs.PreAuthKeyExpire(p.HSUserID, inv.PreAuthKeyID); err != nil {
			logf("join: expiring the unused pre-auth key of %s: %v", p.Name, err)
		}
	}
	if p.HSUserID != 0 {
		if err := h.hs.UserDestroy(p.HSUserID); err != nil {
			logf("join: removing the mesh user of %s: %v", p.Name, err)
		}
	}
	if err := h.db.PersonDelete(p.ID); err != nil {
		logf("join: removing %s: %v", p.Name, err)
		return
	}
	h.db.Audit("system", "person.remove", p.Name, "minted by invite "+inv.Prefix+", redeemed by an existing member")
}

// ownerCircle loads the circle in the path and checks the calling device's person owns it. A folder that does not
// exist gets the same answer as one the caller does not own: circle ids are sequential, and a member must not be
// able to count folders on this Quietport by probing them.
func (h *Hub) ownerCircle(w http.ResponseWriter, r *http.Request, dev model.Device) (model.Circle, bool) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	c, err := h.db.CircleByID(id)
	if err != nil || c.OwnerPersonID == 0 || c.OwnerPersonID != dev.PersonID {
		writeErr(w, 403, "only the owner of this folder can do that")
		return c, false
	}
	return c, true
}

func (h *Hub) handleCirclePeople(w http.ResponseWriter, r *http.Request, dev model.Device) {
	c, ok := h.ownerCircle(w, r, dev)
	if !ok {
		return
	}
	writeJSON(w, 200, h.circlePeople(c, dev))
}

func (h *Hub) circlePeople(c model.Circle, dev model.Device) []model.CirclePerson {
	mems, _ := h.db.MembersOf(c.ID)
	out := []model.CirclePerson{}
	for _, m := range mems {
		devs, _ := h.db.Devices(m.PersonID)
		act := []model.Device{}
		for _, d := range devs {
			if d.Status == model.StatusActive {
				act = append(act, d)
			}
		}
		label := m.PersonName
		if per, err := h.db.PersonByID(m.PersonID); err == nil {
			label = personLabel(per)
		}
		out = append(out, model.CirclePerson{PersonID: m.PersonID, Name: label, Role: m.Role, Self: m.PersonID == dev.PersonID, Devices: act})
	}
	return out
}

// handleCircleRemovePerson: the owner takes someone out of the folder. The owner's device then re-keys the folder
// (opkey -> re-encrypt -> grants -> generation), which is what locks the removed computer out of anything new.
func (h *Hub) handleCircleRemovePerson(w http.ResponseWriter, r *http.Request, dev model.Device) {
	c, ok := h.ownerCircle(w, r, dev)
	if !ok {
		return
	}
	var in struct {
		PersonID int64 `json:"person_id"`
	}
	if err := readJSON(r, &in); err != nil || in.PersonID == 0 || in.PersonID == dev.PersonID {
		writeErr(w, 400, "pick someone other than yourself")
		return
	}
	p, err := h.db.PersonByID(in.PersonID)
	if err != nil {
		writeErr(w, 404, "no such person")
		return
	}
	_ = h.db.MemberRemove(p.ID, c.ID)
	devs, _ := h.db.Devices(p.ID)
	for _, d := range devs {
		_ = h.db.GrantsDeleteForDeviceCircle(d.ID, c.ID)
	}
	invs, _ := h.db.Invites()
	for _, i := range invs {
		if i.PersonID == p.ID && i.ConsumedAt == nil && !i.Revoked {
			_ = h.db.InviteRevoke(i.ID)
		}
	}
	// no folders left: drop them from the mesh too
	if left, _ := h.db.CirclesOf(p.ID); len(left) == 0 {
		for _, d := range devs {
			if d.HSNodeID > 0 {
				_ = h.hs.NodeDelete(d.HSNodeID)
			}
			_ = h.db.DeviceSetStatus(d.ID, model.StatusRevoked)
		}
		_ = h.db.PersonSetStatus(p.ID, model.StatusOffboarded)
	}
	owner, _ := h.db.PersonByID(dev.PersonID)
	h.db.Audit("device:"+strconv.FormatInt(dev.ID, 10)+"/"+owner.Name, "circle.remove-member", c.Slug, p.Name)
	h.db.Event("member.removed", owner.ID, dev.ID, fmt.Sprintf("%s removed %s from %s", owner.Name, p.Name, c.Slug))
	_ = h.refreshPolicy()
	writeJSON(w, 200, h.circlePeople(c, dev))
}

func (h *Hub) handleCircleOpKeyDevice(w http.ResponseWriter, r *http.Request, dev model.Device) {
	c, ok := h.ownerCircle(w, r, dev)
	if !ok {
		return
	}
	bi, err := h.gar.BucketInfo(c.BucketPrefix)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	k, err := h.gar.KeyCreate(c.BucketPrefix+"-owner-"+strconv.FormatInt(time.Now().Unix(), 10), bi.ID, true)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"access_key": k.AccessKeyID, "secret_key": k.SecretAccessKey, "endpoint": "http://" + h.cfg.TailnetIP + ":" + h.cfg.S3Port, "bucket": c.BucketPrefix, "used_bytes": bi.Bytes, "generation": c.Generation})
}

func (h *Hub) handleCircleOpKeyDeleteDevice(w http.ResponseWriter, r *http.Request, dev model.Device) {
	if _, ok := h.ownerCircle(w, r, dev); !ok {
		return
	}
	_ = h.gar.KeyDelete(r.PathValue("ak"))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (h *Hub) handleCircleGenerationDevice(w http.ResponseWriter, r *http.Request, dev model.Device) {
	c, ok := h.ownerCircle(w, r, dev)
	if !ok {
		return
	}
	var in struct{ Generation int }
	if err := readJSON(r, &in); err != nil || in.Generation <= c.Generation {
		writeErr(w, 400, "generation must increase")
		return
	}
	bi, err := h.gar.BucketInfo(c.BucketPrefix)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	oldAK, _, _ := h.db.CircleS3(c.ID)
	k, err := h.gar.KeyCreate(c.BucketPrefix+"-members", bi.ID, false)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = h.db.CircleSetS3(c.ID, k.AccessKeyID, k.SecretAccessKey)
	_ = h.gar.KeyDelete(oldAK)
	_ = h.db.CircleSetGeneration(c.ID, in.Generation)
	_ = h.db.GrantsDeleteForCircleBelow(c.ID, in.Generation)
	owner, _ := h.db.PersonByID(dev.PersonID)
	h.db.Audit("device:"+strconv.FormatInt(dev.ID, 10)+"/"+owner.Name, "circle.rotate-key", c.Slug, fmt.Sprintf("generation %d -> %d", c.Generation, in.Generation))
	writeJSON(w, 200, map[string]any{"ok": true, "generation": in.Generation})
}

// handleCircleGrantsDevice stores circle keys sealed to devices. The owner stores them for everyone still in after
// a re-key. Any member's device may store them for the other computers of its own person, which is how a folder
// joined on one computer reaches that person's others (docs/adr/0019); a grant for anyone else's computer refuses
// the whole batch.
func (h *Hub) handleCircleGrantsDevice(w http.ResponseWriter, r *http.Request, dev model.Device) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	c, err := h.db.CircleByID(id)
	if err != nil {
		c = model.Circle{ID: id} // answered exactly like a folder the caller is not in, below
	}
	var gs []model.KeyGrant
	if err := readJSON(r, &gs); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	if c.OwnerPersonID == 0 || c.OwnerPersonID != dev.PersonID {
		if _, ok := h.db.MemberRole(dev.PersonID, c.ID); !ok {
			writeErr(w, 403, "you are not in this folder")
			return
		}
		own := map[int64]bool{}
		if devs, err := h.db.Devices(dev.PersonID); err == nil {
			for _, d := range devs {
				own[d.ID] = true
			}
		}
		for _, g := range gs {
			if !own[g.DeviceID] {
				writeErr(w, 403, "you can only pass a key to your own computers")
				return
			}
		}
	}
	for _, g := range gs {
		g.CircleID = c.ID
		if err := h.db.GrantPut(g); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "count": len(gs)})
}

// handleDeviceCircleCreate: a member starts another folder from their own computer. The device generates the key;
// the hub only makes the bucket, the storage credentials and the record, and makes the person its owner.
func (h *Hub) handleDeviceCircleCreate(w http.ResponseWriter, r *http.Request, dev model.Device) {
	if h.db.Setting("member_circles") == "0" {
		writeErr(w, 403, "only the operator can create folders on this Quietport")
		return
	}
	var in struct{ Name string }
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	// every member's computer makes a directory of this name (docs/adr/0020)
	name := model.FolderName(in.Name)
	if name == "" {
		writeErr(w, 400, "give the folder a name")
		return
	}
	person, err := h.db.PersonByID(dev.PersonID)
	if err != nil || person.Status != model.StatusActive {
		writeErr(w, 403, "not active")
		return
	}
	if h.gar == nil {
		writeErr(w, 500, "storage not configured")
		return
	}
	// a person may own at most 10 folders here; the operator can raise it
	own := 0
	if cs, err := h.db.Circles(); err == nil {
		for _, c := range cs {
			if c.OwnerPersonID == person.ID {
				own++
			}
		}
	}
	maxOwn := 10
	if v := h.db.Setting("max_member_circles"); v != "" {
		fmt.Sscanf(v, "%d", &maxOwn)
	}
	if own >= maxOwn {
		writeErr(w, 403, fmt.Sprintf("you already own %d folders here", own))
		return
	}
	cslug := strings.Trim(nameSlugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if cslug == "" {
		cslug = "folder"
	}
	cslug += "-" + strings.ToLower(cryptobox.NewInviteCode()[:4])
	quota := int64(5 << 30)
	if v := h.db.Setting("signup_quota"); v != "" {
		fmt.Sscanf(v, "%d", &quota)
	}
	bucket := "qp-" + cslug
	b, err := h.gar.BucketCreate(bucket, quota)
	if err != nil {
		writeErr(w, 500, "storage: "+err.Error())
		return
	}
	k, err := h.gar.KeyCreate(bucket+"-members", b.ID, false)
	if err != nil {
		writeErr(w, 500, "storage key: "+err.Error())
		return
	}
	c, err := h.db.CircleCreate(model.Circle{Slug: cslug, DisplayName: name, BucketPrefix: bucket, QuotaBytes: quota, SyncMode: model.ModeBidirectional, InvitePolicy: model.InviteByMembers, OwnerPersonID: person.ID}, k.AccessKeyID, k.SecretAccessKey)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = h.db.MemberAdd(person.ID, c.ID, "member")
	h.db.Audit("device:"+strconv.FormatInt(dev.ID, 10)+"/"+person.Name, "circle.create", c.Slug, fmt.Sprintf("by member %q quota=%d", name, quota))
	h.db.Event("circle.created", person.ID, dev.ID, fmt.Sprintf("%s started folder %q (%s)", person.Name, name, c.Slug))
	cfg, _ := h.configBundle(person, dev)
	for _, cc := range cfg.Circles {
		if cc.ID == c.ID {
			writeJSON(w, 201, cc)
			return
		}
	}
	writeErr(w, 500, "created but not found")
}
