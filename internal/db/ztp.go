package db

import (
	"database/sql"
	"errors"
	"time"
)

// ZTP device modes.
const (
	ZTPLocal   = "local"   // reached on the LAN, no tunnel
	ZTPRemote  = "remote"  // pre-provisioned, through the tunnel
	ZTPGeneric = "generic" // called home with a batch's script, unannounced
)

// ZTP device states. A pre-provisioned device goes awaiting → enrolled →
// provisioning → provisioned (or failed); one that calls home unannounced is
// pending until an operator approves it (then as above) or rejects it.
const (
	ZTPAwaiting     = "awaiting"
	ZTPPending      = "pending"
	ZTPEnrolled     = "enrolled"
	ZTPProvisioning = "provisioning"
	ZTPProvisioned  = "provisioned"
	ZTPFailed       = "failed"
	ZTPRejected     = "rejected"
)

// ZTPDevice is one row of ztp_devices. JSON columns are carried as the text
// the server wrote; nothing here interprets them.
type ZTPDevice struct {
	ID         string
	Mode       string
	State      string
	Label      string
	Serial     string
	TokenHash  *string
	BatchID    *string
	ExpiresAt  *int64
	TunnelIP   *string
	PeerKey    *string
	LANFrom    *string
	Secret     *string // sealed by the server; never sent to a browser
	TemplateID *string
	ValuesJSON string
	AckedJSON  string
	SiteIDs    string
	RouterID   *string
	FactsJSON  string
	RunID      *string
	CreatedBy  string // the user ID
	CreatedAt  int64
	FirstSeen  *int64
	LastSeen   *int64
	Error      *string
}

// ZTPBatch is one generic script's batch.
type ZTPBatch struct {
	ID        string
	Name      string
	TokenHash string
	PublicKey string
	ExpiresAt *int64
	RevokedAt *int64
	CreatedBy string // the user ID
	CreatedAt int64
}

// ErrZTPNotFound is a lookup that matched nothing.
var ErrZTPNotFound = errors.New("no such provisioning record")

const ztpDeviceCols = `id, mode, state, label, serial, token_hash, batch_id, expires_at, tunnel_ip,
	peer_key, lan_from, secret, template_id, values_json, acked_json, site_ids, router_id,
	facts_json, run_id, created_by, created_at, first_seen, last_seen, error`

func scanZTPDevice(sc interface{ Scan(...any) error }) (ZTPDevice, error) {
	var d ZTPDevice
	err := sc.Scan(&d.ID, &d.Mode, &d.State, &d.Label, &d.Serial, &d.TokenHash, &d.BatchID, &d.ExpiresAt,
		&d.TunnelIP, &d.PeerKey, &d.LANFrom, &d.Secret, &d.TemplateID, &d.ValuesJSON, &d.AckedJSON,
		&d.SiteIDs, &d.RouterID, &d.FactsJSON, &d.RunID, &d.CreatedBy, &d.CreatedAt, &d.FirstSeen,
		&d.LastSeen, &d.Error)
	return d, err
}

func (d *DB) ready() error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	return nil
}

