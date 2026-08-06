package cmd

import (
	"fmt"

	"github.com/128f/fctl/agentpb"
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
					status = guestStatus(r, v.ID)
				}
				fmt.Printf("%-8s %-8s %-16s %-6d %s\n", v.ID, v.Tap, v.IP, v.CID, status)
			}
			return nil
		},
	}
}

// guestStatus queries the guest agent over vsock for its health status. A
// process being alive (per r.IsAlive) doesn't mean the agent inside is up
// and answering yet, so "running" here just means "we couldn't reach the
// agent to say otherwise" rather than a hard failure.
func guestStatus(r *vm.Runner, id string) string {
	health, err := r.Status(id)
	if err != nil {
		return "running (agent unreachable)"
	}
	switch health {
	case agentpb.HealthStatus_HEALTHY:
		return "running (healthy)"
	case agentpb.HealthStatus_DEGRADED:
		return "running (degraded)"
	default:
		return "running"
	}
}
