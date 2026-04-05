package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var (
	flagSetupUID int
	flagSetupGID int
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "One-time host setup: bridge, cgroup parent, jailer dirs, vm user",
	RunE: func(cmd *cobra.Command, args []string) error {
		uid := fmt.Sprintf("%d", flagSetupUID)
		gid := fmt.Sprintf("%d", flagSetupGID)
		steps := [][]string{
			{"ip", "link", "add", "br0", "type", "bridge"},
			{"ip", "addr", "add", "172.16.0.1/24", "dev", "br0"},
			{"ip", "link", "set", "br0", "up"},
			{"mkdir", "-p", "/srv/jailer/firecracker"},
			{"mkdir", "-p", "/sys/fs/cgroup/fctl"},
			{"groupadd", "--system", "-g", gid, "fctl-vm"},
			{"useradd", "--system", "--no-create-home", "-u", uid, "-g", gid, "fctl-vm"},
		}
		for _, s := range steps {
			fmt.Println("+", s)
			out, err := exec.Command(s[0], s[1:]...).CombinedOutput()
			if err != nil {
				fmt.Fprintf(os.Stderr, "  warn: %s\n", out)
			}
		}
		fmt.Println("setup done")
		return nil
	},
}

func init() {
	setupCmd.Flags().IntVar(&flagSetupUID, "uid", 123, "uid for jailer vm user")
	setupCmd.Flags().IntVar(&flagSetupGID, "gid", 123, "gid for jailer vm user")
}
