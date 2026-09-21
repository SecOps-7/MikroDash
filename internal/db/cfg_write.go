package db

import (
	"errors"
	"time"
)

// CfgTemplate is one row of cfg_templates.
//
// Scope and Variables are JSON the server wrote (a list of menus; the typed
// variable declarations), kept opaque here as sitedoc's documents are: the
// shapes live with the code that checks them, internal/cfgtpl.
//
// Body is SEALED for a full export (Sealed true): the server encrypts it with
// the store's key before it reaches this package, because a full export is
// captured with show-sensitive and holds the router's secrets.
type CfgTemplate struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Description     string  `json:"description"`
	Kind            string  `json:"kind"`
	Scope           string  `json:"scope"`
	Body            string  `json:"-"`
	Sealed          bool    `json:"sealed"`
	Variables       string  `json:"variables"`
	Fingerprint     string  `json:"fingerprint"`
	Revision        int     `json:"revision"`
	SourceRouterID  *string `json:"sourceRouterId"`
	SourceModel     *string `json:"sourceModel"`
	SourceOSVersion *string `json:"sourceOsVersion"`
	BackupID        *int64  `json:"backupId"`
	Baseline        *string `json:"baseline"`
	// CreatedBy is the creator's USER ID, not the username. See
	// internal/verify/identity_test.go.
	CreatedBy string `json:"createdBy"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// ErrCfgStale is an update that lost a race: the template changed since the
// editor read it.
var ErrCfgStale = errors.New("the template was changed by someone else since it was opened")

// CreateCfgTemplate writes a new template at revision 1.
func (d *DB) CreateCfgTemplate(t CfgTemplate) error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	now := time.Now().UnixMilli()
	_, err := d.sql.Exec(`INSERT INTO cfg_templates
	    (id, name, description, kind, scope, body, sealed, variables, fingerprint, revision,
	     source_router_id, source_model, source_os_version, backup_id, baseline,
	     created_by, created_at, updated_at)
	    VALUES (?,?,?,?,?,?,?,?,?,1,?,?,?,?,?,?,?,?)`,
		t.ID, t.Name, t.Description, t.Kind, orJSON(t.Scope), t.Body, t.Sealed, orJSON(t.Variables),
		t.Fingerprint, t.SourceRouterID, t.SourceModel, t.SourceOSVersion, t.BackupID, t.Baseline,
		t.CreatedBy, now, now)
	return err
}

// UpdateCfgTemplate saves an edit made to revision `from`, and bumps it.
//
// ── A LOST RACE IS REFUSED, NOT MERGED ──────────────────────────────────────
//
// Two admins editing one template would otherwise each save over the other,
// and the one who saved first would deploy text they never wrote. The row
// changes only while it is still at the revision the editor read, and
// ErrCfgStale says so otherwise. Kind, source and creator never change: a
// capture stays a capture of that router.
func (d *DB) UpdateCfgTemplate(t CfgTemplate, from int) error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	res, err := d.sql.Exec(`UPDATE cfg_templates
	    SET name = ?, description = ?, scope = ?, body = ?, sealed = ?, variables = ?,
	        fingerprint = ?, revision = revision + 1, updated_at = ?
	    WHERE id = ? AND revision = ?`,
		t.Name, t.Description, orJSON(t.Scope), t.Body, t.Sealed, orJSON(t.Variables),
		t.Fingerprint, time.Now().UnixMilli(), t.ID, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCfgStale
	}
	return nil
}

// DeleteCfgTemplate removes a template and its drift baselines, in one
// transaction. Its runs stay, naming it.
func (d *DB) DeleteCfgTemplate(id string) (bool, error) {
	if d == nil || d.sql == nil {
		return false, errors.New("db not open")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM cfg_baselines WHERE template_id = ?`, id); err != nil {
		return false, err
	}
	res, err := tx.Exec(`DELETE FROM cfg_templates WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, tx.Commit()
}

// CfgRun is one deploy.
type CfgRun struct {
	ID           string  `json:"id"`
	TemplateID   *string `json:"templateId"`
	TemplateName string  `json:"templateName"`
	Revision     int     `json:"revision"`
	Method       string  `json:"method"`
	// BodyMasked is what was sent, with every secret shown as a placeholder.
	BodyMasked  string `json:"bodyMasked"`
	Fingerprint string `json:"fingerprint"`
	// ValuesJSON is the run's values WITHOUT its secrets.
	ValuesJSON         string  `json:"values"`
	State              string  `json:"state"`
	CanaryRouterID     *string `json:"canaryRouterId"`
	AllowModelMismatch bool    `json:"allowModelMismatch"`
	// CreatedBy is the USER ID of the admin who started the run.
	CreatedBy  string  `json:"createdBy"`
	CreatedAt  int64   `json:"createdAt"`
	UpdatedAt  int64   `json:"updatedAt"`
	FinishedAt *int64  `json:"finishedAt"`
	Error      *string `json:"error"`
}

// CfgRunTarget is one router in a run.
type CfgRunTarget struct {
	RunID               string  `json:"runId"`
	RouterID            string  `json:"routerId"`
	Position            int     `json:"position"`
	State               string  `json:"state"`
	Step                *string `json:"step"`
	Model               *string `json:"model"`
	Serial              *string `json:"serial"`
	OSVersion           *string `json:"osVersion"`
	ModelOverride       bool    `json:"modelOverride"`
	RenderedFingerprint *string `json:"renderedFingerprint"`
	BackupID            *int64  `json:"backupId"`
	DryRunOutput        *string `json:"dryRunOutput"`
	ImportOutput        *string `json:"importOutput"`
	Applied             *string `json:"applied"`
	FailedLine          *int    `json:"failedLine"`
	ReconnectMS         *int64  `json:"reconnectMs"`
	Warning             *string `json:"warning"`
	Error               *string `json:"error"`
	StartedAt           *int64  `json:"startedAt"`
	FinishedAt          *int64  `json:"finishedAt"`
}

// MaxCfgOutput caps each stored router report. A dry-run report echoes the
// file back, and a full export is hundreds of KB; the History tab needs the
// errors and their surroundings, not a second copy of the configuration.
const MaxCfgOutput = 64 << 10

// CreateCfgRun writes a run and all its targets in one transaction: a run
// whose targets are missing would read as a deploy to nobody.
func (d *DB) CreateCfgRun(r CfgRun, targets []CfgRunTarget) error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	if _, err := tx.Exec(`INSERT INTO cfg_runs
	    (id, template_id, template_name, revision, method, body_masked, fingerprint, values_json,
	     state, canary_router_id, allow_model_mismatch, created_by, created_at, updated_at)
	    VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.TemplateID, r.TemplateName, r.Revision, r.Method, r.BodyMasked, r.Fingerprint,
		orObject(r.ValuesJSON), r.State, r.CanaryRouterID, r.AllowModelMismatch, r.CreatedBy, now, now); err != nil {
		return err
	}
	for i, t := range targets {
		if _, err := tx.Exec(`INSERT INTO cfg_run_targets (run_id, router_id, position, state)
		    VALUES (?,?,?,?)`, r.ID, t.RouterID, i, t.State); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetCfgRunState moves a run on. `errText` is "" for none.
func (d *DB) SetCfgRunState(id, state, errText string) error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	now := time.Now().UnixMilli()
	var fin any
	for _, s := range cfgRunFinished {
		if s == state {
			fin = now
		}
	}
	_, err := d.sql.Exec(`UPDATE cfg_runs SET state = ?, updated_at = ?,
	    finished_at = COALESCE(?, finished_at), error = NULLIF(?, '') WHERE id = ?`,
		state, now, fin, errText, id)
	return err
}

