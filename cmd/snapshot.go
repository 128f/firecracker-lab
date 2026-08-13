package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/128f/fctl/state"
	"github.com/128f/fctl/vm"
	"github.com/spf13/cobra"
)

func newSnapshotCmd(cfg *Config) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "snapshot <vm-id> --name <name>",
		Short: "Pause a VM, save a full snapshot, and tear it down",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			id := args[0]

			s, err := state.Load(state.DBPath(cfg.DataDir))
			if err != nil {
				return err
			}
			defer s.Close()

			v, err := s.Get(id)
			if err != nil {
				return err
			}
			if v == nil {
				return fmt.Errorf("unknown VM: %s", id)
			}

			existing, err := s.GetSnapshotByName(name)
			if err != nil {
				return err
			}
			if existing != nil {
				return fmt.Errorf("snapshot %q already exists (dir: %s)", name, existing.Dir)
			}

			snapDir := filepath.Join(cfg.DataDir, "snapshots", name)
			r := &vm.Runner{DataDir: cfg.DataDir, FirecrackerBin: cfg.FCBin}

			fmt.Printf("snapshotting %s -> %s...\n", id, name)
			if err := r.Snapshot(v, snapDir); err != nil {
				return fmt.Errorf("snapshot %s: %w", id, err)
			}
			if _, err := s.InsertSnapshot(name, snapDir, v.VCPUs, v.MemMiB); err != nil {
				return err
			}
			return s.Remove(id)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name to save the snapshot under (required)")
	cmd.AddCommand(newSnapshotListCmd(cfg))
	return cmd
}

func newSnapshotListCmd(cfg *Config) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := state.Load(state.DBPath(cfg.DataDir))
			if err != nil {
				return err
			}
			defer s.Close()

			snaps, err := s.ListSnapshots()
			if err != nil {
				return err
			}
			if len(snaps) == 0 {
				fmt.Println("no snapshots")
				return nil
			}
			fmt.Printf("%-16s %-6s %-8s %s\n", "NAME", "VCPUS", "MEM", "DIR")
			for _, sn := range snaps {
				fmt.Printf("%-16s %-6d %-8d %s\n", sn.Name, sn.VCPUs, sn.MemMiB, sn.Dir)
			}
			return nil
		},
	}
}
