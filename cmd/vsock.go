package cmd

import (
	"fmt"
	"os"

	"github.com/128f/fctl/state"
	"github.com/128f/fctl/vm"
	"github.com/spf13/cobra"
)

const defaultVsockPort = 1234

func newVsockCmd(cfg *Config) *cobra.Command {
	var port uint32

	c := &cobra.Command{
		Use:   "vsock <id>",
		Short: "Connect to a guest vsock listener",
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
			conn, err := r.DialVsock(id, port)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "connected to %s vsock port %d (ctrl+] to detach)\r\n", id, port)
			return vm.AttachSession(conn)
		},
	}

	c.Flags().Uint32Var(&port, "port", defaultVsockPort, "guest vsock port to connect to")
	return c
}
