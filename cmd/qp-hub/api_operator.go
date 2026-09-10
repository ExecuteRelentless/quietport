package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"quietport.app/quietport/internal/hubdb"
	"quietport.app/quietport/internal/model"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

func (h *Hub) routesOperator(mux *http.ServeMux) {
	op := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if err := h.checkOperator(r); err != nil {
				writeErr(w, 401, err.Error())
				return
			}
			fn(w, r)
		}
	}
	mux.HandleFunc("GET /op/ping", op(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "version": Version, "host": h.cfg.Host, "tailnet_ip": h.cfg.TailnetIP})
	}))
	mux.HandleFunc("POST /op/persons", op(h.opPersonAdd))
	mux.HandleFunc("GET /op/persons", op(h.opPersonList))
	mux.HandleFunc("GET /op/persons/{name}", op(h.opPersonShow))
	mux.HandleFunc("DELETE /op/persons/{name}", op(h.opPersonRemove))
	mux.HandleFunc("POST /op/persons/{name}/offboard", op(h.opPersonOffboard))
	mux.HandleFunc("POST /op/circles", op(h.opCircleCreate))
	mux.HandleFunc("GET /op/circles", op(h.opCircleList))
	mux.HandleFunc("GET /op/circles/{slug}", op(h.opCircleShow))
	mux.HandleFunc("PATCH /op/circles/{slug}", op(h.opCircleUpdate))
	mux.HandleFunc("DELETE /op/circles/{slug}", op(h.opCircleDestroy))
	mux.HandleFunc("POST /op/circles/{slug}/members", op(h.opMemberAdd))
	mux.HandleFunc("DELETE /op/circles/{slug}/members/{person}", op(h.opMemberRemove))
	mux.HandleFunc("POST /op/circles/{slug}/opkey", op(h.opCircleOpKey))
	mux.HandleFunc("DELETE /op/circles/{slug}/opkey/{id}", op(h.opCircleOpKeyDelete))
	mux.HandleFunc("POST /op/circles/{slug}/generation", op(h.opCircleSetGeneration))
	mux.HandleFunc("POST /op/invites", op(h.opInviteCreate))
	mux.HandleFunc("GET /op/invites", op(h.opInviteList))
	mux.HandleFunc("DELETE /op/invites/{prefix}", op(h.opInviteRevoke))
	mux.HandleFunc("GET /op/devices", op(h.opDeviceList))
	mux.HandleFunc("DELETE /op/devices/{id}", op(h.opDeviceRevoke))
	mux.HandleFunc("POST /op/grants", op(h.opGrantsPut))
	mux.HandleFunc("GET /op/status", op(h.opStatus))
	mux.HandleFunc("GET /op/logs/{name}", op(h.opLogs))
	mux.HandleFunc("GET /op/audit", op(h.opAudit))
	mux.HandleFunc("GET /op/events", op(h.opEvents))
	mux.HandleFunc("POST /op/policy/refresh", op(func(w http.ResponseWriter, r *http.Request) {
		if err := h.refreshPolicy(); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /op/backup", op(h.opBackupStatus))
	mux.HandleFunc("POST /op/settings", op(func(w http.ResponseWriter, r *http.Request) {
		var kv map[string]string
		if err := readJSON(r, &kv); err != nil {
			writeErr(w, 400, "bad request")
			return
		}
		for k, v := range kv {
			if k == "operator_token_hash" {
				continue
			}
			_ = h.db.SetSetting(k, v)
		}
		h.db.Audit(r.Header.Get("X-Operator"), "settings.set", "hub", fmt.Sprint(kv))
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
}

func operator(r *http.Request) string {
	if o := r.Header.Get("X-Operator"); o != "" {
		return o
	}
	return "operator"
}

// --- persons ---

func (h *Hub) opPersonAdd(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Email, Household string }
	if err := readJSON(r, &in); err != nil || !slugRe.MatchString(in.Name) {
		writeErr(w, 400, "name must be lowercase letters, digits and dashes (it becomes the mesh user name)")
		return
	}
	// email is optional: it is only a note for the operator, nothing is ever sent to it
	uid, err := h.hs.UserCreate(in.Name)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	p, err := h.db.PersonAdd(model.Person{Name: in.Name, DisplayName: in.Name, Email: in.Email, Household: in.Household, HSUser: in.Name, HSUserID: uid})
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	h.db.Audit(operator(r), "person.add", p.Name, p.Email)
	_ = h.refreshPolicy()
	writeJSON(w, 201, p)
}

func (h *Hub) opPersonList(w http.ResponseWriter, r *http.Request) {
	ps, err := h.db.Persons()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, ps)
}

type personView struct {
	model.Person
	Circles []model.Membership `json:"circles"`
	Devices []deviceView       `json:"devices"`
	Invites []model.Invite     `json:"invites"`
}
type deviceView struct {
	model.Device
	Heartbeat *model.Heartbeat `json:"heartbeat,omitempty"`
	Online    bool             `json:"online"`
}

func (h *Hub) personView(p model.Person) personView {
	v := personView{Person: p}
	v.Circles, _ = h.db.CirclesOf(p.ID)
	devs, _ := h.db.Devices(p.ID)
	for _, d := range devs {
		dv := deviceView{Device: d, Online: time.Since(d.LastHeartbeat) < 2*model.HeartbeatInterval}
		if hb, ok := h.db.HeartbeatLatest(d.ID); ok {
			dv.Heartbeat = &hb
		}
		v.Devices = append(v.Devices, dv)
	}
	invs, _ := h.db.Invites()
	for _, i := range invs {
		if i.PersonID == p.ID && !i.Revoked {
			v.Invites = append(v.Invites, i.Invite)
		}
	}
	return v
}

func (h *Hub) opPersonShow(w http.ResponseWriter, r *http.Request) {
	p, err := h.db.PersonByName(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, "no such person")
		return
	}
	writeJSON(w, 200, h.personView(p))
}

func (h *Hub) opPersonRemove(w http.ResponseWriter, r *http.Request) {
	p, err := h.db.PersonByName(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, "no such person")
		return
	}
	devs, _ := h.db.Devices(p.ID)
	for _, d := range devs {
		if d.Status != model.StatusRevoked {
			writeErr(w, 409, "person still has active devices: run offboard first")
			return
		}
	}
	if p.HSUserID > 0 {
		_ = h.hs.UserDestroy(p.HSUserID)
	}
	if err := h.db.PersonDelete(p.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	h.db.Audit(operator(r), "person.remove", p.Name, "")
	_ = h.refreshPolicy()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// opPersonOffboard does the hub-side steps of FR-115 (1, 2, 6). Key rotation and re-encryption (3, 4, 5) run from
// qpctl because they need circle keys, which the hub does not have.
func (h *Hub) opPersonOffboard(w http.ResponseWriter, r *http.Request) {
	p, err := h.db.PersonByName(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, "no such person")
		return
	}
	devs, _ := h.db.Devices(p.ID)
	var revoked []int64
	for _, d := range devs {
		if d.HSNodeID > 0 {
			_ = h.hs.NodeDelete(d.HSNodeID)
		}
		_ = h.db.DeviceSetStatus(d.ID, model.StatusRevoked)
		_ = h.db.GrantsDeleteForDevice(d.ID)
		revoked = append(revoked, d.ID)
	}
	mems, _ := h.db.CirclesOf(p.ID)
	var circles []string
	for _, m := range mems {
		c, _ := h.db.CircleByID(m.CircleID)
		circles = append(circles, c.Slug)
		_ = h.db.MemberRemove(p.ID, m.CircleID)
	}
	invs, _ := h.db.Invites()
	for _, i := range invs {
		if i.PersonID == p.ID && i.ConsumedAt == nil && !i.Revoked {
			if i.PreAuthKeyID > 0 {
				_ = h.hs.PreAuthKeyExpire(p.HSUserID, i.PreAuthKeyID)
			}
			_ = h.db.InviteRevoke(i.ID)
		}
	}
	_ = h.db.PersonSetStatus(p.ID, model.StatusOffboarded)
	h.db.Audit(operator(r), "person.offboard", p.Name, fmt.Sprintf("devices=%v circles=%v", revoked, circles))
	_ = h.refreshPolicy()
	writeJSON(w, 200, map[string]any{"devices_revoked": revoked, "circles": circles})
}

// --- circles ---

func (h *Hub) opCircleCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Slug, Name, Mode, BwLimit, Invites string
		Quota                              int64
		Retention                          int
		Excludes                           []string
	}
	if err := readJSON(r, &in); err != nil || !slugRe.MatchString(in.Slug) || in.Name == "" {
		writeErr(w, 400, "slug must be lowercase letters, digits and dashes; name is required")
		return
	}
	if h.gar == nil {
		writeErr(w, 500, "storage not configured")
		return
	}
	bucket := "qp-" + in.Slug
	b, err := h.gar.BucketCreate(bucket, in.Quota)
	if err != nil {
		writeErr(w, 500, "storage: "+err.Error())
		return
	}
	k, err := h.gar.KeyCreate(bucket+"-members", b.ID, false)
	if err != nil {
		writeErr(w, 500, "storage key: "+err.Error())
		return
	}
	c, err := h.db.CircleCreate(model.Circle{Slug: in.Slug, DisplayName: in.Name, BucketPrefix: bucket, QuotaBytes: in.Quota, SyncMode: in.Mode, VersionRetentionDays: in.Retention, Excludes: in.Excludes, BwLimit: in.BwLimit, InvitePolicy: in.Invites}, k.AccessKeyID, k.SecretAccessKey)
	if err != nil {
		_ = h.gar.KeyDelete(k.AccessKeyID)
		_ = h.gar.BucketDelete(b.ID)
		writeErr(w, 409, err.Error())
		return
	}
	h.db.Audit(operator(r), "circle.create", c.Slug, fmt.Sprintf("quota=%d mode=%s", c.QuotaBytes, c.SyncMode))
	writeJSON(w, 201, c)
}

