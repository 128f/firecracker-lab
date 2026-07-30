package cmd

import (
	"github.com/spf13/cobra"
)

// Config holds the dependencies shared by every subcommand.
type Config struct {
	LabDir string
	FCBin  string
}

var rootCmd = &cobra.Command{
	Use:   "fctl",
	Short: "Firecracker VM manager",
}

func Execute(dir string) error {
	cfg := &Config{LabDir: dir}

	rootCmd.PersistentFlags().StringVar(&cfg.FCBin, "firecracker", "firecracker", "path to firecracker binary")

	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(newRunCmd(cfg))
	rootCmd.AddCommand(newDestroyCmd(cfg))
	rootCmd.AddCommand(newListCmd(cfg))
	rootCmd.AddCommand(newConsoleCmd(cfg))

	return rootCmd.Execute()
}
