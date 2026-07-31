package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

var (
	flagSetupUID int
	flagSetupGID int
	flagSetupWAN string
)

func newSetupCmd(cfg *Config) *cobra.Command {
	setupCmd := &cobra.Command{
		Use:   "setup",
		Short: "One-time host setup: bridge, cgroup parent, jailer dirs, vm user, data dir",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetup(cfg)
		},
	}
	setupCmd.Flags().IntVar(&flagSetupUID, "uid", 123, "uid for jailer vm user")
	setupCmd.Flags().IntVar(&flagSetupGID, "gid", 123, "gid for jailer vm user")
	setupCmd.Flags().StringVar(&flagSetupWAN, "wan", "", "WAN interface for NAT (auto-detected from default route if empty)")
	return setupCmd
}

func runSetup(cfg *Config) error {
	uid := fmt.Sprintf("%d", flagSetupUID)
	gid := fmt.Sprintf("%d", flagSetupGID)

	wan := flagSetupWAN
	if wan == "" {
		out, err := exec.Command("ip", "route", "show", "default").Output()
		if err != nil {
			return fmt.Errorf("could not detect default route interface: %w (use --wan to set manually)", err)
		}
		fields := strings.Fields(string(out))
		for i, f := range fields {
			if f == "dev" && i+1 < len(fields) {
				wan = fields[i+1]
				break
			}
		}
		if wan == "" {
			return fmt.Errorf("no default route found (use --wan to set manually)")
		}
		fmt.Printf("detected WAN interface: %s\n", wan)
	}

	steps := [][]string{
		{"ip", "link", "add", "br0", "type", "bridge"},
		{"ip", "addr", "add", "172.16.0.1/24", "dev", "br0"},
		{"ip", "link", "set", "br0", "up"},
		{"sysctl", "-w", "net.ipv4.ip_forward=1"},
		{"iptables", "-t", "nat", "-A", "POSTROUTING", "-s", "172.16.0.0/24", "-o", wan, "-j", "MASQUERADE"},
		{"iptables", "-A", "FORWARD", "-i", "br0", "-o", wan, "-j", "ACCEPT"},
		{"iptables", "-A", "FORWARD", "-i", wan, "-o", "br0", "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		{"mkdir", "-p", "/srv/jailer/firecracker"},
		{"mkdir", "-p", "/sys/fs/cgroup/fctl"},
		{"groupadd", "--system", "-g", gid, "fctl-vm"},
		{"useradd", "--system", "--no-create-home", "-u", uid, "-g", gid, "fctl-vm"},
		{"mkdir", "-p", cfg.DataDir},
		{"chown", fmt.Sprintf("%s:%s", uid, gid), cfg.DataDir},
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
}
