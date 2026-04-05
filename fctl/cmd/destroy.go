package cmd

import (
	"fmt"

	"github.com/128f/fctl/state"
	"github.com/128f/fctl/vm"
	"github.com/spf13/cobra"
)

var destroyCmd = &cobra.Command{
	Use:   "destroy <id>",
	Short: "Stop and remove a VM",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		s, err := state.Load(state.StatePath(labDir))
		if err != nil {
			return err
		}
		v, ok := s.VMs[id]
		if !ok {
			return fmt.Errorf("unknown VM: %s", id)
		}
		r := &vm.Runner{LabDir: labDir}
		fmt.Printf("destroying %s...\n", id)
		if err := r.Destroy(v); err != nil {
			return err
		}
		return s.Remove(id)
	},
}
