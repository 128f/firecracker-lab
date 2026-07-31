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
	Use:   "fctl",
	Short: "Firecracker VM manager",
}

func defaultDataDir() string {
	if v := os.Getenv("FCTL_DATA_DIR"); v != "" {
		return v
	}
	return "/var/lib/fctl"
}

func Execute() error {
	cfg := &Config{}

	rootCmd.PersistentFlags().StringVar(&cfg.DataDir, "data-dir", defaultDataDir(), "directory for VM state, images, and the state DB")
	rootCmd.PersistentFlags().StringVar(&cfg.SourceDir, "source-dir", ".", "directory containing build-time inputs (vmlinux.bin, base rootfs.ext4)")
	rootCmd.PersistentFlags().StringVar(&cfg.FCBin, "firecracker", "firecracker", "path to firecracker binary")

	rootCmd.AddCommand(newSetupCmd(cfg))
	rootCmd.AddCommand(newRunCmd(cfg))
	rootCmd.AddCommand(newDestroyCmd(cfg))
	rootCmd.AddCommand(newListCmd(cfg))
	rootCmd.AddCommand(newConsoleCmd(cfg))
	rootCmd.AddCommand(newImageCmd(cfg))

	return rootCmd.Execute()
}
