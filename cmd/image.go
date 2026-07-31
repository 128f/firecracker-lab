package cmd

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/128f/fctl/state"
	"github.com/spf13/cobra"
)

func newImageCmd(cfg *Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage the base rootfs image registry",
	}
	cmd.AddCommand(newImageImportCmd(cfg))
	cmd.AddCommand(newImageListCmd(cfg))
	return cmd
}

func newImageImportCmd(cfg *Config) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "import <path>",
		Short: "Import a rootfs image into the registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			src := args[0]

			if err := checkExt4(src); err != nil {
				return err
			}

			s, err := state.Load(state.DBPath(cfg.DataDir))
			if err != nil {
				return err
			}
			defer s.Close()

			existing, err := s.GetImageByName(name)
			if err != nil {
				return err
			}
			if existing != nil {
				return fmt.Errorf("image %q is already registered (path: %s)", name, existing.Path)
			}

			imagesDir := filepath.Join(cfg.DataDir, "images")
			if err := os.MkdirAll(imagesDir, 0755); err != nil {
				return fmt.Errorf("mkdir images dir: %w", err)
			}
			dst := filepath.Join(imagesDir, name+".ext4")

			size, err := copyFile(src, dst)
			if err != nil {
				return fmt.Errorf("copy image: %w", err)
			}

			img, err := s.InsertImage(name, dst, size)
			if err != nil {
				return err
			}
			fmt.Printf("imported %s (%d bytes) -> %s\n", img.Name, img.SizeBytes, img.Path)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name to register the image under (required)")
	return cmd
}

func newImageListCmd(cfg *Config) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered images",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := state.Load(state.DBPath(cfg.DataDir))
			if err != nil {
				return err
			}
			defer s.Close()

			images, err := s.ListImages()
			if err != nil {
				return err
			}
			if len(images) == 0 {
				fmt.Println("no images registered")
				return nil
			}
			fmt.Printf("%-16s %-12s %s\n", "NAME", "SIZE", "PATH")
			for _, img := range images {
				fmt.Printf("%-16s %-12d %s\n", img.Name, img.SizeBytes, img.Path)
			}
			return nil
		},
	}
}

func copyFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	n, err := io.Copy(out, in)
	if err != nil {
		return 0, err
	}
	return n, out.Sync()
}

// checkExt4 does a light sanity check that path looks like an ext2/3/4
// image: big enough to hold a superblock, with the ext magic number
// (0xEF53) at its expected offset. Not full validation — this is a
// single-operator tool, not a multi-tenant upload path.
func checkExt4(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory, not a file", path)
	}
	if info.Size() < 1024+64 {
		return fmt.Errorf("%s is too small to be a valid ext4 image", path)
	}

	const superblockMagicOffset = 1024 + 56
	buf := make([]byte, 2)
	if _, err := f.ReadAt(buf, superblockMagicOffset); err != nil {
		return fmt.Errorf("read superblock: %w", err)
	}
	if magic := binary.LittleEndian.Uint16(buf); magic != 0xEF53 {
		return fmt.Errorf("%s does not look like an ext2/3/4 image (bad superblock magic)", path)
	}
	return nil
}
