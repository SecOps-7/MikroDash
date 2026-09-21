package db

import (
	"database/sql"
	"errors"
)

const cfgTemplateCols = `id, name, description, kind, scope, sealed, variables, fingerprint, revision,
	source_router_id, source_model, source_os_version, backup_id, baseline,
	created_by, created_at, updated_at`

func scanCfgTemplate(s interface{ Scan(...any) error }, body *string) (CfgTemplate, error) {
	var t CfgTemplate
	dst := []any{&t.ID, &t.Name, &t.Description, &t.Kind, &t.Scope, &t.Sealed, &t.Variables,
		&t.Fingerprint, &t.Revision, &t.SourceRouterID, &t.SourceModel, &t.SourceOSVersion,
		&t.BackupID, &t.Baseline, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt}
	if body != nil {
		dst = append(dst, body)
	}
	err := s.Scan(dst...)
	return t, err
}

// CfgTemplates lists every template WITHOUT its body: the Library shows
// cards, and a full export's body is sealed and hundreds of KB.
func (d *DB) CfgTemplates() ([]CfgTemplate, error) {
	out := []CfgTemplate{}
	if d == nil || d.sql == nil {
		return out, errors.New("db not open")
	}
	rows, err := d.sql.Query(`SELECT ` + cfgTemplateCols + ` FROM cfg_templates ORDER BY name`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scanCfgTemplate(rows, nil)
		if err != nil {
			return out, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CfgTemplate reads one template with its body, or nil when there is none.
func (d *DB) CfgTemplate(id string) (*CfgTemplate, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	var body string
	t, err := scanCfgTemplate(d.sql.QueryRow(`SELECT `+cfgTemplateCols+`, body
	    FROM cfg_templates WHERE id = ?`, id), &body)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Body = body
	return &t, nil
}

const cfgRunCols = `id, template_id, template_name, revision, method, body_masked, fingerprint,
	values_json, state, canary_router_id, allow_model_mismatch, created_by, created_at,
	updated_at, finished_at, error`

func scanCfgRun(s interface{ Scan(...any) error }) (CfgRun, error) {
	var r CfgRun
	err := s.Scan(&r.ID, &r.TemplateID, &r.TemplateName, &r.Revision, &r.Method, &r.BodyMasked,
		&r.Fingerprint, &r.ValuesJSON, &r.State, &r.CanaryRouterID, &r.AllowModelMismatch,
		&r.CreatedBy, &r.CreatedAt, &r.UpdatedAt, &r.FinishedAt, &r.Error)
	return r, err
}

// CfgRuns lists the newest runs first, at most `limit` (1..500).
func (d *DB) CfgRuns(limit int) ([]CfgRun, error) {
	out := []CfgRun{}
	if d == nil || d.sql == nil {
		return out, errors.New("db not open")
	}
	if limit < 1 || limit > 500 {
		limit = 500
	}
	rows, err := d.sql.Query(`SELECT `+cfgRunCols+` FROM cfg_runs
	    ORDER BY created_at DESC, id LIMIT ?`, limit)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanCfgRun(rows)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CfgRun reads one run and its targets in deploy order, or nil when there is
// none.
func (d *DB) CfgRun(id string) (*CfgRun, []CfgRunTarget, error) {
	if d == nil || d.sql == nil {
		return nil, nil, errors.New("db not open")
	}
	r, err := scanCfgRun(d.sql.QueryRow(`SELECT `+cfgRunCols+` FROM cfg_runs WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	rows, err := d.sql.Query(`SELECT run_id, router_id, position, state, step, model, serial,
	    os_version, model_override, rendered_fingerprint, backup_id, dry_run_output,
	    import_output, applied, failed_line, reconnect_ms, warning, error, started_at, finished_at
	    FROM cfg_run_targets WHERE run_id = ? ORDER BY position`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	targets := []CfgRunTarget{}
	for rows.Next() {
		var t CfgRunTarget
		if err := rows.Scan(&t.RunID, &t.RouterID, &t.Position, &t.State, &t.Step, &t.Model,
			&t.Serial, &t.OSVersion, &t.ModelOverride, &t.RenderedFingerprint, &t.BackupID,
			&t.DryRunOutput, &t.ImportOutput, &t.Applied, &t.FailedLine, &t.ReconnectMS,
			&t.Warning, &t.Error, &t.StartedAt, &t.FinishedAt); err != nil {
			return nil, nil, err
		}
		targets = append(targets, t)
	}
	return &r, targets, rows.Err()
}

// CfgBaselines lists every baseline WITHOUT its body, for the Drift grid.
func (d *DB) CfgBaselines() ([]CfgBaseline, error) {
	out := []CfgBaseline{}
	if d == nil || d.sql == nil {
		return out, errors.New("db not open")
	}
	rows, err := d.sql.Query(`SELECT template_id, router_id, run_id, fingerprint, taken_at
	    FROM cfg_baselines ORDER BY template_id, router_id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var b CfgBaseline
		if err := rows.Scan(&b.TemplateID, &b.RouterID, &b.RunID, &b.Fingerprint, &b.TakenAt); err != nil {
			return out, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CfgBaselineFor reads one baseline with its body, or nil when there is none.
func (d *DB) CfgBaselineFor(templateID, routerID string) (*CfgBaseline, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	var b CfgBaseline
	err := d.sql.QueryRow(`SELECT template_id, router_id, run_id, body, fingerprint, taken_at
	    FROM cfg_baselines WHERE template_id = ? AND router_id = ?`, templateID, routerID).
		Scan(&b.TemplateID, &b.RouterID, &b.RunID, &b.Body, &b.Fingerprint, &b.TakenAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}
