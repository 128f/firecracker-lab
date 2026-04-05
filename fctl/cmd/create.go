package cmd

import (
	"fmt"

	"github.com/128f/fctl/state"
	"github.com/128f/fctl/vm"
	"github.com/spf13/cobra"
)

var (
	flagVCPUs     int
	flagMemMiB    int
	flagCount     int
	flagJailerBin string
	flagFCBin     string
	flagUID       int
	flagGID       int
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create and start one or more VMs",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := state.Load(state.StatePath(labDir))
		if err != nil {
			return err
		}

		r := &vm.Runner{
			LabDir:         labDir,
			JailerBin:      flagJailerBin,
			FirecrackerBin: flagFCBin,
			UID:            flagUID,
			GID:            flagGID,
		}

		for range flagCount {
			id, tapIdx, ip, cid := s.NextAlloc()
			_ = tapIdx

			v := &state.VM{
				ID:     id,
				Tap:    fmt.Sprintf("tap%d", cid-3),
				IP:     ip,
				CID:    cid,
				VCPUs:  flagVCPUs,
				MemMiB: flagMemMiB,
			}

			fmt.Printf("creating %s (tap=%s ip=%s cid=%d)...\n", v.ID, v.Tap, v.IP, v.CID)

			if err := r.Create(v); err != nil {
				return fmt.Errorf("create %s: %w", v.ID, err)
			}
			if err := r.Start(v); err != nil {
				return fmt.Errorf("start %s: %w", v.ID, err)
			}
			if err := s.Add(v); err != nil {
				return err
			}
			fmt.Printf("started %s\n", v.ID)
		}
		return nil
	},
}

func init() {
	createCmd.Flags().IntVar(&flagVCPUs, "vcpus", 1, "vCPU count")
	createCmd.Flags().IntVar(&flagMemMiB, "mem", 256, "memory in MiB")
	createCmd.Flags().IntVar(&flagCount, "count", 1, "number of VMs to create")
	createCmd.Flags().StringVar(&flagJailerBin, "jailer", "jailer", "path to jailer binary")
	createCmd.Flags().StringVar(&flagFCBin, "firecracker", "firecracker", "path to firecracker binary")
	createCmd.Flags().IntVar(&flagUID, "uid", 123, "uid for jailer vm user")
	createCmd.Flags().IntVar(&flagGID, "gid", 123, "gid for jailer vm user")
}
