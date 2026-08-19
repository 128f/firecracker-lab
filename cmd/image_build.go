package cmd

import (
	"fmt"
	"os"

	"github.com/128f/fctl/ociimage"
	"github.com/spf13/cobra"
)

func newImageBuildCmd(cfg *Config) *cobra.Command {
	var (
		flagPlatform         string
		flagGuestAgentBinary string
		flagInitPath         string
		flagOutput           string
		flagSize             string
		flagLocal            bool
	)

	cmd := &cobra.Command{
		Use:   "build <ref>",
		Short: "Build a bootable ext4 rootfs from an OCI/Docker image reference",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			if flagGuestAgentBinary == "" {
				return fmt.Errorf("--guest-agent-binary is required (see guest-agent/build.sh for how to build one)")
			}
			if flagOutput == "" {
				return fmt.Errorf("-o/--output is required")
			}

			b := &ociimage.Builder{
				Platform:         flagPlatform,
				GuestAgentBinary: flagGuestAgentBinary,
				InitPath:         flagInitPath,
				Local:            flagLocal,
			}

			if flagLocal {
				fmt.Printf("loading %s from local docker daemon...\n", ref)
			} else {
				fmt.Printf("pulling %s (%s)...\n", ref, flagPlatform)
			}
			result, err := b.Build(cmd.Context(), ref)
			if err != nil {
				return fmt.Errorf("build image: %w", err)
			}
			defer os.RemoveAll(result.RootfsDir)

			fmt.Printf("packing ext4 image (%s)...\n", flagSize)
			if err := ociimage.PackExt4(cmd.Context(), ociimage.PackExt4Options{
				RootfsDir: result.RootfsDir,
				OutPath:   flagOutput,
				Size:      flagSize,
			}); err != nil {
				return fmt.Errorf("pack ext4: %w", err)
			}

			fmt.Printf("built %s\n", flagOutput)
			fmt.Printf("next: fctl image import %s --name <name>\n", flagOutput)
			return nil
		},
	}

	cmd.Flags().StringVar(&flagPlatform, "platform", envOr("FCTL_IMAGE_PLATFORM", "linux/amd64"), "target platform for the pulled image (this repo assumes x86_64 throughout) (env: FCTL_IMAGE_PLATFORM)")
	cmd.Flags().StringVar(&flagGuestAgentBinary, "guest-agent-binary", envOr("FCTL_GUEST_AGENT_BINARY", ""), "path to a pre-built linux guest-agent binary (required; see guest-agent/build.sh) (env: FCTL_GUEST_AGENT_BINARY)")
	cmd.Flags().StringVar(&flagInitPath, "init-path", envOr("FCTL_IMAGE_INIT_PATH", "/bin/guest-agent"), "path inside the rootfs to install the guest agent at (changing this requires also updating vm/vm.go's boot_args) (env: FCTL_IMAGE_INIT_PATH)")
	cmd.Flags().StringVarP(&flagOutput, "output", "o", "", "output .ext4 file path (required)")
	cmd.Flags().StringVar(&flagSize, "size", envOr("FCTL_IMAGE_SIZE", "2048M"), "size of the ext4 filesystem (passed to mkfs.ext4) (env: FCTL_IMAGE_SIZE)")
	cmd.Flags().BoolVar(&flagLocal, "local", false, "load ref from the local docker daemon instead of pulling from a remote registry")

	return cmd
}
