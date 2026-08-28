package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// Config holds the dependencies shared by every subcommand.
type Config struct {
	// DataDir owns all runtime state: the vms/ dir and the state DB.
	DataDir string
	// SourceDir holds build-time inputs (vmlinux.bin, base rootfs.ext4)
	// consumed by setup/first-time image registration.
	SourceDir string
	FCBin     string
}

var rootCmd = &cobra.Command{
	Use:   "labctl",
	Short: "Firecracker VM manager",
}

// envOr returns the value of the given environment variable, or fallback
// if it's unset or empty.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func defaultDataDir() string {
	return envOr("LABCTL_DATA_DIR", "/var/lib/labctl")
}

func defaultSourceDir() string {
	return envOr("LABCTL_SOURCE_DIR", ".")
}

func defaultFirecrackerBin() string {
	return envOr("LABCTL_FIRECRACKER_BIN", "firecracker")
}

func defaultJailerBin() string {
	return envOr("LABCTL_JAILER_BIN", "jailer")
}

func Execute() error {
	cfg := &Config{}

	rootCmd.PersistentFlags().StringVar(&cfg.DataDir, "data-dir", defaultDataDir(), "directory for VM state, images, and the state DB (env: LABCTL_DATA_DIR)")
	rootCmd.PersistentFlags().StringVar(&cfg.SourceDir, "source-dir", defaultSourceDir(), "directory containing build-time inputs (vmlinux.bin) (env: LABCTL_SOURCE_DIR)")
	rootCmd.PersistentFlags().StringVar(&cfg.FCBin, "firecracker", defaultFirecrackerBin(), "path to firecracker binary (env: LABCTL_FIRECRACKER_BIN)")

	rootCmd.AddCommand(newSetupCmd(cfg))
	rootCmd.AddCommand(newRunCmd(cfg))
	rootCmd.AddCommand(newDestroyCmd(cfg))
	rootCmd.AddCommand(newListCmd(cfg))
	rootCmd.AddCommand(newConsoleCmd(cfg))
	rootCmd.AddCommand(newVsockCmd(cfg))
	rootCmd.AddCommand(newImageCmd(cfg))
	rootCmd.AddCommand(newSnapshotCmd(cfg))
	rootCmd.AddCommand(newRestoreCmd(cfg))
	rootCmd.AddCommand(newMigrateUnitColumnCmd(cfg))
	rootCmd.AddCommand(newMigratePortsColumnCmd(cfg))
	rootCmd.AddCommand(newPortsCmd(cfg))

	return rootCmd.Execute()
}
