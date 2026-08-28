package cmd

import (
	"fmt"

	"github.com/128f/labctl/state"
	"github.com/spf13/cobra"
)

// newMigratePortsColumnCmd is a one-off migration for databases created
// before socat-based port forwarding was added. Run it once against an
// existing labctl.db, then delete this file and
// state/migrate_ports_column.go.
func newMigratePortsColumnCmd(cfg *Config) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate-ports-column",
		Short: "One-off: add the ports column to a pre-existing labctl.db",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := state.DBPath(cfg.DataDir)
			if err := state.MigratePortsColumn(path); err != nil {
				return err
			}
			fmt.Printf("migrated %s\n", path)
			return nil
		},
	}
}
