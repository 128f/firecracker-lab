package state

import "database/sql"

// MigrateUnitColumn adds the vms.unit column (introduced when pid-based
// process tracking was replaced with systemd unit tracking) to a database
// created before that change, backfilling it deterministically from id for
// any pre-existing rows. This is a one-off, manually-run migration rather
// than something Load does automatically — run it once via `labctl
// migrate-unit-column` against an existing labctl.db, then this file and
// the corresponding command can both be deleted.
func MigrateUnitColumn(dbPath string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.Query(`PRAGMA table_info(vms)`)
	if err != nil {
		return err
	}
	hasUnit := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "unit" {
			hasUnit = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if hasUnit {
		return nil
	}

	if _, err := db.Exec(`ALTER TABLE vms ADD COLUMN unit TEXT`); err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE vms SET unit = 'labctl-' || id WHERE unit IS NULL`)
	return err
}
