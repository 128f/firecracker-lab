package cmd

import (
	"fmt"
	"net"
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

	lanCIDR, err := interfaceCIDR(wan)
	if err != nil {
		return fmt.Errorf("could not detect LAN CIDR on %s: %w", wan, err)
	}
	fmt.Printf("detected LAN CIDR on %s: %s\n", wan, lanCIDR)

	steps := [][]string{
		{"ip", "link", "add", "br0", "type", "bridge"},
		{"ip", "addr", "add", "172.16.0.1/24", "dev", "br0"},
		{"ip", "link", "set", "br0", "up"},
		{"sysctl", "-w", "net.ipv4.ip_forward=1"},
		{"mkdir", "-p", "/srv/jailer/firecracker"},
		{"groupadd", "--system", "-g", gid, "labctl-vm"},
		{"useradd", "--system", "--no-create-home", "-u", uid, "-g", gid, "labctl-vm"},
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

	if err := applyNftRuleset(wan, lanCIDR); err != nil {
		fmt.Fprintf(os.Stderr, "  warn: nft ruleset: %s\n", err)
	}

	fmt.Println("setup done")
	return nil
}

// applyNftRuleset loads the VM networking policy as a single atomic nftables
// transaction: VMs on br0 may reach the internet and each other, but not the
// host's LAN (lanCIDR), and not the host itself.
func applyNftRuleset(wan, lanCIDR string) error {
	ruleset := fmt.Sprintf(`
table inet labctl {
	chain forward {
		type filter hook forward priority 0; policy drop;
		iifname "br0" ip daddr %[2]s drop
		iifname "br0" oifname "%[1]s" accept
		iifname "%[1]s" oifname "br0" ct state established,related accept
	}
	chain input {
		type filter hook input priority 0; policy accept;
		iifname "br0" ct state established,related accept
		iifname "br0" drop
	}
	chain postrouting {
		type nat hook postrouting priority 100;
		ip saddr 172.16.0.0/24 ip daddr != %[2]s oifname "%[1]s" masquerade
	}
}
`, wan, lanCIDR)

	fmt.Println("+ nft -f - <<ruleset>>")
	fmt.Print(ruleset)
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(ruleset)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", out, err)
	}
	return nil
}

// interfaceCIDR returns the CIDR of the first IPv4 address on iface, e.g. "192.168.8.0/24".
func interfaceCIDR(iface string) (string, error) {
	out, err := exec.Command("ip", "-o", "-4", "addr", "show", "dev", iface).Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "inet" && i+1 < len(fields) {
			_, ipNet, err := net.ParseCIDR(fields[i+1])
			if err != nil {
				return "", err
			}
			return ipNet.String(), nil
		}
	}
	return "", fmt.Errorf("no inet address found on %s", iface)
}