func (h *Hub) fillCircle(c *model.Circle) {
	c.Members, _ = h.db.MembersOf(c.ID)
	if h.gar != nil {
		if bi, err := h.gar.BucketInfo(c.BucketPrefix); err == nil {
			c.UsedBytes = bi.Bytes
		}
	}
}

func (h *Hub) opCircleList(w http.ResponseWriter, r *http.Request) {
	cs, err := h.db.Circles()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for i := range cs {
		h.fillCircle(&cs[i])
	}
	writeJSON(w, 200, cs)
}

func (h *Hub) opCircleShow(w http.ResponseWriter, r *http.Request) {
	c, err := h.db.CircleBySlug(r.PathValue("slug"))
	if err != nil {
		writeErr(w, 404, "no such circle")
		return
	}
	h.fillCircle(&c)
	writeJSON(w, 200, c)
}

func (h *Hub) opCircleUpdate(w http.ResponseWriter, r *http.Request) {
	c, err := h.db.CircleBySlug(r.PathValue("slug"))
	if err != nil {
		writeErr(w, 404, "no such circle")
		return
	}
	var in map[string]json.RawMessage
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	set := func(k string, v any) bool {
		raw, ok := in[k]
		if !ok {
			return false
		}
		return json.Unmarshal(raw, v) == nil
	}
	set("name", &c.DisplayName)
	set("mode", &c.SyncMode)
	set("retention", &c.VersionRetentionDays)
	set("excludes", &c.Excludes)
	set("bwlimit", &c.BwLimit)
	set("invites", &c.InvitePolicy)
	var ownerName string
	if set("owner", &ownerName) {
		if ownerName == "" || ownerName == "none" {
			c.OwnerPersonID = 0
		} else if p, err := h.db.PersonByName(ownerName); err == nil {
			c.OwnerPersonID = p.ID
		} else {
			writeErr(w, 404, "no such person for owner")
			return
		}
	}
	if set("quota", &c.QuotaBytes) && h.gar != nil {
		if bi, err := h.gar.BucketInfo(c.BucketPrefix); err == nil {
			if err := h.gar.BucketSetQuota(bi.ID, c.QuotaBytes); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
		}
	}
	if err := h.db.CircleUpdate(c); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	h.db.Audit(operator(r), "circle.update", c.Slug, string(mustJSON(in)))
	h.fillCircle(&c)
	writeJSON(w, 200, c)
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func (h *Hub) opCircleDestroy(w http.ResponseWriter, r *http.Request) {
	c, err := h.db.CircleBySlug(r.PathValue("slug"))
	if err != nil {
		writeErr(w, 404, "no such circle")
		return
	}
	if h.gar != nil {
		ak, sk, _ := h.db.CircleS3(c.ID)
		if bi, err := h.gar.BucketInfo(c.BucketPrefix); err == nil {
			// empty the bucket through the S3 API with a temporary owner key, then delete it
			ok, oerr := h.gar.KeyCreate(c.BucketPrefix+"-purge", bi.ID, true)
			if oerr == nil {
				_ = h.rclonePurge(c.BucketPrefix, ok.AccessKeyID, ok.SecretAccessKey)
				_ = h.gar.KeyDelete(ok.AccessKeyID)
			}
			_ = h.gar.KeyDelete(ak)
			_ = sk
			if err := h.gar.BucketDelete(bi.ID); err != nil {
				writeErr(w, 500, "storage: "+err.Error())
				return
			}
		}
	}
	if err := h.db.CircleDelete(c.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	h.db.Audit(operator(r), "circle.destroy", c.Slug, "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (h *Hub) rcloneEnv(bucket, ak, sk string) []string {
	return append(os.Environ(),
		"RCLONE_CONFIG_HUB_TYPE=s3", "RCLONE_CONFIG_HUB_PROVIDER=Other", "RCLONE_CONFIG_HUB_ACCESS_KEY_ID="+ak,
		"RCLONE_CONFIG_HUB_SECRET_ACCESS_KEY="+sk, "RCLONE_CONFIG_HUB_ENDPOINT=http://"+h.cfg.TailnetIP+":"+h.cfg.S3Port,
		"RCLONE_CONFIG_HUB_REGION=garage", "RCLONE_CONFIG_HUB_FORCE_PATH_STYLE=true")
}

func (h *Hub) rclonePurge(bucket, ak, sk string) error {
	cmd := exec.Command("rclone", "delete", "hub:"+bucket, "--rmdirs", "-q")
	cmd.Env = h.rcloneEnv(bucket, ak, sk)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("purge: %v: %s", err, out)
	}
	return nil
}

// opCircleOpKey: a temporary owner-level S3 key for the operator (re-encryption on rotate, canary checks).
func (h *Hub) opCircleOpKey(w http.ResponseWriter, r *http.Request) {
	c, err := h.db.CircleBySlug(r.PathValue("slug"))
	if err != nil {
		writeErr(w, 404, "no such circle")
		return
	}
	bi, err := h.gar.BucketInfo(c.BucketPrefix)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	k, err := h.gar.KeyCreate(c.BucketPrefix+"-op-"+strconv.FormatInt(time.Now().Unix(), 10), bi.ID, true)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	h.db.Audit(operator(r), "circle.opkey", c.Slug, k.AccessKeyID)
	writeJSON(w, 200, map[string]any{"access_key": k.AccessKeyID, "secret_key": k.SecretAccessKey, "endpoint": "http://" + h.cfg.TailnetIP + ":" + h.cfg.S3Port, "bucket": c.BucketPrefix, "used_bytes": bi.Bytes, "generation": c.Generation})
}

func (h *Hub) opCircleOpKeyDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.gar.KeyDelete(r.PathValue("id")); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// opCircleSetGeneration: the switch at the end of a rotation (FR-91). Rotates the members' S3 key, drops old grants.
func (h *Hub) opCircleSetGeneration(w http.ResponseWriter, r *http.Request) {
	c, err := h.db.CircleBySlug(r.PathValue("slug"))
	if err != nil {
		writeErr(w, 404, "no such circle")
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
	// devices of current members that have no grant for the new generation will be told to re-provision by their agent
	h.db.Audit(operator(r), "circle.rotate-key", c.Slug, fmt.Sprintf("generation %d -> %d", c.Generation, in.Generation))
	writeJSON(w, 200, map[string]any{"ok": true, "generation": in.Generation})
}

// --- membership ---

func (h *Hub) opMemberAdd(w http.ResponseWriter, r *http.Request) {
	c, err := h.db.CircleBySlug(r.PathValue("slug"))
	if err != nil {
		writeErr(w, 404, "no such circle")
		return
	}
	var in struct{ Person, Role string }
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	p, err := h.db.PersonByName(in.Person)
	if err != nil {
		writeErr(w, 404, "no such person")
		return
	}
	if err := h.db.MemberAdd(p.ID, c.ID, in.Role); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	h.db.Audit(operator(r), "circle.add-member", c.Slug, p.Name)
	devs, _ := h.db.Devices(p.ID)
	writeJSON(w, 200, map[string]any{"ok": true, "devices": devs, "generation": c.Generation, "circle_id": c.ID})
}

func (h *Hub) opMemberRemove(w http.ResponseWriter, r *http.Request) {
	c, err := h.db.CircleBySlug(r.PathValue("slug"))
	if err != nil {
		writeErr(w, 404, "no such circle")
		return
	}
	p, err := h.db.PersonByName(r.PathValue("person"))
	if err != nil {
		writeErr(w, 404, "no such person")
		return
	}
	_ = h.db.MemberRemove(p.ID, c.ID)
	devs, _ := h.db.Devices(p.ID)
	for _, d := range devs {
		_ = h.db.GrantsDeleteForDeviceCircle(d.ID, c.ID)
	}
	h.db.Audit(operator(r), "circle.remove-member", c.Slug, p.Name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// --- invites ---

func (h *Hub) opInviteCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Person     string
		CircleIDs  []int64 `json:"circle_ids"`
		TTL        string
		SealedKeys string `json:"sealed_keys"`
		CodeHash   string `json:"code_hash"`
		Prefix     string
	}
	if err := readJSON(r, &in); err != nil || in.CodeHash == "" || in.SealedKeys == "" {
		writeErr(w, 400, "bad request")
		return
	}
	p, err := h.db.PersonByName(in.Person)
	if err != nil || p.Status != model.StatusActive {
		writeErr(w, 404, "no such active person")
		return
	}
	ttl, err := time.ParseDuration(in.TTL)
	if err != nil || ttl <= 0 || ttl > 24*time.Hour {
		ttl = 24 * time.Hour // FR-84 / FR-101
	}
	for _, cid := range in.CircleIDs {
		if _, err := h.db.CircleByID(cid); err != nil {
			writeErr(w, 404, fmt.Sprintf("circle %d not found", cid))
			return
		}
		_ = h.db.MemberAdd(p.ID, cid, "")
	}
	pak, err := h.hs.PreAuthKeyCreate(p.HSUserID, ttl, nil)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	pakID, _ := pak.ID.Int64()
	inv, err := h.db.InviteAdd(hubdb.InviteRow{Invite: model.Invite{InviterName: h.cfg.OperatorName, CodeHash: in.CodeHash, PersonID: p.ID, CircleIDs: in.CircleIDs, ExpiresAt: time.Now().Add(ttl), Prefix: in.Prefix},
		PreAuthKey: pak.Key, PreAuthKeyID: pakID, SealedKeys: in.SealedKeys})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	h.db.Audit(operator(r), "invite.create", p.Name, fmt.Sprintf("circles=%v ttl=%s prefix=%s", in.CircleIDs, ttl, in.Prefix))
	writeJSON(w, 201, map[string]any{"id": inv.ID, "expires_at": inv.ExpiresAt, "url_base": "https://" + h.cfg.Host + "/j/"})
}

func (h *Hub) opInviteList(w http.ResponseWriter, r *http.Request) {
	invs, err := h.db.Invites()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := []model.Invite{}
	for _, i := range invs {
		if i.Revoked {
			continue
		}
		out = append(out, i.Invite)
	}
	writeJSON(w, 200, out)
}

func (h *Hub) opInviteRevoke(w http.ResponseWriter, r *http.Request) {
	inv, err := h.db.InviteByPrefix(r.PathValue("prefix"))
	if err != nil {
		writeErr(w, 404, "no live invite with that prefix")
		return
	}
	p, _ := h.db.PersonByID(inv.PersonID)
	if inv.PreAuthKeyID > 0 {
		_ = h.hs.PreAuthKeyExpire(p.HSUserID, inv.PreAuthKeyID)
	}
	_ = h.db.InviteRevoke(inv.ID)
	h.db.Audit(operator(r), "invite.revoke", p.Name, inv.Prefix)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// --- devices + grants ---

func (h *Hub) opDeviceList(w http.ResponseWriter, r *http.Request) {
	var pid int64
	if n := r.URL.Query().Get("person"); n != "" {
		p, err := h.db.PersonByName(n)
		if err != nil {
			writeErr(w, 404, "no such person")
			return
		}
		pid = p.ID
	}
	devs, err := h.db.Devices(pid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := []deviceView{}
	for _, d := range devs {
		dv := deviceView{Device: d, Online: time.Since(d.LastHeartbeat) < 2*model.HeartbeatInterval}
		if hb, ok := h.db.HeartbeatLatest(d.ID); ok {
			dv.Heartbeat = &hb
		}
		out = append(out, dv)
	}
	writeJSON(w, 200, out)
}

func (h *Hub) opDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	d, err := h.db.DeviceByID(id)
	if err != nil {
		writeErr(w, 404, "no such device")
		return
	}
	if d.HSNodeID > 0 {
		_ = h.hs.NodeDelete(d.HSNodeID)
	}
	_ = h.db.DeviceSetStatus(d.ID, model.StatusRevoked)
	_ = h.db.GrantsDeleteForDevice(d.ID)
	p, _ := h.db.PersonByID(d.PersonID)
	h.db.Audit(operator(r), "device.revoke", p.Name, fmt.Sprintf("device %d %s", d.ID, d.Hostname))
	mems, _ := h.db.CirclesOf(d.PersonID)
	var slugs []string
	for _, m := range mems {
		c, _ := h.db.CircleByID(m.CircleID)
		slugs = append(slugs, c.Slug)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "circles": slugs})
}

func (h *Hub) opGrantsPut(w http.ResponseWriter, r *http.Request) {
	var gs []model.KeyGrant
	if err := readJSON(r, &gs); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	for _, g := range gs {
		if err := h.db.GrantPut(g); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "count": len(gs)})
}

// --- status / logs / audit ---

type statusView struct {
	Hub     map[string]any `json:"hub"`
	Persons []personView   `json:"persons"`
	Circles []model.Circle `json:"circles"`
	Backup  map[string]any `json:"backup"`
}

func (h *Hub) opStatus(w http.ResponseWriter, r *http.Request) {
	ps, _ := h.db.Persons()
	sv := statusView{Hub: map[string]any{"version": Version, "host": h.cfg.Host, "tailnet_ip": h.cfg.TailnetIP, "time": time.Now().UTC()}}
	for _, p := range ps {
		sv.Persons = append(sv.Persons, h.personView(p))
	}
	sv.Circles, _ = h.db.Circles()
	for i := range sv.Circles {
		h.fillCircle(&sv.Circles[i])
	}
	sv.Backup = h.backupStatus()
	writeJSON(w, 200, sv)
}

func (h *Hub) opLogs(w http.ResponseWriter, r *http.Request) {
	p, err := h.db.PersonByName(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, "no such person")
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	if n <= 0 {
		n = 100
	}
	devs, _ := h.db.Devices(p.ID)
	out := map[string]any{}
	for _, d := range devs {
		hbs, _ := h.db.Heartbeats(d.ID, n)
		out[fmt.Sprintf("%d:%s", d.ID, d.Hostname)] = hbs
	}
	evs, _ := h.db.Events(p.ID, n)
	out["events"] = evs
	writeJSON(w, 200, out)
}

func (h *Hub) opAudit(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 {
		n = 200
	}
	a, err := h.db.AuditList(n)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, a)
}

func (h *Hub) opEvents(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 {
		n = 200
	}
	e, err := h.db.Events(0, n)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, e)
}

