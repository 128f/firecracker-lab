package cmd

import (
	"github.com/spf13/cobra"
)

var (
	labDir string
	fcBin  string
)

var rootCmd = &cobra.Command{
	Use:   "fctl",
	Short: "Firecracker VM manager",
}

func init() {
	rootCmd.PersistentFlags().StringVar(&fcBin, "firecracker", "firecracker", "path to firecracker binary")

	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(createCmd)
	rootCmd.AddCommand(destroyCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(consoleCmd)
}

func Execute(dir string) error {
	labDir = dir
	return rootCmd.Execute()
}
