package cmd

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"

	"github.com/128f/fctl/state"
	"github.com/128f/fctl/vm"
	"github.com/spf13/cobra"
	"golang.org/x/term"
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
			defer conn.Close()
			fmt.Fprintf(os.Stderr, "connected to %s vsock port %d (ctrl+] to detach)\r\n", id, port)

			fd := int(os.Stdin.Fd())
			oldState, err := term.MakeRaw(fd)
			if err != nil {
				return fmt.Errorf("raw mode: %w", err)
			}
			defer term.Restore(fd, oldState)

			done := make(chan struct{})
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt)

			var once sync.Once
			closeDone := func() { once.Do(func() { close(done) }) }

			go func() {
				io.Copy(conn, &ctrlBracketReader{r: os.Stdin, done: done})
				closeDone()
			}()
			go func() {
				io.Copy(os.Stdout, conn)
				closeDone()
			}()

			select {
			case <-done:
			case <-sig:
			}
			return nil
		},
	}

	c.Flags().Uint32Var(&port, "port", defaultVsockPort, "guest vsock port to connect to")
	return c
}
