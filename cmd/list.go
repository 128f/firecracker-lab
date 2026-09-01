package cmd

import (
	"fmt"

	"github.com/128f/labctl/agentpb"
	"github.com/128f/labctl/state"
	"github.com/128f/labctl/vm"
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
			fmt.Printf("%-8s %-8s %-16s %-6s %-6s %-10s %s\n", "ID", "TAP", "IP", "CID", "CPU%", "MEM AVAIL", "STATUS")
			for _, v := range vms {
				status, cpu, mem := "stopped", "-", "-"
				if r.IsAlive(v) {
					status, cpu, mem = guestStatus(r, v.ID)
				}
				fmt.Printf("%-8s %-8s %-16s %-6d %-6s %-10s %s\n", v.ID, v.Tap, v.IP, v.CID, cpu, mem, status)
			}
			return nil
		},
	}
}

// guestStatus queries the guest agent over vsock for its health, cpu%, and
// available memory. A process being alive (per r.IsAlive) doesn't mean the
// agent inside is up and answering yet, so "running" here just means "we
// couldn't reach the agent to say otherwise" rather than a hard failure.
func guestStatus(r *vm.Runner, id string) (status, cpu, mem string) {
	st, err := r.Status(id)
	if err != nil {
		return "unknown (agent unreachable)", "-", "-"
	}
	cpu = fmt.Sprintf("%d%%", st.GetCpuPct())
	mem = formatBytes(st.GetMemAvailableBytes())
	switch st.GetStatus() {
	case agentpb.HealthStatus_HEALTHY:
		return "running (healthy)", cpu, mem
	case agentpb.HealthStatus_DEGRADED:
		return "running (degraded)", cpu, mem
	default:
		return "running", cpu, mem
	}
}

// formatBytes renders a byte count in the largest whole unit that keeps it
// >= 1 (B/KiB/MiB/GiB), e.g. 1536 -> "1.5KiB".
func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
