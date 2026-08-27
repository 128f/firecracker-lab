package cmd

import (
	"fmt"

	"github.com/128f/labctl/state"
	"github.com/128f/labctl/vm"
	"github.com/spf13/cobra"
)

func newRestoreCmd(cfg *Config) *cobra.Command {
	var (
		flagUID           int
		flagGID           int
		flagAttachConsole bool
		flagJailerBin     string
	)

	cmd := &cobra.Command{
		Use:   "restore <name>",
		Short: "Boot a new VM from a saved snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			s, err := state.Load(state.DBPath(cfg.DataDir))
			if err != nil {
				return err
			}
			defer s.Close()

			snap, err := s.GetSnapshotByName(name)
			if err != nil {
				return err
			}
			if snap == nil {
				return fmt.Errorf("unknown snapshot: %s (see `labctl snapshot list`)", name)
			}

			v, err := s.AllocateAndInsert(snap.VCPUs, snap.MemMiB, "")
			if err != nil {
				return err
			}

			r := &vm.Runner{
				DataDir:        cfg.DataDir,
				SourceDir:      cfg.SourceDir,
				JailerBin:      flagJailerBin,
				FirecrackerBin: cfg.FCBin,
				UID:            flagUID,
				GID:            flagGID,
			}

			fmt.Printf("restoring %s from snapshot %s (tap=%s ip=%s cid=%d)...\n", v.ID, name, v.Tap, v.IP, v.CID)
			if err := r.Restore(v, snap.Dir, flagAttachConsole); err != nil {
				_ = s.Remove(v.ID)
				return fmt.Errorf("restore %s: %w", v.ID, err)
			}
			fmt.Printf("restored %s\n", v.ID)
			return nil
		},
	}

	cmd.Flags().IntVar(&flagUID, "uid", 123, "uid for jailer vm user")
	cmd.Flags().IntVar(&flagGID, "gid", 123, "gid for jailer vm user")
	cmd.Flags().BoolVarP(&flagAttachConsole, "attach-console", "a", false, "run VM in foreground, attached to its console (default: detached, runs in background)")
	cmd.Flags().StringVar(&flagJailerBin, "jailer", defaultJailerBin(), "path to jailer binary (env: LABCTL_JAILER_BIN)")

	return cmd
}
