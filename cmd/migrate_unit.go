package cmd

import (
	"fmt"

	"github.com/128f/labctl/state"
	"github.com/spf13/cobra"
)

// newMigrateUnitColumnCmd is a one-off migration for databases created
// before pid-based process tracking was replaced with systemd unit
// tracking. Run it once against an existing labctl.db, then delete this
// file and state/migrate_unit.go.
func newMigrateUnitColumnCmd(cfg *Config) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate-unit-column",
		Short: "One-off: add the vms.unit column to a pre-existing labctl.db",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := state.DBPath(cfg.DataDir)
			if err := state.MigrateUnitColumn(path); err != nil {
				return err
			}
			fmt.Printf("migrated %s\n", path)
			return nil
		},
	}
}
