// Package model holds the types shared by the hub, qpctl and the agent.
package model

import "time"

type Person struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name,omitempty"` // what they typed ("Austin Lee"); Name is the slug
	Email       string    `json:"email"`
	Household   string    `json:"household,omitempty"`
	HSUser      string    `json:"hs_user"` // headscale user name
	HSUserID    int64     `json:"hs_user_id"`
	Status      string    `json:"status"` // active | offboarded
	CreatedAt   time.Time `json:"created_at"`
}

type Device struct {
	ID            int64     `json:"id"`
	PersonID      int64     `json:"person_id"`
	Hostname      string    `json:"hostname"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	AgentVersion  string    `json:"agent_version"`
	HSNodeID      int64     `json:"headscale_node_id"`
	TailnetIP     string    `json:"tailnet_ip"`
	PubKey        string    `json:"pubkey"` // base64 x25519, for sealed key grants
	EnrolledAt    time.Time `json:"enrolled_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	Status        string    `json:"status"` // active | revoked | needs_reprovision
}

type Circle struct {
	ID                   int64     `json:"id"`
	Slug                 string    `json:"slug"`
	DisplayName          string    `json:"display_name"`
	BucketPrefix         string    `json:"bucket_prefix"` // garage bucket name
	Generation           int       `json:"generation"`    // key generation; objects live under g<N>/
	QuotaBytes           int64     `json:"quota_bytes"`
	SyncMode             string    `json:"sync_mode"` // bidirectional | receive-only | send-only
	VersionRetentionDays int       `json:"version_retention_days"`
	Excludes             []string  `json:"excludes,omitempty"`
	BwLimit              string    `json:"bwlimit,omitempty"` // rclone --bwlimit value or timetable
	InvitePolicy         string    `json:"invite_policy"`     // members (any read/write member can invite) | operator
	OwnerPersonID        int64     `json:"owner_person_id"`   // member who may remove people from their own computer (0 = operator only)
	CreatedAt            time.Time `json:"created_at"`
	// filled for show
	Members   []Membership `json:"members,omitempty"`
	UsedBytes int64        `json:"used_bytes,omitempty"`
}

type Membership struct {
	PersonID   int64     `json:"person_id"`
	PersonName string    `json:"person_name,omitempty"`
	CircleID   int64     `json:"circle_id"`
	Role       string    `json:"role"` // member | readonly
	AddedAt    time.Time `json:"added_at"`
}

type Invite struct {
	ID          int64      `json:"id"`
	CodeHash    string     `json:"code_hash"`
	PersonID    int64      `json:"person_id"`
	PersonName  string     `json:"person_name,omitempty"`
	InviterName string     `json:"inviter_name,omitempty"` // who made the link: a member's display name, or the operator
	CircleIDs   []int64    `json:"circle_ids"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	ConsumedAt  *time.Time `json:"consumed_at,omitempty"`
	Prefix      string     `json:"prefix,omitempty"` // first 6 chars of the code, for `invite list`/`revoke`
}

// CircleKey is the material a member needs to open one generation of one circle.
type CircleKey struct {
	Slug       string `json:"slug"`
	Generation int    `json:"generation"`
	Password   string `json:"password"` // rclone crypt password (raw, not obscured)
	Salt       string `json:"salt"`     // rclone crypt password2
}

// KeyGrant is a circle key sealed to one device's public key. The hub stores and relays it, and cannot open it.
type KeyGrant struct {
	ID         int64     `json:"id"`
	DeviceID   int64     `json:"device_id"`
	CircleID   int64     `json:"circle_id"`
	Slug       string    `json:"slug"`
	Generation int       `json:"generation"`
	SealedBox  string    `json:"sealed_box"` // base64 nacl SealAnonymous(JSON CircleKey)
	CreatedAt  time.Time `json:"created_at"`
}

// CircleConfig is what the agent receives per circle in the config bundle. No key material here.
type CircleConfig struct {
	ID                   int64    `json:"id"`
	Slug                 string   `json:"slug"`
	DisplayName          string   `json:"display_name"`
	Bucket               string   `json:"bucket"`
	Generation           int      `json:"generation"`
	SyncMode             string   `json:"sync_mode"`
	Role                 string   `json:"role"`
	QuotaBytes           int64    `json:"quota_bytes"`
	UsedBytes            int64    `json:"used_bytes"`
	VersionRetentionDays int      `json:"version_retention_days"`
	Excludes             []string `json:"excludes,omitempty"`
	BwLimit              string   `json:"bwlimit,omitempty"`
	CanInvite            bool     `json:"can_invite"` // this member may create invite links for the circle
	Owner                bool     `json:"owner"`      // this member may remove people and re-key from their computer
	S3AccessKey          string   `json:"s3_access_key"`
	S3SecretKey          string   `json:"s3_secret_key"`
}

// ConfigBundle is returned on enrol and on every heartbeat.
type ConfigBundle struct {
	ServerTime     time.Time      `json:"server_time"`
	S3Endpoint     string         `json:"s3_endpoint"` // http://<hub tailnet ip>:3900
	SyncInterval   int            `json:"sync_interval_seconds"`
	Circles        []CircleConfig `json:"circles"`
	Grants         []KeyGrant     `json:"grants"`
	DeviceStatus   string         `json:"device_status"`
	SupportContact string         `json:"support_contact"`
	Update         *UpdateInfo    `json:"update,omitempty"`
}

type UpdateInfo struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Sig     string `json:"sig"` // base64 ed25519 over sha256 hex
}

type Heartbeat struct {
	DeviceID       int64                   `json:"device_id"`
	Timestamp      time.Time               `json:"timestamp"`
	AgentVersion   string                  `json:"agent_version"`
	OS             string                  `json:"os"`
	ConnectionType string                  `json:"connection_type"` // direct | relayed | down
	PerCircle      map[string]CircleHealth `json:"per_circle"`
	PendingBytes   int64                   `json:"pending_bytes"`
	FreeDisk       int64                   `json:"free_disk"`
	ErrorCount     int                     `json:"error_count"`
	Conditions     []string                `json:"conditions,omitempty"` // quota_exceeded:<slug>, path_too_long:<slug>:<n>, corrupt_state:<slug>, sync_refused:<slug>, sync_failing:<slug>, clock_skew, disk_low
	ClientTime     time.Time               `json:"client_time"`
}

type CircleHealth struct {
	LastSync   time.Time `json:"last_sync"`
	LastError  string    `json:"last_error,omitempty"`
	Pending    int64     `json:"pending_bytes"`
	Generation int       `json:"generation"`
	Failures   int       `json:"failures,omitempty"` // consecutive failed cycles
}

type EnrolRequest struct {
	InviteCode   string `json:"invite_code"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agent_version"`
	TailnetIP    string `json:"tailnet_ip"`
	PubKey       string `json:"pubkey"`
}

