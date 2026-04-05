package cmd

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"

	"github.com/128f/fctl/state"
	"github.com/128f/fctl/vm"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var consoleCmd = &cobra.Command{
	Use:   "console <id>",
	Short: "Attach to VM serial console",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		s, err := state.Load(state.StatePath(labDir))
		if err != nil {
			return err
		}
		if _, ok := s.VMs[id]; !ok {
			return fmt.Errorf("unknown VM: %s", id)
		}

		r := &vm.Runner{LabDir: labDir}
		sock := r.ConsolePath(id)

		conn, err := net.Dial("unix", sock)
		if err != nil {
			return fmt.Errorf("connect to console: %w", err)
		}
		defer conn.Close()

		// Put terminal in raw mode so keystrokes go straight through
		fd := int(os.Stdin.Fd())
		oldState, err := term.MakeRaw(fd)
		if err != nil {
			return fmt.Errorf("raw mode: %w", err)
		}
		defer term.Restore(fd, oldState)

		fmt.Fprintf(os.Stderr, "connected to %s console (ctrl+] to detach)\r\n", id)

		// Trap ctrl+] (0x1d) to detach
		done := make(chan struct{})
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)

		go func() {
			defer close(done)
			io.Copy(conn, &ctrlBracketReader{r: os.Stdin, done: done})
		}()
		go io.Copy(os.Stdout, conn)

		select {
		case <-done:
		case <-sig:
		}
		return nil
	},
}

// ctrlBracketReader wraps stdin and closes done on ctrl+] (0x1d).
type ctrlBracketReader struct {
	r    io.Reader
	done chan struct{}
}

func (c *ctrlBracketReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	for i := range n {
		if p[i] == 0x1d {
			select {
			case <-c.done:
			default:
				close(c.done)
			}
			return i, io.EOF
		}
	}
	return n, err
}