// SaveCfgTarget writes one target's whole row. The job holds each target in
// memory and saves it before every router command, so the row always says
// what was about to happen.
func (d *DB) SaveCfgTarget(t CfgRunTarget) error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	_, err := d.sql.Exec(`UPDATE cfg_run_targets SET
	    state = ?, step = ?, model = ?, serial = ?, os_version = ?, model_override = ?,
	    rendered_fingerprint = ?, backup_id = ?, dry_run_output = ?, import_output = ?,
	    applied = ?, failed_line = ?, reconnect_ms = ?, warning = ?, error = ?,
	    started_at = ?, finished_at = ?
	    WHERE run_id = ? AND router_id = ?`,
		t.State, t.Step, t.Model, t.Serial, t.OSVersion, t.ModelOverride,
		t.RenderedFingerprint, t.BackupID, capOutput(t.DryRunOutput), capOutput(t.ImportOutput),
		t.Applied, t.FailedLine, t.ReconnectMS, t.Warning, t.Error,
		t.StartedAt, t.FinishedAt, t.RunID, t.RouterID)
	return err
}

// CfgBaseline is what a router held of a template's menus after it was
// applied, for drift.
type CfgBaseline struct {
	TemplateID  string  `json:"templateId"`
	RouterID    string  `json:"routerId"`
	RunID       *string `json:"runId"`
	Body        string  `json:"-"`
	Fingerprint string  `json:"fingerprint"`
	TakenAt     int64   `json:"takenAt"`
}

// SetCfgBaseline records a router's state for a template, replacing the last.
func (d *DB) SetCfgBaseline(b CfgBaseline) error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	_, err := d.sql.Exec(`INSERT INTO cfg_baselines (template_id, router_id, run_id, body, fingerprint, taken_at)
	    VALUES (?,?,?,?,?,?)
	    ON CONFLICT (template_id, router_id) DO UPDATE SET
	      run_id = excluded.run_id, body = excluded.body,
	      fingerprint = excluded.fingerprint, taken_at = excluded.taken_at`,
		b.TemplateID, b.RouterID, b.RunID, b.Body, b.Fingerprint, b.TakenAt)
	return err
}

// capOutput keeps the head and the tail of an oversized report: RouterOS puts
// the error near the end and the context near the start.
func capOutput(s *string) *string {
	if s == nil || len(*s) <= MaxCfgOutput {
		return s
	}
	half := MaxCfgOutput / 2
	c := (*s)[:half] + "\n… (report shortened) …\n" + (*s)[len(*s)-half:]
	return &c
}

func orJSON(s string) string {
	if s == "" {
		return "[]"
	}
	return s
}

func orObject(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}