// DeviceInviteRequest: a member device creates an invite for a circle it belongs to. The keys are sealed on the device
// under the code; the hub receives only the hash and the sealed blob (same zero-knowledge shape as operator invites).
type DeviceInviteRequest struct {
	CircleID   int64  `json:"circle_id"`
	Name       string `json:"name"` // what the inviter calls the person; no email, no account
	CodeHash   string `json:"code_hash"`
	Prefix     string `json:"prefix"`
	SealedKeys string `json:"sealed_keys"`
	TTL        string `json:"ttl"`
}

// CirclePerson is what an owner sees on the Share page.
type CirclePerson struct {
	PersonID int64    `json:"person_id"`
	Name     string   `json:"name"`
	Role     string   `json:"role"`
	Self     bool     `json:"self"`
	Devices  []Device `json:"devices"`
}

type DeviceInviteResponse struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
	Person    string    `json:"person"`
}

type EnrolResponse struct {
	DeviceID    int64        `json:"device_id"`
	DeviceToken string       `json:"device_token"`
	Config      ConfigBundle `json:"config"`
}

// InvitePayload is what the installer downloads (after the invite page). Circle keys are inside
// SealedKeys, encrypted with a key derived from the invite code, which the hub only holds hashed.
type InvitePayload struct {
	InviterName    string   `json:"inviter_name,omitempty"`
	LoginServer    string   `json:"login_server"`
	PreAuthKey     string   `json:"preauth_key"`
	HubAPI         string   `json:"hub_api"` // http://<tailnet ip>:8443
	SupportContact string   `json:"support_contact"`
	OperatorName   string   `json:"operator_name"`
	Circles        []string `json:"circles"`     // display names, for folder creation before first sync
	SealedKeys     string   `json:"sealed_keys"` // base64 chacha20poly1305(JSON []CircleKey) under HKDF(code)
	AgentVersion   string   `json:"agent_version"`
	// self-serve start (open signup): the device creates the key for this circle itself
	NewCircleID   int64  `json:"new_circle_id,omitempty"`
	NewCircleSlug string `json:"new_circle_slug,omitempty"`
	Code          string `json:"code,omitempty"` // only in /j/new responses: the enrolment code the hub minted
}

// JoinRequest: a device that already has Quietport redeems an invite link for the person it belongs to. No new
// person, no new device, no reinstall (docs/adr/0019).
type JoinRequest struct {
	Code string `json:"code"`
}

// JoinResponse carries what the joining device needs: the folders it is now in (with storage credentials, the same
// shape a heartbeat sends), the circle keys the inviter sealed under the code, and the joiner's own other computers
// so the device can seal the key to them. It never names anyone else.
type JoinResponse struct {
	InviterName  string         `json:"inviter_name,omitempty"`
	Circles      []CircleConfig `json:"circles"`
	SealedKeys   string         `json:"sealed_keys"`
	OtherDevices []DeviceKey    `json:"other_devices"`
}

// DeviceKey is a device as a sealing target: its id and its public key, nothing else.
type DeviceKey struct {
	ID     int64  `json:"id"`
	PubKey string `json:"pubkey"`
}

type AuditEntry struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Operator  string    `json:"operator"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail"`
}

type Event struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Kind      string    `json:"kind"`
	PersonID  int64     `json:"person_id"`
	DeviceID  int64     `json:"device_id"`
	Detail    string    `json:"detail"`
}

const (
	StatusActive       = "active"
	StatusRevoked      = "revoked"
	StatusReprovision  = "needs_reprovision"
	StatusOffboarded   = "offboarded"
	ModeBidirectional  = "bidirectional"
	ModeReceiveOnly    = "receive-only"
	ModeSendOnly       = "send-only"
	InviteByMembers    = "members"
	InviteByOperator   = "operator"
	DefaultRetention   = 30
	HeartbeatInterval  = 5 * time.Minute
	DueInterval        = 30 * time.Second // how often an agent asks whether a grant waits for it (docs/adr/0028)
	DefaultSyncSeconds = 60
)
