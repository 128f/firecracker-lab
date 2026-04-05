package cmd

import (
	"github.com/spf13/cobra"
)

var labDir string

var rootCmd = &cobra.Command{
	Use:   "fctl",
	Short: "Firecracker VM manager",
}

func init() {
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
