package cmd

import (
	"fmt"

	"github.com/128f/fctl/state"
	"github.com/128f/fctl/vm"
	"github.com/spf13/cobra"
)

var (
	flagVCPUs   int
	flagMemMiB  int
	flagCount   int
	flagUID     int
	flagGID     int
	flagDetach    bool
	flagJailerBin string
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Create and run one or more VMs",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := state.Load(state.StatePath(labDir))
		if err != nil {
			return err
		}

		r := &vm.Runner{
			LabDir:         labDir,
			JailerBin:      flagJailerBin,
			FirecrackerBin: fcBin,
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

			fmt.Printf("running %s (tap=%s ip=%s cid=%d)...\n", v.ID, v.Tap, v.IP, v.CID)

			if err := r.Run(v, flagDetach); err != nil {
				return fmt.Errorf("run %s: %w", v.ID, err)
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
	runCmd.Flags().IntVar(&flagVCPUs, "vcpus", 1, "vCPU count")
	runCmd.Flags().IntVar(&flagMemMiB, "mem", 256, "memory in MiB")
	runCmd.Flags().IntVar(&flagCount, "count", 1, "number of VMs to create")
	runCmd.Flags().IntVar(&flagUID, "uid", 123, "uid for jailer vm user")
	runCmd.Flags().IntVar(&flagGID, "gid", 123, "gid for jailer vm user")
	runCmd.Flags().BoolVarP(&flagDetach, "detach", "d", false, "run VM in background")
	runCmd.Flags().StringVar(&flagJailerBin, "jailer", "jailer", "path to jailer binary")
}