func (h *Hub) backupStatus() map[string]any {
	out := map[string]any{"last": nil}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(h.cfg.DBPath), "backup", "last.json"))
	if err == nil {
		var v map[string]any
		if json.Unmarshal(b, &v) == nil {
			out["last"] = v
		}
	}
	c, err := os.ReadFile(filepath.Join(filepath.Dir(h.cfg.DBPath), "backup", "canary.json"))
	if err == nil {
		var v map[string]any
		if json.Unmarshal(c, &v) == nil {
			out["canary"] = v
		}
	}
	return out
}

func (h *Hub) opBackupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, h.backupStatus())
}

// refreshPolicy regenerates the headscale policy from persons/households (FR-81/82/83), commits it and pushes it.
func (h *Hub) refreshPolicy() error {
	ps, err := h.db.Persons()
	if err != nil {
		return err
	}
	groups := map[string][]string{}
	for _, p := range ps {
		if p.Status != model.StatusActive {
			continue
		}
		hh := p.Household
		if hh == "" {
			hh = p.Name
		}
		g := "group:household-" + strings.ToLower(regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(strings.ToLower(hh), "-"))
		groups[g] = append(groups[g], p.HSUser+"@")
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("// Quietport mesh policy. Generated by qp-hub from the person/household tables; do not edit by hand.\n")
	sb.WriteString("// Posture (FR-82): every device may reach the hub; devices may not reach each other.\n{\n  \"groups\": {\n")
	for i, k := range keys {
		fmt.Fprintf(&sb, "    %q: [%s]", k, quoteList(groups[k]))
		if i < len(keys)-1 {
			sb.WriteString(",")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("  },\n  \"tagOwners\": { \"tag:hub\": [\"hub@\"] },\n  \"acls\": [\n    { \"action\": \"accept\", \"src\": [\"*\"], \"dst\": [\"tag:hub:*\"] }\n  ]\n}\n")
	_ = os.MkdirAll(h.cfg.PolicyDir, 0o750)
	path := filepath.Join(h.cfg.PolicyDir, "policy.hujson")
	if err := os.WriteFile(path, []byte(sb.String()), 0o640); err != nil {
		return err
	}
	git := func(args ...string) { c := exec.Command("git", args...); c.Dir = h.cfg.PolicyDir; _ = c.Run() }
	if _, err := os.Stat(filepath.Join(h.cfg.PolicyDir, ".git")); err != nil {
		git("init", "-q", "-b", "main")
	}
	git("add", "policy.hujson")
	git("-c", "user.name=qp-hub", "-c", "user.email=hub@quietport.app", "commit", "-q", "-m", "policy refresh "+time.Now().UTC().Format(time.RFC3339))
	return h.hs.PolicySet(path)
}

func quoteList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = strconv.Quote(x)
	}
	return strings.Join(q, ", ")
}
