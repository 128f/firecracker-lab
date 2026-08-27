package state

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestAllocateAndInsertConcurrent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "labctl.db")
	s, err := Load(dbPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()

	const n = 20
	var wg sync.WaitGroup
	vms := make([]*VM, n)
	errs := make([]error, n)

	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			vms[i], errs[i] = s.AllocateAndInsert(1, 256, "")
		}(i)
	}
	wg.Wait()

	taps := make(map[string]bool)
	ips := make(map[string]bool)
	cids := make(map[int]bool)
	ids := make(map[string]bool)

	for i, err := range errs {
		if err != nil {
			t.Fatalf("AllocateAndInsert[%d]: %v", i, err)
		}
		v := vms[i]
		if taps[v.Tap] {
			t.Errorf("duplicate tap: %s", v.Tap)
		}
		taps[v.Tap] = true
		if ips[v.IP] {
			t.Errorf("duplicate ip: %s", v.IP)
		}
		ips[v.IP] = true
		if cids[v.CID] {
			t.Errorf("duplicate cid: %d", v.CID)
		}
		cids[v.CID] = true
		if ids[v.ID] {
			t.Errorf("duplicate id: %s", v.ID)
		}
		ids[v.ID] = true
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != n {
		t.Fatalf("expected %d VMs in db, got %d", n, len(list))
	}
}

func TestAllocateReusesGapAfterRemove(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "labctl.db")
	s, err := Load(dbPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()

	v0, err := s.AllocateAndInsert(1, 256, "")
	if err != nil {
		t.Fatalf("AllocateAndInsert: %v", err)
	}
	v1, err := s.AllocateAndInsert(1, 256, "")
	if err != nil {
		t.Fatalf("AllocateAndInsert: %v", err)
	}
	if err := s.Remove(v0.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	v2, err := s.AllocateAndInsert(1, 256, "")
	if err != nil {
		t.Fatalf("AllocateAndInsert: %v", err)
	}
	if v2.Tap != v0.Tap || v2.ID != v0.ID {
		t.Fatalf("expected reuse of freed slot %s/%s, got %s/%s", v0.ID, v0.Tap, v2.ID, v2.Tap)
	}
	_ = v1
}
