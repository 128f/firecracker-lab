package cmd

import (
	"fmt"

	"github.com/128f/fctl/state"
	"github.com/128f/fctl/vm"
	"github.com/spf13/cobra"
)

func newListCmd(cfg *Config) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List VMs",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := state.Load(state.DBPath(cfg.DataDir))
			if err != nil {
				return err
			}
			defer s.Close()
			vms, err := s.List()
			if err != nil {
				return err
			}
			if len(vms) == 0 {
				fmt.Println("no VMs")
				return nil
			}
			r := &vm.Runner{DataDir: cfg.DataDir, FirecrackerBin: cfg.FCBin}
			fmt.Printf("%-8s %-8s %-16s %-6s %s\n", "ID", "TAP", "IP", "CID", "STATUS")
			for _, v := range vms {
				status := "stopped"
				if r.IsAlive(v.ID) {
					status = "running"
				}
				fmt.Printf("%-8s %-8s %-16s %-6d %s\n", v.ID, v.Tap, v.IP, v.CID, status)
			}
			return nil
		},
	}
}
