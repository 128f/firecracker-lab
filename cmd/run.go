package cmd

import (
	"fmt"

	"github.com/128f/fctl/state"
	"github.com/128f/fctl/vm"
	"github.com/spf13/cobra"
)

func newRunCmd(cfg *Config) *cobra.Command {
	var (
		flagVCPUs         int
		flagMemMiB        int
		flagCount         int
		flagUID           int
		flagGID           int
		flagAttachConsole bool
		flagJailerBin     string
		flagImage         string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Create and run one or more VMs",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := state.Load(state.DBPath(cfg.DataDir))
			if err != nil {
				return err
			}
			defer s.Close()

			img, err := resolveImage(s, flagImage)
			if err != nil {
				return err
			}

			r := &vm.Runner{
				DataDir:        cfg.DataDir,
				SourceDir:      cfg.SourceDir,
				JailerBin:      flagJailerBin,
				FirecrackerBin: cfg.FCBin,
				UID:            flagUID,
				GID:            flagGID,
			}

			for range flagCount {
				v, err := s.AllocateAndInsert(flagVCPUs, flagMemMiB, img.ID)
				if err != nil {
					return err
				}

				fmt.Printf("running %s (tap=%s ip=%s cid=%d image=%s)...\n", v.ID, v.Tap, v.IP, v.CID, img.Name)

				if err := r.Run(v, img.Path, flagAttachConsole); err != nil {
					return fmt.Errorf("run %s: %w", v.ID, err)
				}
				fmt.Printf("started %s\n", v.ID)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&flagVCPUs, "vcpus", 1, "vCPU count")
	cmd.Flags().IntVar(&flagMemMiB, "mem", 256, "memory in MiB")
	cmd.Flags().IntVar(&flagCount, "count", 1, "number of VMs to create")
	cmd.Flags().IntVar(&flagUID, "uid", 123, "uid for jailer vm user")
	cmd.Flags().IntVar(&flagGID, "gid", 123, "gid for jailer vm user")
	cmd.Flags().BoolVarP(&flagAttachConsole, "attach-console", "a", false, "run VM in foreground, attached to its console (default: detached, runs in background)")
	cmd.Flags().StringVar(&flagJailerBin, "jailer", defaultJailerBin(), "path to jailer binary (env: FCTL_JAILER_BIN)")
	cmd.Flags().StringVar(&flagImage, "image", "", "name of the registered image to boot (default: the only registered image, if there's exactly one)")

	return cmd
}

// resolveImage resolves the --image flag to a registered image, defaulting
// to the sole registered image when name is empty and exactly one exists.
func resolveImage(s *state.State, name string) (*state.Image, error) {
	if name != "" {
		img, err := s.GetImageByName(name)
		if err != nil {
			return nil, err
		}
		if img == nil {
			return nil, fmt.Errorf("unknown image: %s (see `fctl image list`)", name)
		}
		return img, nil
	}

	images, err := s.ListImages()
	if err != nil {
		return nil, err
	}
	switch len(images) {
	case 0:
		return nil, fmt.Errorf("no images registered; import one with `fctl image import <path> --name <name>`")
	case 1:
		return images[0], nil
	default:
		return nil, fmt.Errorf("--image is required: %d images registered (see `fctl image list`)", len(images))
	}
}
