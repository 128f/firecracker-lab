package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type VM struct {
	ID     string `json:"id"`
	Tap    string `json:"tap"`
	IP     string `json:"ip"`
	CID    int    `json:"cid"`
	VCPUs  int    `json:"vcpus"`
	MemMiB int    `json:"mem_mib"`
	Unit   string `json:"unit"`
	// Ports are the guest-initiated vsock ports currently forwarded to the
	// same-numbered host TCP port via a socat systemd unit (see
	// vm.Runner.LaunchPortForwards). Managed by `labctl ports`.
	Ports []int `json:"ports"`
}

// UnitName returns the deterministic systemd transient-unit name (bare,
// without a .service suffix) for a VM with the given id.
func UnitName(id string) string { return "labctl-" + id }

type Image struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

type Snapshot struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Dir    string `json:"dir"`
	VCPUs  int    `json:"vcpus"`
	MemMiB int    `json:"mem_mib"`
	// Ports are the forwarded ports captured from the source VM at
	// snapshot time, so Restore can carry them over to the new VM.
	Ports []int `json:"ports"`
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
    unit       TEXT,
    ports      TEXT NOT NULL DEFAULT '[]',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS images (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    path       TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS snapshots (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    dir        TEXT NOT NULL,
    vcpus      INTEGER NOT NULL,
    mem_mib    INTEGER NOT NULL,
    ports      TEXT NOT NULL DEFAULT '[]',
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
func (s *State) AllocateAndInsert(vcpus, memMiB int, imageID string, ports []int) (*VM, error) {
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

	if ports == nil {
		ports = []int{}
	}
	v := &VM{
		ID:     vmID(i),
		Tap:    tapName(i),
		IP:     vmIP(i),
		CID:    3 + i,
		VCPUs:  vcpus,
		MemMiB: memMiB,
		Unit:   UnitName(vmID(i)),
		Ports:  ports,
	}

	var imgID any
	if imageID != "" {
		imgID = imageID
	}

	portsJSON, err := json.Marshal(v.Ports)
	if err != nil {
		return nil, err
	}

	_, err = conn.ExecContext(ctx,
		`INSERT INTO vms (id, tap, ip, cid, vcpus, mem_mib, image_id, unit, ports) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.Tap, v.IP, v.CID, v.VCPUs, v.MemMiB, imgID, v.Unit, string(portsJSON),
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
	var portsJSON string
	err := s.db.QueryRow(
		`SELECT id, tap, ip, cid, vcpus, mem_mib, unit, ports FROM vms WHERE id = ?`, id,
	).Scan(&v.ID, &v.Tap, &v.IP, &v.CID, &v.VCPUs, &v.MemMiB, &v.Unit, &portsJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(portsJSON), &v.Ports); err != nil {
		return nil, fmt.Errorf("unmarshal ports for %s: %w", id, err)
	}
	return v, nil
}

// List returns all VMs.
func (s *State) List() ([]*VM, error) {
	rows, err := s.db.Query(`SELECT id, tap, ip, cid, vcpus, mem_mib, unit, ports FROM vms`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vms []*VM
	for rows.Next() {
		v := &VM{}
		var portsJSON string
		if err := rows.Scan(&v.ID, &v.Tap, &v.IP, &v.CID, &v.VCPUs, &v.MemMiB, &v.Unit, &portsJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(portsJSON), &v.Ports); err != nil {
			return nil, fmt.Errorf("unmarshal ports for %s: %w", v.ID, err)
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

// SetPorts persists the full set of forwarded ports for the VM with the
// given id, replacing whatever was there before.
func (s *State) SetPorts(id string, ports []int) error {
	if ports == nil {
		ports = []int{}
	}
	data, err := json.Marshal(ports)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE vms SET ports = ? WHERE id = ?`, string(data), id)
	return err
}

// InsertImage records a newly-imported base image.
func (s *State) InsertImage(name, path string, sizeBytes int64) (*Image, error) {
	img := &Image{ID: uuid.NewString(), Name: name, Path: path, SizeBytes: sizeBytes}
	_, err := s.db.Exec(
		`INSERT INTO images (id, name, path, size_bytes) VALUES (?, ?, ?, ?)`,
		img.ID, img.Name, img.Path, img.SizeBytes,
	)
	if err != nil {
		return nil, err
	}
	return img, nil
}

// GetImage returns the image with the given id.
func (s *State) GetImage(id string) (*Image, error) {
	return s.scanImage(s.db.QueryRow(`SELECT id, name, path, size_bytes FROM images WHERE id = ?`, id))
}

// GetImageByName returns the image with the given name.
func (s *State) GetImageByName(name string) (*Image, error) {
	return s.scanImage(s.db.QueryRow(`SELECT id, name, path, size_bytes FROM images WHERE name = ?`, name))
}

func (s *State) scanImage(row *sql.Row) (*Image, error) {
	img := &Image{}
	err := row.Scan(&img.ID, &img.Name, &img.Path, &img.SizeBytes)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return img, nil
}

// DeleteImage removes the image with the given id.
func (s *State) DeleteImage(id string) error {
	_, err := s.db.Exec(`DELETE FROM images WHERE id = ?`, id)
	return err
}

// CountVMsUsingImage returns how many VMs reference the given image id.
func (s *State) CountVMsUsingImage(imageID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM vms WHERE image_id = ?`, imageID).Scan(&n)
	return n, err
}

// ListImages returns all registered images.
func (s *State) ListImages() ([]*Image, error) {
	rows, err := s.db.Query(`SELECT id, name, path, size_bytes FROM images`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var images []*Image
	for rows.Next() {
		img := &Image{}
		if err := rows.Scan(&img.ID, &img.Name, &img.Path, &img.SizeBytes); err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	return images, rows.Err()
}

// InsertSnapshot records a newly-created snapshot.
func (s *State) InsertSnapshot(name, dir string, vcpus, memMiB int, ports []int) (*Snapshot, error) {
	if ports == nil {
		ports = []int{}
	}
	snap := &Snapshot{ID: uuid.NewString(), Name: name, Dir: dir, VCPUs: vcpus, MemMiB: memMiB, Ports: ports}
	portsJSON, err := json.Marshal(snap.Ports)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(
		`INSERT INTO snapshots (id, name, dir, vcpus, mem_mib, ports) VALUES (?, ?, ?, ?, ?, ?)`,
		snap.ID, snap.Name, snap.Dir, snap.VCPUs, snap.MemMiB, string(portsJSON),
	)
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// GetSnapshotByName returns the snapshot with the given name.
func (s *State) GetSnapshotByName(name string) (*Snapshot, error) {
	return s.scanSnapshot(s.db.QueryRow(`SELECT id, name, dir, vcpus, mem_mib, ports FROM snapshots WHERE name = ?`, name))
}

func (s *State) scanSnapshot(row *sql.Row) (*Snapshot, error) {
	snap := &Snapshot{}
	var portsJSON string
	err := row.Scan(&snap.ID, &snap.Name, &snap.Dir, &snap.VCPUs, &snap.MemMiB, &portsJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(portsJSON), &snap.Ports); err != nil {
		return nil, fmt.Errorf("unmarshal ports for snapshot %s: %w", snap.Name, err)
	}
	return snap, nil
}

// DeleteSnapshot removes the snapshot with the given id.
func (s *State) DeleteSnapshot(id string) error {
	_, err := s.db.Exec(`DELETE FROM snapshots WHERE id = ?`, id)
	return err
}

// ListSnapshots returns all saved snapshots.
func (s *State) ListSnapshots() ([]*Snapshot, error) {
	rows, err := s.db.Query(`SELECT id, name, dir, vcpus, mem_mib, ports FROM snapshots`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snaps []*Snapshot
	for rows.Next() {
		snap := &Snapshot{}
		var portsJSON string
		if err := rows.Scan(&snap.ID, &snap.Name, &snap.Dir, &snap.VCPUs, &snap.MemMiB, &portsJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(portsJSON), &snap.Ports); err != nil {
			return nil, fmt.Errorf("unmarshal ports for snapshot %s: %w", snap.Name, err)
		}
		snaps = append(snaps, snap)
	}
	return snaps, rows.Err()
}

func tapName(i int) string { return fmt.Sprintf("tap%d", i) }
func vmID(i int) string    { return fmt.Sprintf("vm%d", i) }
func vmIP(i int) string    { return fmt.Sprintf("172.16.%d.%d", (i+2)/254, (i+2)%254+1) }

// DBPath returns the default labctl.db path relative to dataDir.
func DBPath(dataDir string) string {
	return filepath.Join(dataDir, "labctl.db")
}
