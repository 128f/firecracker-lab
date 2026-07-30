package cmd

import (
	"fmt"

	"github.com/128f/fctl/state"
	"github.com/128f/fctl/vm"
	"github.com/spf13/cobra"
)

func newRunCmd(cfg *Config) *cobra.Command {
	var (
		flagVCPUs     int
		flagMemMiB    int
		flagCount     int
		flagUID       int
		flagGID       int
		flagDetach    bool
		flagJailerBin string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Create and run one or more VMs",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := state.Load(state.DBPath(cfg.LabDir))
			if err != nil {
				return err
			}
			defer s.Close()

			r := &vm.Runner{
				LabDir:         cfg.LabDir,
				JailerBin:      flagJailerBin,
				FirecrackerBin: cfg.FCBin,
				UID:            flagUID,
				GID:            flagGID,
			}

			for range flagCount {
				v, err := s.AllocateAndInsert(flagVCPUs, flagMemMiB, "")
				if err != nil {
					return err
				}

				fmt.Printf("running %s (tap=%s ip=%s cid=%d)...\n", v.ID, v.Tap, v.IP, v.CID)

				if err := r.Run(v, flagDetach); err != nil {
					return fmt.Errorf("run %s: %w", v.ID, err)
				}
				fmt.Printf("started %s\n", v.ID)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&flagVCPUs, "vcpus", 1, "vCPU count")
	cmd.Flags().IntVar(&flagMemMiB, "mem", 256, "memory in MiB")
	cmd.Flags().IntVar(&flagCount, "count", 1, "number of VMs to create")
	cmd.Flags().IntVar(&flagUID, "uid", 123, "uid for jailer vm user")
	cmd.Flags().IntVar(&flagGID, "gid", 123, "gid for jailer vm user")
	cmd.Flags().BoolVarP(&flagDetach, "detach", "d", false, "run VM in background")
	cmd.Flags().StringVar(&flagJailerBin, "jailer", "jailer", "path to jailer binary")

	return cmd
}
