package state

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type VM struct {
	ID     string `json:"id"`
	Tap    string `json:"tap"`
	IP     string `json:"ip"`
	CID    int    `json:"cid"`
	VCPUs  int    `json:"vcpus"`
	MemMiB int    `json:"mem_mib"`
}

type State struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS vms (
    id         TEXT PRIMARY KEY,
    tap        TEXT NOT NULL UNIQUE,
    ip         TEXT NOT NULL UNIQUE,
    cid        INTEGER NOT NULL UNIQUE,
    vcpus      INTEGER NOT NULL,
    mem_mib    INTEGER NOT NULL,
    image_id   TEXT,
    status     TEXT NOT NULL DEFAULT 'running',
    pid        INTEGER,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS images (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    path       TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS jobs (
    id         TEXT PRIMARY KEY,
    vm_id      TEXT REFERENCES vms(id),
    spec_ref   TEXT,
    repo_ref   TEXT,
    result_ref TEXT,
    status     TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

// Load opens (creating if necessary) the SQLite state database at path.
func Load(path string) (*State, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite only allows one writer at a time; a single connection avoids
	// SQLITE_BUSY from this process racing itself, and BEGIN IMMEDIATE
	// below handles serialization against other processes.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &State{db: db}, nil
}

func (s *State) Close() error {
	return s.db.Close()
}

// AllocateAndInsert finds the lowest unused index and inserts the new VM
// row in a single BEGIN IMMEDIATE transaction, closing the race between
// "find a free slot" and "claim it" across concurrent processes.
func (s *State) AllocateAndInsert(vcpus, memMiB int, imageID string) (*VM, error) {
	ctx := context.Background()

	// database/sql's Tx always issues a plain "BEGIN" (deferred), which
	// doesn't take the write lock up front. Grab a single connection out
	// of the pool and drive BEGIN IMMEDIATE / COMMIT on it manually so
	// the lock is held for the whole find-then-insert sequence.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	var i int
	found := false
	for i = range 1000 {
		var exists int
		err := conn.QueryRowContext(ctx, "SELECT 1 FROM vms WHERE tap = ?", tapName(i)).Scan(&exists)
		if err == sql.ErrNoRows {
			found = true
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, fmt.Errorf("no available slots")
	}

	v := &VM{
		ID:     vmID(i),
		Tap:    tapName(i),
		IP:     vmIP(i),
		CID:    3 + i,
		VCPUs:  vcpus,
		MemMiB: memMiB,
	}

	var imgID any
	if imageID != "" {
		imgID = imageID
	}

	_, err = conn.ExecContext(ctx,
		`INSERT INTO vms (id, tap, ip, cid, vcpus, mem_mib, image_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.Tap, v.IP, v.CID, v.VCPUs, v.MemMiB, imgID,
	)
	if err != nil {
		return nil, err
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	committed = true
	return v, nil
}

// Get returns the VM with the given id.
func (s *State) Get(id string) (*VM, error) {
	v := &VM{}
	err := s.db.QueryRow(
		`SELECT id, tap, ip, cid, vcpus, mem_mib FROM vms WHERE id = ?`, id,
	).Scan(&v.ID, &v.Tap, &v.IP, &v.CID, &v.VCPUs, &v.MemMiB)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// List returns all VMs.
func (s *State) List() ([]*VM, error) {
	rows, err := s.db.Query(`SELECT id, tap, ip, cid, vcpus, mem_mib FROM vms`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vms []*VM
	for rows.Next() {
		v := &VM{}
		if err := rows.Scan(&v.ID, &v.Tap, &v.IP, &v.CID, &v.VCPUs, &v.MemMiB); err != nil {
			return nil, err
		}
		vms = append(vms, v)
	}
	return vms, rows.Err()
}

// Remove deletes the VM with the given id.
func (s *State) Remove(id string) error {
	_, err := s.db.Exec(`DELETE FROM vms WHERE id = ?`, id)
	return err
}

func tapName(i int) string { return fmt.Sprintf("tap%d", i) }
func vmID(i int) string    { return fmt.Sprintf("vm%d", i) }
func vmIP(i int) string    { return fmt.Sprintf("172.16.%d.%d", (i+2)/254, (i+2)%254+1) }

// DBPath returns the default fctl.db path relative to labDir.
func DBPath(labDir string) string {
	return filepath.Join(labDir, "fctl.db")
}
