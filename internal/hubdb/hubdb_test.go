package hubdb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/model"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// A fresh database must come up with every migration applied: the columns later code reads (invite.inviter_name,
// person.display_name, circle.owner_person_id) exist and round-trip.
func TestOpenAppliesMigrations(t *testing.T) {
	d := openTest(t)
	p, err := d.PersonAdd(model.Person{Name: "sam-k3q7", DisplayName: "Sam", Household: "home", HSUser: "sam-k3q7"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.PersonByID(p.ID)
	if err != nil || got.DisplayName != "Sam" || got.Status != model.StatusActive {
		t.Fatalf("person round trip: %+v %v", got, err)
	}
	c, err := d.CircleCreate(model.Circle{Slug: "trip-abcd", DisplayName: "Trip", BucketPrefix: "qp-trip-abcd", QuotaBytes: 5 << 30,
		SyncMode: model.ModeBidirectional, InvitePolicy: model.InviteByMembers, OwnerPersonID: p.ID}, "ak", "sk")
	if err != nil {
		t.Fatal(err)
	}
	back, err := d.CircleByID(c.ID)
	if err != nil || back.OwnerPersonID != p.ID || back.InvitePolicy != model.InviteByMembers {
		t.Fatalf("circle round trip: %+v %v", back, err)
	}
	// opening the same file again must be a no-op, not a failed migration
	d2, err := Open(filepath.Join(filepath.Dir(dbPath(t, d)), "hub.db"))
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	_ = d2.Close()
}

func dbPath(t *testing.T, d *DB) string {
	t.Helper()
	var name, file string
	if err := d.QueryRow(`PRAGMA database_list`).Scan(new(int), &name, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

// An invite hands out its sealed keys exactly once; a second consume, a wrong hash and a revoked or expired row all
// return nothing (docs/adr/0001).
func TestInviteConsumedOnce(t *testing.T) {
	d := openTest(t)
	p, _ := d.PersonAdd(model.Person{Name: "guest-zz11", DisplayName: "Guest", HSUser: "guest-zz11"})
	code := cryptobox.NewInviteCode()
	inv, err := d.InviteAdd(InviteRow{Invite: model.Invite{CodeHash: cryptobox.HashToken(code), PersonID: p.ID, CircleIDs: []int64{1},
		ExpiresAt: time.Now().Add(time.Hour), Prefix: code[:6], InviterName: "Sam"}, PreAuthKey: "pak", SealedKeys: "sealed-blob"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.InviterName != "Sam" {
		t.Fatalf("inviter name not stored: %+v", inv)
	}
	peek, live := d.InvitePeek(cryptobox.HashToken(code))
	if !live || peek.InviterName != "Sam" || peek.PersonName != "guest-zz11" {
		t.Fatalf("peek: %+v live=%v", peek, live)
	}
	row, ok, err := d.InviteConsume(context.Background(), cryptobox.HashToken(code), "203.0.113.5")
	if err != nil || !ok || row.SealedKeys != "sealed-blob" {
		t.Fatalf("first consume: ok=%v err=%v row=%+v", ok, err, row)
	}
	if _, ok, _ := d.InviteConsume(context.Background(), cryptobox.HashToken(code), "203.0.113.5"); ok {
		t.Fatal("second consume succeeded")
	}
	if _, ok, _ := d.InviteConsume(context.Background(), cryptobox.HashToken("not-a-code"), "x"); ok {
		t.Fatal("unknown hash consumed")
	}
	if _, live := d.InvitePeek(cryptobox.HashToken(code)); live {
		t.Fatal("consumed invite still peeks as live")
	}
	// expired
	code2 := cryptobox.NewInviteCode()
	_, _ = d.InviteAdd(InviteRow{Invite: model.Invite{CodeHash: cryptobox.HashToken(code2), PersonID: p.ID, CircleIDs: []int64{1},
		ExpiresAt: time.Now().Add(-time.Minute), Prefix: code2[:6]}, PreAuthKey: "pak", SealedKeys: "s"})
	if _, ok, _ := d.InviteConsume(context.Background(), cryptobox.HashToken(code2), "x"); ok {
		t.Fatal("expired invite consumed")
	}
}

// The audit log is append-only at the database level: updates and deletes must fail even for code with the handle.
func TestAuditLogIsAppendOnly(t *testing.T) {
	d := openTest(t)
	d.Audit("sam", "circle.create", "trip-abcd", "test")
	if _, err := d.Exec(`UPDATE audit_log SET detail='changed'`); err == nil {
		t.Fatal("UPDATE on audit_log succeeded")
	}
	if _, err := d.Exec(`DELETE FROM audit_log`); err == nil {
		t.Fatal("DELETE on audit_log succeeded")
	}
	rows, err := d.AuditList(10)
	if err != nil || len(rows) != 1 || rows[0].Detail != "test" {
		t.Fatalf("audit list: %v %+v", err, rows)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	d := openTest(t)
	if d.Setting("open_signup") != "" {
		t.Fatal("unset setting is not empty")
	}
	if err := d.SetSetting("open_signup", "1"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting("open_signup", "0"); err != nil {
		t.Fatal(err)
	}
	if d.Setting("open_signup") != "0" {
		t.Fatalf("setting = %q, want 0", d.Setting("open_signup"))
	}
}
