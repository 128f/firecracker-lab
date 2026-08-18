package cmd

import (
	"fmt"

	"github.com/128f/fctl/state"
	"github.com/128f/fctl/vm"
	"github.com/spf13/cobra"
)

func newConsoleCmd(cfg *Config) *cobra.Command {
	return &cobra.Command{
		Use:   "console <id>",
		Short: "Attach an interactive shell to a VM (over the guest agent's PTY vsock port)",
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

			r := &vm.Runner{DataDir: cfg.DataDir, FirecrackerBin: cfg.FCBin}
			return r.AttachConsole(id)
		},
	}
}
