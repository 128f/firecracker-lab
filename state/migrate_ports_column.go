package state

import "database/sql"

// MigratePortsColumn adds the ports column (introduced when socat-based
// port forwarding was added) to a database created before that change,
// backfilling '[]' for any pre-existing rows. This is a one-off,
// manually-run migration rather than something Load does automatically —
// run it once via `labctl migrate-ports-column` against an existing
// labctl.db, then this file and the corresponding command can both be
// deleted.
func MigratePortsColumn(dbPath string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	for _, table := range []string{"vms", "snapshots"} {
		hasPorts, err := hasColumn(db, table, "ports")
		if err != nil {
			return err
		}
		if hasPorts {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ports TEXT`); err != nil {
			return err
		}
		if _, err := db.Exec(`UPDATE ` + table + ` SET ports = '[]' WHERE ports IS NULL`); err != nil {
			return err
		}
	}
	return nil
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
