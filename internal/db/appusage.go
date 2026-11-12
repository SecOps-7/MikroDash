package db

// Reading and writing the Apps Usage history; see appusage_schema.go.

// AppUsageRow is the bytes one application moved on one router in one hour.
// Hour is unix milliseconds at the start of the hour.
type AppUsageRow struct {
	RouterID string
	App      string
	Hour     int64
	RxBytes  int64
	TxBytes  int64
}

// AddAppUsage adds each row's bytes to what its hour already holds, in one
// transaction. A row with no bytes is skipped.
func (d *DB) AddAppUsage(rows []AppUsageRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT INTO app_usage (router_id, app, hour, rx_bytes, tx_bytes) VALUES (?, ?, ?, ?, ?)
	   ON CONFLICT(router_id, app, hour) DO UPDATE SET rx_bytes = rx_bytes + excluded.rx_bytes,
	     tx_bytes = tx_bytes + excluded.tx_bytes`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		rx, tx := max(r.RxBytes, 0), max(r.TxBytes, 0)
		if rx == 0 && tx == 0 {
			continue
		}
		if _, err := stmt.Exec(r.RouterID, r.App, r.Hour, rx, tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AppUsage is one router's hourly rows with an hour in [from, to], oldest first.
func (d *DB) AppUsage(routerID string, from, to int64) ([]AppUsageRow, error) {
	rows, err := d.sql.Query(`SELECT app, hour, rx_bytes, tx_bytes FROM app_usage
	  WHERE router_id = ? AND hour >= ? AND hour <= ? ORDER BY hour, app`, routerID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppUsageRow{}
	for rows.Next() {
		r := AppUsageRow{RouterID: routerID}
		if err := rows.Scan(&r.App, &r.Hour, &r.RxBytes, &r.TxBytes); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AppHistoryRouters is the routers whose app usage is recorded, which are kept
// reading their connections while nobody watches.
func (d *DB) AppHistoryRouters() (map[string]bool, error) {
	rows, err := d.sql.Query(`SELECT router_id FROM app_history_routers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// SetAppHistory switches the recording of a router's app usage on or off.
func (d *DB) SetAppHistory(routerID string, on bool) error {
	q := `DELETE FROM app_history_routers WHERE router_id = ?`
	if on {
		q = `INSERT OR IGNORE INTO app_history_routers (router_id) VALUES (?)`
	}
	_, err := d.sql.Exec(q, routerID)
	return err
}
