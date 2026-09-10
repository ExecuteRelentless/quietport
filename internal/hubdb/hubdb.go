// Package hubdb is the hub's SQLite state (SRD §8). Circle keys are deliberately absent from this schema.
package hubdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"quietport.app/quietport/internal/model"
)

type DB struct{ *sql.DB }

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS person (
  id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL, email TEXT NOT NULL, household TEXT NOT NULL DEFAULT '',
  hs_user TEXT NOT NULL, hs_user_id INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS device (
  id INTEGER PRIMARY KEY, person_id INTEGER NOT NULL REFERENCES person(id), hostname TEXT NOT NULL, os TEXT NOT NULL, arch TEXT NOT NULL DEFAULT '',
  agent_version TEXT NOT NULL DEFAULT '', hs_node_id INTEGER NOT NULL DEFAULT 0, tailnet_ip TEXT NOT NULL DEFAULT '', pubkey TEXT NOT NULL,
  token_hash TEXT NOT NULL, enrolled_at TEXT NOT NULL, last_heartbeat TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'active');
CREATE TABLE IF NOT EXISTS circle (
  id INTEGER PRIMARY KEY, slug TEXT UNIQUE NOT NULL, display_name TEXT NOT NULL, bucket_prefix TEXT NOT NULL, generation INTEGER NOT NULL DEFAULT 1,
  quota_bytes INTEGER NOT NULL DEFAULT 0, sync_mode TEXT NOT NULL DEFAULT 'bidirectional', version_retention_days INTEGER NOT NULL DEFAULT 30,
  excludes TEXT NOT NULL DEFAULT '[]', bwlimit TEXT NOT NULL DEFAULT '', s3_access_key TEXT NOT NULL DEFAULT '', s3_secret_key TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS membership (
  person_id INTEGER NOT NULL REFERENCES person(id), circle_id INTEGER NOT NULL REFERENCES circle(id) ON DELETE CASCADE,
  role TEXT NOT NULL DEFAULT 'member', added_at TEXT NOT NULL, PRIMARY KEY(person_id, circle_id));
CREATE TABLE IF NOT EXISTS invite (
  id INTEGER PRIMARY KEY, code_hash TEXT UNIQUE NOT NULL, prefix TEXT NOT NULL, person_id INTEGER NOT NULL REFERENCES person(id),
  circle_ids TEXT NOT NULL, preauth_key TEXT NOT NULL, preauth_key_id INTEGER NOT NULL DEFAULT 0, sealed_keys TEXT NOT NULL,
  created_at TEXT NOT NULL, expires_at TEXT NOT NULL, consumed_at TEXT, consumed_ip TEXT NOT NULL DEFAULT '', revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS key_grant (
  id INTEGER PRIMARY KEY, device_id INTEGER NOT NULL REFERENCES device(id) ON DELETE CASCADE, circle_id INTEGER NOT NULL REFERENCES circle(id) ON DELETE CASCADE,
  generation INTEGER NOT NULL, sealed_box TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(device_id, circle_id, generation));
CREATE TABLE IF NOT EXISTS heartbeat (
  id INTEGER PRIMARY KEY, device_id INTEGER NOT NULL REFERENCES device(id) ON DELETE CASCADE, ts TEXT NOT NULL, body TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS heartbeat_dev ON heartbeat(device_id, ts);
CREATE TABLE IF NOT EXISTS audit_log (
  id INTEGER PRIMARY KEY, ts TEXT NOT NULL, operator TEXT NOT NULL, action TEXT NOT NULL, target TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS event (
  id INTEGER PRIMARY KEY, ts TEXT NOT NULL, kind TEXT NOT NULL, person_id INTEGER NOT NULL DEFAULT 0, device_id INTEGER NOT NULL DEFAULT 0, detail TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS release (
  os TEXT NOT NULL, arch TEXT NOT NULL, version TEXT NOT NULL, file TEXT NOT NULL, sha256 TEXT NOT NULL, sig TEXT NOT NULL, published_at TEXT NOT NULL, PRIMARY KEY(os, arch));
`

func Open(path string) (*DB, error) {
	d, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	if _, err := d.Exec(schema); err != nil {
		return nil, err
	}
	// additive migrations
	_, _ = d.Exec(`ALTER TABLE circle ADD COLUMN invite_policy TEXT NOT NULL DEFAULT 'members'`)
	_, _ = d.Exec(`ALTER TABLE invite ADD COLUMN inviter_name TEXT NOT NULL DEFAULT ''`)
	_, _ = d.Exec(`ALTER TABLE person ADD COLUMN display_name TEXT NOT NULL DEFAULT ''`)
	_, _ = d.Exec(`ALTER TABLE circle ADD COLUMN owner_person_id INTEGER NOT NULL DEFAULT 0`)
	// audit_log is append-only (FR-93): forbid UPDATE/DELETE at the engine level.
	_, _ = d.Exec(`CREATE TRIGGER IF NOT EXISTS audit_no_update BEFORE UPDATE ON audit_log BEGIN SELECT RAISE(ABORT,'audit_log is append-only'); END;`)
	_, _ = d.Exec(`CREATE TRIGGER IF NOT EXISTS audit_no_delete BEFORE DELETE ON audit_log BEGIN SELECT RAISE(ABORT,'audit_log is append-only'); END;`)
	return &DB{d}, nil
}

func now() string           { return time.Now().UTC().Format(time.RFC3339) }
func ts(s string) time.Time { t, _ := time.Parse(time.RFC3339, s); return t }
func tsp(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := ts(s.String)
	return &t
}

var ErrNotFound = errors.New("not found")

// --- settings ---

func (d *DB) Setting(k string) string {
	var v string
	_ = d.QueryRow(`SELECT value FROM settings WHERE key=?`, k).Scan(&v)
	return v
}
func (d *DB) SetSetting(k, v string) error {
	_, err := d.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v)
	return err
}

// --- audit + events ---

func (d *DB) Audit(operator, action, target, detail string) {
	_, _ = d.Exec(`INSERT INTO audit_log(ts,operator,action,target,detail) VALUES(?,?,?,?,?)`, now(), operator, action, target, detail)
}
func (d *DB) AuditList(limit int) ([]model.AuditEntry, error) {
	rows, err := d.Query(`SELECT id,ts,operator,action,target,detail FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AuditEntry
	for rows.Next() {
		var e model.AuditEntry
		var t string
		if err := rows.Scan(&e.ID, &t, &e.Operator, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		e.Timestamp = ts(t)
		out = append(out, e)
	}
	return out, nil
}
func (d *DB) Event(kind string, personID, deviceID int64, detail string) {
	_, _ = d.Exec(`INSERT INTO event(ts,kind,person_id,device_id,detail) VALUES(?,?,?,?,?)`, now(), kind, personID, deviceID, detail)
}
func (d *DB) Events(personID int64, limit int) ([]model.Event, error) {
	q := `SELECT id,ts,kind,person_id,device_id,detail FROM event`
	args := []any{}
	if personID > 0 {
		q += ` WHERE person_id=?`
		args = append(args, personID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		var t string
		if err := rows.Scan(&e.ID, &t, &e.Kind, &e.PersonID, &e.DeviceID, &e.Detail); err != nil {
			return nil, err
		}
		e.Timestamp = ts(t)
		out = append(out, e)
	}
	return out, nil
}

// --- persons ---

func scanPerson(r interface{ Scan(...any) error }) (model.Person, error) {
	var p model.Person
	var c string
	err := r.Scan(&p.ID, &p.Name, &p.Email, &p.Household, &p.HSUser, &p.HSUserID, &p.Status, &c, &p.DisplayName)
	p.CreatedAt = ts(c)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

const personCols = `id,name,email,household,hs_user,hs_user_id,status,created_at,display_name`

func (d *DB) PersonAdd(p model.Person) (model.Person, error) {
	res, err := d.Exec(`INSERT INTO person(name,email,household,hs_user,hs_user_id,status,created_at,display_name) VALUES(?,?,?,?,?,?,?,?)`,
		p.Name, p.Email, p.Household, p.HSUser, p.HSUserID, model.StatusActive, now(), p.DisplayName)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return p, fmt.Errorf("person %q already exists", p.Name)
		}
		return p, err
	}
	p.ID, _ = res.LastInsertId()
	return d.PersonByID(p.ID)
}
func (d *DB) PersonByName(name string) (model.Person, error) {
	return scanPerson(d.QueryRow(`SELECT `+personCols+` FROM person WHERE name=?`, name))
}
func (d *DB) PersonByID(id int64) (model.Person, error) {
	return scanPerson(d.QueryRow(`SELECT `+personCols+` FROM person WHERE id=?`, id))
}
func (d *DB) Persons() ([]model.Person, error) {
	rows, err := d.Query(`SELECT ` + personCols + ` FROM person ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Person
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
func (d *DB) PersonSetStatus(id int64, status string) error {
	_, err := d.Exec(`UPDATE person SET status=? WHERE id=?`, status, id)
	return err
}
func (d *DB) PersonSetHSUserID(id, hsID int64) error {
	_, err := d.Exec(`UPDATE person SET hs_user_id=? WHERE id=?`, hsID, id)
	return err
}
func (d *DB) PersonDelete(id int64) error {
	if _, err := d.Exec(`DELETE FROM membership WHERE person_id=?`, id); err != nil {
		return err
	}
	if _, err := d.Exec(`DELETE FROM invite WHERE person_id=?`, id); err != nil {
		return err
	}
	if _, err := d.Exec(`DELETE FROM device WHERE person_id=?`, id); err != nil {
		return err
	}
	_, err := d.Exec(`DELETE FROM person WHERE id=?`, id)
	return err
}

// --- circles ---

const circleCols = `id,slug,display_name,bucket_prefix,generation,quota_bytes,sync_mode,version_retention_days,excludes,bwlimit,s3_access_key,s3_secret_key,created_at,invite_policy,owner_person_id`

type circleRow struct {
	model.Circle
	S3AccessKey, S3SecretKey string
}

func scanCircle(r interface{ Scan(...any) error }) (circleRow, error) {
	var c circleRow
	var ex, created string
	err := r.Scan(&c.ID, &c.Slug, &c.DisplayName, &c.BucketPrefix, &c.Generation, &c.QuotaBytes, &c.SyncMode, &c.VersionRetentionDays, &ex, &c.BwLimit, &c.S3AccessKey, &c.S3SecretKey, &created, &c.InvitePolicy, &c.OwnerPersonID)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	_ = json.Unmarshal([]byte(ex), &c.Excludes)
	c.CreatedAt = ts(created)
	return c, nil
}

func (d *DB) CircleCreate(c model.Circle, s3Access, s3Secret string) (model.Circle, error) {
	ex, _ := json.Marshal(c.Excludes)
	if c.VersionRetentionDays == 0 {
		c.VersionRetentionDays = model.DefaultRetention
	}
	if c.SyncMode == "" {
		c.SyncMode = model.ModeBidirectional
	}
	if c.InvitePolicy == "" {
		c.InvitePolicy = model.InviteByMembers
	}
	res, err := d.Exec(`INSERT INTO circle(slug,display_name,bucket_prefix,generation,quota_bytes,sync_mode,version_retention_days,excludes,bwlimit,s3_access_key,s3_secret_key,created_at,invite_policy,owner_person_id) VALUES(?,?,?,1,?,?,?,?,?,?,?,?,?,?)`,
		c.Slug, c.DisplayName, c.BucketPrefix, c.QuotaBytes, c.SyncMode, c.VersionRetentionDays, string(ex), c.BwLimit, s3Access, s3Secret, now(), c.InvitePolicy, c.OwnerPersonID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return c, fmt.Errorf("circle %q already exists", c.Slug)
		}
		return c, err
	}
	c.ID, _ = res.LastInsertId()
	cr, err := d.circleByID(c.ID)
	return cr.Circle, err
}
func (d *DB) circleByID(id int64) (circleRow, error) {
	return scanCircle(d.QueryRow(`SELECT `+circleCols+` FROM circle WHERE id=?`, id))
}
func (d *DB) CircleBySlug(slug string) (model.Circle, error) {
	c, err := scanCircle(d.QueryRow(`SELECT `+circleCols+` FROM circle WHERE slug=?`, slug))
	return c.Circle, err
}
func (d *DB) CircleByID(id int64) (model.Circle, error) {
	c, err := d.circleByID(id)
	return c.Circle, err
}
func (d *DB) CircleS3(id int64) (access, secret string, err error) {
	c, err := d.circleByID(id)
	return c.S3AccessKey, c.S3SecretKey, err
}
func (d *DB) CircleSetS3(id int64, access, secret string) error {
	_, err := d.Exec(`UPDATE circle SET s3_access_key=?, s3_secret_key=? WHERE id=?`, access, secret, id)
	return err
}
func (d *DB) Circles() ([]model.Circle, error) {
	rows, err := d.Query(`SELECT ` + circleCols + ` FROM circle ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Circle
	for rows.Next() {
		c, err := scanCircle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c.Circle)
	}
	return out, nil
}
func (d *DB) CircleUpdate(c model.Circle) error {
	ex, _ := json.Marshal(c.Excludes)
	if c.InvitePolicy == "" {
		c.InvitePolicy = model.InviteByMembers
	}
	_, err := d.Exec(`UPDATE circle SET display_name=?,quota_bytes=?,sync_mode=?,version_retention_days=?,excludes=?,bwlimit=?,invite_policy=?,owner_person_id=? WHERE id=?`,
		c.DisplayName, c.QuotaBytes, c.SyncMode, c.VersionRetentionDays, string(ex), c.BwLimit, c.InvitePolicy, c.OwnerPersonID, c.ID)
	return err
}
func (d *DB) CircleSetGeneration(id int64, gen int) error {
	_, err := d.Exec(`UPDATE circle SET generation=? WHERE id=?`, gen, id)
	return err
}
func (d *DB) CircleDelete(id int64) error {
	_, err := d.Exec(`DELETE FROM circle WHERE id=?`, id)
	return err
}

// --- memberships ---

func (d *DB) MemberAdd(personID, circleID int64, role string) error {
	if role == "" {
		role = "member"
	}
	_, err := d.Exec(`INSERT INTO membership(person_id,circle_id,role,added_at) VALUES(?,?,?,?) ON CONFLICT DO UPDATE SET role=excluded.role`, personID, circleID, role, now())
	return err
}
func (d *DB) MemberRemove(personID, circleID int64) error {
	_, err := d.Exec(`DELETE FROM membership WHERE person_id=? AND circle_id=?`, personID, circleID)
	return err
}
func (d *DB) MembersOf(circleID int64) ([]model.Membership, error) {
	rows, err := d.Query(`SELECT m.person_id,p.name,m.circle_id,m.role,m.added_at FROM membership m JOIN person p ON p.id=m.person_id WHERE m.circle_id=? ORDER BY p.name`, circleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Membership
	for rows.Next() {
		var m model.Membership
		var a string
		if err := rows.Scan(&m.PersonID, &m.PersonName, &m.CircleID, &m.Role, &a); err != nil {
			return nil, err
		}
		m.AddedAt = ts(a)
		out = append(out, m)
	}
	return out, nil
}
func (d *DB) CirclesOf(personID int64) ([]model.Membership, error) {
	rows, err := d.Query(`SELECT m.person_id,p.name,m.circle_id,m.role,m.added_at FROM membership m JOIN person p ON p.id=m.person_id WHERE m.person_id=?`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Membership
	for rows.Next() {
		var m model.Membership
		var a string
		if err := rows.Scan(&m.PersonID, &m.PersonName, &m.CircleID, &m.Role, &a); err != nil {
			return nil, err
		}
		m.AddedAt = ts(a)
		out = append(out, m)
	}
	return out, nil
}

// --- devices ---

const deviceCols = `id,person_id,hostname,os,arch,agent_version,hs_node_id,tailnet_ip,pubkey,enrolled_at,last_heartbeat,status`

func scanDevice(r interface{ Scan(...any) error }) (model.Device, error) {
	var v model.Device
	var e, h string
	err := r.Scan(&v.ID, &v.PersonID, &v.Hostname, &v.OS, &v.Arch, &v.AgentVersion, &v.HSNodeID, &v.TailnetIP, &v.PubKey, &e, &h, &v.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	v.EnrolledAt, v.LastHeartbeat = ts(e), ts(h)
	return v, err
}
func (d *DB) DeviceAdd(v model.Device, tokenHash string) (model.Device, error) {
	res, err := d.Exec(`INSERT INTO device(person_id,hostname,os,arch,agent_version,hs_node_id,tailnet_ip,pubkey,token_hash,enrolled_at,status) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		v.PersonID, v.Hostname, v.OS, v.Arch, v.AgentVersion, v.HSNodeID, v.TailnetIP, v.PubKey, tokenHash, now(), model.StatusActive)
	if err != nil {
		return v, err
	}
	v.ID, _ = res.LastInsertId()
	return d.DeviceByID(v.ID)
}
func (d *DB) DeviceByID(id int64) (model.Device, error) {
	return scanDevice(d.QueryRow(`SELECT `+deviceCols+` FROM device WHERE id=?`, id))
}
func (d *DB) DeviceByToken(tokenHash string) (model.Device, error) {
	return scanDevice(d.QueryRow(`SELECT `+deviceCols+` FROM device WHERE token_hash=?`, tokenHash))
}
func (d *DB) Devices(personID int64) ([]model.Device, error) {
	q := `SELECT ` + deviceCols + ` FROM device`
	var args []any
	if personID > 0 {
		q += ` WHERE person_id=?`
		args = append(args, personID)
	}
	rows, err := d.Query(q+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Device
	for rows.Next() {
		v, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
func (d *DB) DeviceSetStatus(id int64, status string) error {
	_, err := d.Exec(`UPDATE device SET status=? WHERE id=?`, status, id)
	return err
}
func (d *DB) DeviceTouch(id int64, agentVersion, tailnetIP string, hsNodeID int64) error {
	_, err := d.Exec(`UPDATE device SET last_heartbeat=?, agent_version=?, tailnet_ip=CASE WHEN ?='' THEN tailnet_ip ELSE ? END, hs_node_id=CASE WHEN ?=0 THEN hs_node_id ELSE ? END WHERE id=?`,
		now(), agentVersion, tailnetIP, tailnetIP, hsNodeID, hsNodeID, id)
	return err
}
func (d *DB) DeviceDelete(id int64) error {
	_, err := d.Exec(`DELETE FROM device WHERE id=?`, id)
	return err
}

// --- invites ---

type InviteRow struct {
	model.Invite
	PreAuthKey   string
	PreAuthKeyID int64
	SealedKeys   string
	Revoked      bool
	ConsumedIP   string
}

const inviteCols = `i.id,i.code_hash,i.prefix,i.person_id,p.name,i.circle_ids,i.preauth_key,i.preauth_key_id,i.sealed_keys,i.created_at,i.expires_at,i.consumed_at,i.consumed_ip,i.revoked,i.inviter_name`

func scanInvite(r interface{ Scan(...any) error }) (InviteRow, error) {
	var v InviteRow
	var cids, c, e string
	var consumed sql.NullString
	var rev int
	err := r.Scan(&v.ID, &v.CodeHash, &v.Prefix, &v.PersonID, &v.PersonName, &cids, &v.PreAuthKey, &v.PreAuthKeyID, &v.SealedKeys, &c, &e, &consumed, &v.ConsumedIP, &rev, &v.InviterName)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	_ = json.Unmarshal([]byte(cids), &v.CircleIDs)
	v.CreatedAt, v.ExpiresAt, v.ConsumedAt, v.Revoked = ts(c), ts(e), tsp(consumed), rev == 1
	return v, nil
}
func (d *DB) InviteAdd(v InviteRow) (InviteRow, error) {
	cids, _ := json.Marshal(v.CircleIDs)
	res, err := d.Exec(`INSERT INTO invite(code_hash,prefix,person_id,circle_ids,preauth_key,preauth_key_id,sealed_keys,created_at,expires_at,inviter_name) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		v.CodeHash, v.Prefix, v.PersonID, string(cids), v.PreAuthKey, v.PreAuthKeyID, v.SealedKeys, now(), v.ExpiresAt.UTC().Format(time.RFC3339), v.InviterName)
	if err != nil {
		return v, err
	}
	v.ID, _ = res.LastInsertId()
	return d.inviteWhere(`i.id=?`, v.ID)
}
func (d *DB) inviteWhere(where string, args ...any) (InviteRow, error) {
	return scanInvite(d.QueryRow(`SELECT `+inviteCols+` FROM invite i JOIN person p ON p.id=i.person_id WHERE `+where, args...))
}
func (d *DB) InviteByHash(h string) (InviteRow, error) { return d.inviteWhere(`i.code_hash=?`, h) }
func (d *DB) InviteByPrefix(prefix string) (InviteRow, error) {
	return d.inviteWhere(`i.prefix=? AND i.consumed_at IS NULL AND i.revoked=0`, prefix)
}
func (d *DB) Invites() ([]InviteRow, error) {
	rows, err := d.Query(`SELECT ` + inviteCols + ` FROM invite i JOIN person p ON p.id=i.person_id ORDER BY i.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InviteRow
	for rows.Next() {
		v, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// InviteConsume marks the code used, atomically: returns false if it was already consumed, revoked or expired
// (FR-101/104). The returned row carries the sealed keys one last time; the stored copy is wiped.
func (d *DB) InviteConsume(ctx context.Context, h, ip string) (InviteRow, bool, error) {
	var sealed string
	if err := d.QueryRowContext(ctx, `SELECT sealed_keys FROM invite WHERE code_hash=? AND consumed_at IS NULL AND revoked=0 AND expires_at>?`, h, now()).Scan(&sealed); err != nil {
		return InviteRow{}, false, nil
	}
	res, err := d.ExecContext(ctx, `UPDATE invite SET consumed_at=?, consumed_ip=?, sealed_keys='' WHERE code_hash=? AND consumed_at IS NULL AND revoked=0 AND expires_at>?`, now(), ip, h, now())
	if err != nil {
		return InviteRow{}, false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return InviteRow{}, false, nil
	}
	v, err := d.InviteByHash(h)
	if err != nil {
		return v, false, err
	}
	v.SealedKeys = sealed
	return v, true, nil
}

// InvitePeek returns the row only if it is still live (for the invite page and the installer script, which do not consume).
func (d *DB) InvitePeek(h string) (InviteRow, bool) {
	v, err := d.InviteByHash(h)
	if err != nil || v.Revoked || v.ConsumedAt != nil || time.Now().After(v.ExpiresAt) {
		return v, false
	}
	return v, true
}
func (d *DB) InviteRevoke(id int64) error {
	_, err := d.Exec(`UPDATE invite SET revoked=1, sealed_keys='' WHERE id=?`, id)
	return err
}

// --- key grants ---

func (d *DB) GrantPut(g model.KeyGrant) error {
	_, err := d.Exec(`INSERT INTO key_grant(device_id,circle_id,generation,sealed_box,created_at) VALUES(?,?,?,?,?) ON CONFLICT(device_id,circle_id,generation) DO UPDATE SET sealed_box=excluded.sealed_box, created_at=excluded.created_at`,
		g.DeviceID, g.CircleID, g.Generation, g.SealedBox, now())
	return err
}
func (d *DB) GrantsFor(deviceID int64) ([]model.KeyGrant, error) {
	rows, err := d.Query(`SELECT g.id,g.device_id,g.circle_id,c.slug,g.generation,g.sealed_box,g.created_at FROM key_grant g JOIN circle c ON c.id=g.circle_id WHERE g.device_id=? AND g.generation=c.generation`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.KeyGrant
	for rows.Next() {
		var g model.KeyGrant
		var c string
		if err := rows.Scan(&g.ID, &g.DeviceID, &g.CircleID, &g.Slug, &g.Generation, &g.SealedBox, &c); err != nil {
			return nil, err
		}
		g.CreatedAt = ts(c)
		out = append(out, g)
	}
	return out, nil
}
func (d *DB) GrantsDeleteForDevice(deviceID int64) error {
	_, err := d.Exec(`DELETE FROM key_grant WHERE device_id=?`, deviceID)
	return err
}
func (d *DB) GrantsDeleteForCircleBelow(circleID int64, gen int) error {
	_, err := d.Exec(`DELETE FROM key_grant WHERE circle_id=? AND generation<?`, circleID, gen)
	return err
}
func (d *DB) GrantsDeleteForDeviceCircle(deviceID, circleID int64) error {
	_, err := d.Exec(`DELETE FROM key_grant WHERE device_id=? AND circle_id=?`, deviceID, circleID)
	return err
}
func (d *DB) GrantExists(deviceID, circleID int64, gen int) bool {
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM key_grant WHERE device_id=? AND circle_id=? AND generation=?`, deviceID, circleID, gen).Scan(&n)
	return n > 0
}

// --- heartbeats ---

func (d *DB) HeartbeatAdd(deviceID int64, hb model.Heartbeat) error {
	b, _ := json.Marshal(hb)
	if _, err := d.Exec(`INSERT INTO heartbeat(device_id,ts,body) VALUES(?,?,?)`, deviceID, now(), string(b)); err != nil {
		return err
	}
	// keep 7 days per device
	_, _ = d.Exec(`DELETE FROM heartbeat WHERE device_id=? AND ts<?`, deviceID, time.Now().Add(-7*24*time.Hour).UTC().Format(time.RFC3339))
	return nil
}
func (d *DB) HeartbeatLatest(deviceID int64) (model.Heartbeat, bool) {
	var b string
	if err := d.QueryRow(`SELECT body FROM heartbeat WHERE device_id=? ORDER BY id DESC LIMIT 1`, deviceID).Scan(&b); err != nil {
		return model.Heartbeat{}, false
	}
	var hb model.Heartbeat
	_ = json.Unmarshal([]byte(b), &hb)
	return hb, true
}
func (d *DB) Heartbeats(deviceID int64, limit int) ([]model.Heartbeat, error) {
	rows, err := d.Query(`SELECT body FROM heartbeat WHERE device_id=? ORDER BY id DESC LIMIT ?`, deviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Heartbeat
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var hb model.Heartbeat
		_ = json.Unmarshal([]byte(b), &hb)
		out = append(out, hb)
	}
	return out, nil
}

// --- releases (NFR-40) ---

type Release struct {
	OS, Arch, Version, File, SHA256, Sig string
	PublishedAt                        time.Time
}

func (d *DB) ReleasePut(r Release) error {
	_, err := d.Exec(`INSERT INTO release(os,arch,version,file,sha256,sig,published_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(os,arch) DO UPDATE SET version=excluded.version,file=excluded.file,sha256=excluded.sha256,sig=excluded.sig,published_at=excluded.published_at`,
		r.OS, r.Arch, r.Version, r.File, r.SHA256, r.Sig, now())
	return err
}
func (d *DB) ReleaseGet(os, arch string) (Release, bool) {
	var r Release
	var p string
	if err := d.QueryRow(`SELECT os,arch,version,file,sha256,sig,published_at FROM release WHERE os=? AND arch=?`, os, arch).Scan(&r.OS, &r.Arch, &r.Version, &r.File, &r.SHA256, &r.Sig, &p); err != nil {
		return r, false
	}
	r.PublishedAt = ts(p)
	return r, true
}
