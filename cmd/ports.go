package cmd

import (
	"fmt"
	"strconv"

	"github.com/128f/labctl/state"
	"github.com/128f/labctl/vm"
	"github.com/spf13/cobra"
)

func newPortsCmd(cfg *Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ports",
		Short: "Manage a VM's guest->host vsock port forwarding (socat units)",
	}
	cmd.AddCommand(newPortsAddCmd(cfg))
	cmd.AddCommand(newPortsRmCmd(cfg))
	cmd.AddCommand(newPortsListCmd(cfg))
	cmd.AddCommand(newPortsReloadCmd(cfg))
	return cmd
}

func newPortsAddCmd(cfg *Config) *cobra.Command {
	return &cobra.Command{
		Use:   "add <vm-id> <port>",
		Short: "Forward a guest-initiated vsock port to the same host TCP port",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			port, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid port %q: %w", args[1], err)
			}

			s, err := state.Load(state.DBPath(cfg.DataDir))
			if err != nil {
				return err
			}
			defer s.Close()

			v, err := s.Get(id)
			if err != nil {
				return err
			}
			if v == nil {
				return fmt.Errorf("unknown VM: %s", id)
			}
			for _, p := range v.Ports {
				if p == port {
					return fmt.Errorf("port %d is already forwarded for %s", port, id)
				}
			}

			v.Ports = append(v.Ports, port)
			if err := s.SetPorts(id, v.Ports); err != nil {
				return err
			}

			r := &vm.Runner{DataDir: cfg.DataDir, FirecrackerBin: cfg.FCBin, UID: defaultJailerUID, GID: defaultJailerGID}
			if !r.IsAlive(v) {
				fmt.Printf("%s is not running; port %d will start forwarding on its next restore\n", id, port)
				return nil
			}
			if err := r.LaunchPortForwards(&state.VM{ID: v.ID, Unit: v.Unit, Ports: []int{port}}); err != nil {
				return fmt.Errorf("start forward for port %d: %w", port, err)
			}
			if err := r.StartTcpVsockProxy(id, port); err != nil {
				return fmt.Errorf("start guest-side proxy for port %d: %w", port, err)
			}
			fmt.Printf("forwarding %s vsock port %d -> host 127.0.0.1:%d\n", id, port, port)
			return nil
		},
	}
}

func newPortsRmCmd(cfg *Config) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <vm-id> <port>",
		Short: "Stop forwarding a vsock port",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			port, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid port %q: %w", args[1], err)
			}

			s, err := state.Load(state.DBPath(cfg.DataDir))
			if err != nil {
				return err
			}
			defer s.Close()

			v, err := s.Get(id)
			if err != nil {
				return err
			}
			if v == nil {
				return fmt.Errorf("unknown VM: %s", id)
			}

			remaining := v.Ports[:0]
			found := false
			for _, p := range v.Ports {
				if p == port {
					found = true
					continue
				}
				remaining = append(remaining, p)
			}
			if !found {
				return fmt.Errorf("port %d is not forwarded for %s", port, id)
			}
			if err := s.SetPorts(id, remaining); err != nil {
				return err
			}

			r := &vm.Runner{DataDir: cfg.DataDir, FirecrackerBin: cfg.FCBin}
			if err := r.StopPortForwards(&state.VM{ID: v.ID, Unit: v.Unit, Ports: []int{port}}); err != nil {
				return fmt.Errorf("stop forward for port %d: %w", port, err)
			}
			if r.IsAlive(v) {
				if err := r.StopTcpVsockProxy(id, port); err != nil {
					return fmt.Errorf("stop guest-side proxy for port %d: %w", port, err)
				}
			}
			fmt.Printf("stopped forwarding %s vsock port %d\n", id, port)
			return nil
		},
	}
}

func newPortsListCmd(cfg *Config) *cobra.Command {
	return &cobra.Command{
		Use:   "list <vm-id>",
		Short: "List a VM's forwarded ports and their unit status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			s, err := state.Load(state.DBPath(cfg.DataDir))
			if err != nil {
				return err
			}
			defer s.Close()

			v, err := s.Get(id)
			if err != nil {
				return err
			}
			if v == nil {
				return fmt.Errorf("unknown VM: %s", id)
			}
			if len(v.Ports) == 0 {
				fmt.Println("no forwarded ports")
				return nil
			}

			r := &vm.Runner{DataDir: cfg.DataDir, FirecrackerBin: cfg.FCBin}
			fmt.Printf("%-8s %s\n", "PORT", "STATUS")
			for _, p := range v.Ports {
				status := "inactive"
				if r.PortForwardAlive(id, p) {
					status = "active"
				}
				fmt.Printf("%-8d %s\n", p, status)
			}
			return nil
		},
	}
}

func newPortsReloadCmd(cfg *Config) *cobra.Command {
	return &cobra.Command{
		Use:   "reload <vm-id>",
		Short: "Restart the socat unit for each of a VM's forwarded ports",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			s, err := state.Load(state.DBPath(cfg.DataDir))
			if err != nil {
				return err
			}
			defer s.Close()

			v, err := s.Get(id)
			if err != nil {
				return err
			}
			if v == nil {
				return fmt.Errorf("unknown VM: %s", id)
			}
			if len(v.Ports) == 0 {
				fmt.Println("no forwarded ports to reload")
				return nil
			}

			r := &vm.Runner{DataDir: cfg.DataDir, FirecrackerBin: cfg.FCBin}
			if err := r.RestartPortForwards(v); err != nil {
				return err
			}
			for _, port := range v.Ports {
				// Best-effort: a proxy may not currently be running (e.g. the
				// guest agent restarted), so a stop failure here isn't fatal.
				_ = r.StopTcpVsockProxy(id, port)
				if err := r.StartTcpVsockProxy(id, port); err != nil {
					return fmt.Errorf("restart guest-side proxy for port %d: %w", port, err)
				}
			}
			fmt.Printf("reloaded %d port forward(s) for %s\n", len(v.Ports), id)
			return nil
		},
	}
}