// CreateZTPDevice inserts a device. CreatedAt is set here when zero.
func (d *DB) CreateZTPDevice(v ZTPDevice) error {
	if err := d.ready(); err != nil {
		return err
	}
	if v.CreatedAt == 0 {
		v.CreatedAt = time.Now().UnixMilli()
	}
	_, err := d.sql.Exec(`INSERT INTO ztp_devices (`+ztpDeviceCols+`)
	    VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.ID, v.Mode, v.State, v.Label, v.Serial, v.TokenHash, v.BatchID, v.ExpiresAt, v.TunnelIP,
		v.PeerKey, v.LANFrom, v.Secret, v.TemplateID, orJSONObject(v.ValuesJSON), orJSONArray(v.AckedJSON),
		orJSONArray(v.SiteIDs), v.RouterID, orJSONObject(v.FactsJSON), v.RunID, v.CreatedBy, v.CreatedAt,
		v.FirstSeen, v.LastSeen, v.Error)
	return err
}

// SaveZTPDevice writes every column of an existing device but its id, mode,
// creator and creation time, which never change.
func (d *DB) SaveZTPDevice(v ZTPDevice) error {
	if err := d.ready(); err != nil {
		return err
	}
	res, err := d.sql.Exec(`UPDATE ztp_devices SET state = ?, label = ?, serial = ?, token_hash = ?,
	    batch_id = ?, expires_at = ?, tunnel_ip = ?, peer_key = ?, lan_from = ?, secret = ?,
	    template_id = ?, values_json = ?, acked_json = ?, site_ids = ?, router_id = ?, facts_json = ?,
	    run_id = ?, first_seen = ?, last_seen = ?, error = ? WHERE id = ?`,
		v.State, v.Label, v.Serial, v.TokenHash, v.BatchID, v.ExpiresAt, v.TunnelIP, v.PeerKey,
		v.LANFrom, v.Secret, v.TemplateID, orJSONObject(v.ValuesJSON), orJSONArray(v.AckedJSON),
		orJSONArray(v.SiteIDs), v.RouterID, orJSONObject(v.FactsJSON), v.RunID, v.FirstSeen, v.LastSeen,
		v.Error, v.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrZTPNotFound
	}
	return nil
}

func (d *DB) ztpDeviceWhere(where string, arg any) (*ZTPDevice, error) {
	if err := d.ready(); err != nil {
		return nil, err
	}
	v, err := scanZTPDevice(d.sql.QueryRow(`SELECT `+ztpDeviceCols+` FROM ztp_devices WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrZTPNotFound
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// ZTPDevice is one device by id.
func (d *DB) ZTPDevice(id string) (*ZTPDevice, error) { return d.ztpDeviceWhere("id = ?", id) }

// ZTPDeviceByToken is the pre-provisioned device a token's hash belongs to.
func (d *DB) ZTPDeviceByToken(hash string) (*ZTPDevice, error) {
	return d.ztpDeviceWhere("token_hash = ?", hash)
}

// ZTPDeviceByPeer is the device holding a WireGuard public key.
func (d *DB) ZTPDeviceByPeer(key string) (*ZTPDevice, error) {
	return d.ztpDeviceWhere("peer_key = ?", key)
}

// ZTPDevices is every device, newest first.
func (d *DB) ZTPDevices() ([]ZTPDevice, error) {
	if err := d.ready(); err != nil {
		return nil, err
	}
	rows, err := d.sql.Query(`SELECT ` + ztpDeviceCols + ` FROM ztp_devices ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ZTPDevice{}
	for rows.Next() {
		v, err := scanZTPDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteZTPDevice removes a device's record. The router it became, if any, is
// the store's; this does not touch it.
func (d *DB) DeleteZTPDevice(id string) error {
	if err := d.ready(); err != nil {
		return err
	}
	_, err := d.sql.Exec(`DELETE FROM ztp_devices WHERE id = ?`, id)
	return err
}

// CreateZTPBatch inserts a batch. CreatedAt is set here when zero.
func (d *DB) CreateZTPBatch(b ZTPBatch) error {
	if err := d.ready(); err != nil {
		return err
	}
	if b.CreatedAt == 0 {
		b.CreatedAt = time.Now().UnixMilli()
	}
	_, err := d.sql.Exec(`INSERT INTO ztp_batches (id, name, token_hash, public_key, expires_at,
	    revoked_at, created_by, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		b.ID, b.Name, b.TokenHash, b.PublicKey, b.ExpiresAt, b.RevokedAt, b.CreatedBy, b.CreatedAt)
	return err
}

const ztpBatchCols = `id, name, token_hash, public_key, expires_at, revoked_at, created_by, created_at`

func scanZTPBatch(sc interface{ Scan(...any) error }) (ZTPBatch, error) {
	var b ZTPBatch
	err := sc.Scan(&b.ID, &b.Name, &b.TokenHash, &b.PublicKey, &b.ExpiresAt, &b.RevokedAt, &b.CreatedBy, &b.CreatedAt)
	return b, err
}

// ZTPBatchByToken is the batch a token's hash belongs to, revoked or not: the
// caller decides what a revoked or expired batch means.
func (d *DB) ZTPBatchByToken(hash string) (*ZTPBatch, error) {
	if err := d.ready(); err != nil {
		return nil, err
	}
	b, err := scanZTPBatch(d.sql.QueryRow(`SELECT `+ztpBatchCols+` FROM ztp_batches WHERE token_hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrZTPNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// ZTPBatches is every batch, newest first.
func (d *DB) ZTPBatches() ([]ZTPBatch, error) {
	if err := d.ready(); err != nil {
		return nil, err
	}
	rows, err := d.sql.Query(`SELECT ` + ztpBatchCols + ` FROM ztp_batches ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ZTPBatch{}
	for rows.Next() {
		b, err := scanZTPBatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// RevokeZTPBatch marks a batch revoked: its token is refused from now on.
func (d *DB) RevokeZTPBatch(id string, at int64) error {
	if err := d.ready(); err != nil {
		return err
	}
	res, err := d.sql.Exec(`UPDATE ztp_batches SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, at, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrZTPNotFound
	}
	return nil
}

// orJSONArray and orJSONObject give an empty JSON column its empty value of
// the right shape. orJSON (cfg_write.go) is for lists; the object columns here
// (values, facts) would read back as "[]" with it, which is the wrong type for
// the browser that parses them.
func orJSONArray(s string) string {
	if s == "" {
		return "[]"
	}
	return s
}

func orJSONObject(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}
