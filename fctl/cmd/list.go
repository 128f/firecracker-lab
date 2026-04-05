package cmd

import (
	"fmt"

	"github.com/128f/fctl/state"
	"github.com/128f/fctl/vm"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List VMs",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := state.Load(state.StatePath(labDir))
		if err != nil {
			return err
		}
		if len(s.VMs) == 0 {
			fmt.Println("no VMs")
			return nil
		}
		r := &vm.Runner{LabDir: labDir, FirecrackerBin: fcBin}
		fmt.Printf("%-8s %-8s %-16s %-6s %s\n", "ID", "TAP", "IP", "CID", "STATUS")
		for _, v := range s.VMs {
			status := "stopped"
			if r.IsAlive(v.ID) {
				status = "running"
			}
			fmt.Printf("%-8s %-8s %-16s %-6d %s\n", v.ID, v.Tap, v.IP, v.CID, status)
		}
		return nil
	},
}
