package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	VMs  map[string]*VM `json:"vms"`
	path string
}

func Load(path string) (*State, error) {
	s := &State{VMs: make(map[string]*VM), path: path}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(data, s)
}

func (s *State) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

func (s *State) Add(vm *VM) error {
	s.VMs[vm.ID] = vm
	return s.Save()
}

func (s *State) Remove(id string) error {
	delete(s.VMs, id)
	return s.Save()
}

// NextAlloc returns the next available vm ID and index.
func (s *State) NextAlloc() (id string, tapIdx int, ip string, cid int) {
	used := make(map[int]bool)
	for _, vm := range s.VMs {
		for i := range 1000 {
			if vm.Tap == tapName(i) {
				used[i] = true
			}
		}
	}
	for i := range 1000 {
		if !used[i] {
			return vmID(i), i, vmIP(i), 3 + i
		}
	}
	panic("no available slots")
}

func tapName(i int) string { return fmt.Sprintf("tap%d", i) }
func vmID(i int) string    { return fmt.Sprintf("vm%d", i) }
func vmIP(i int) string    { return fmt.Sprintf("172.16.%d.%d", (i+2)/254, (i+2)%254+1) }

// StatePath returns the default state.json path relative to labDir.
func StatePath(labDir string) string {
	return filepath.Join(labDir, "state.json")
}
