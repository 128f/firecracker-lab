package ociimage

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
)

const defaultMkfsBin = "mkfs.ext4"

// PackExt4Options configures ext4 packing via PackExt4.
type PackExt4Options struct {
	// RootfsDir is the populated directory to pack.
	RootfsDir string
	// OutPath is the destination .ext4 file path. Must not already exist.
	OutPath string
	// Size is passed straight through to mkfs.ext4, e.g. "2048M".
	Size string
	// MkfsBin is the mkfs.ext4 binary to invoke. Defaults to "mkfs.ext4"
	// on PATH.
	MkfsBin string
	Logger  *slog.Logger
}

func (o PackExt4Options) log() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
}

func (o PackExt4Options) mkfsBin() string {
	if o.MkfsBin != "" {
		return o.MkfsBin
	}
	return defaultMkfsBin
}

// PackExt4 packs RootfsDir into an ext4 image at OutPath by shelling out
// directly to the host's mkfs.ext4 (e2fsprogs). mkfs.ext4 is Linux-only;
// this is expected to fail with an actionable error on non-Linux hosts,
// same as the rest of labctl assumes a Linux host with firecracker/jailer/ip
// on PATH.
func PackExt4(ctx context.Context, opts PackExt4Options) error {
	if _, err := os.Stat(opts.OutPath); err == nil {
		return fmt.Errorf("output path %s already exists (remove it first)", opts.OutPath)
	}

	mkfsBin := opts.mkfsBin()
	if _, err := exec.LookPath(mkfsBin); err != nil {
		return fmt.Errorf("%s not found on PATH (install e2fsprogs): %w", mkfsBin, err)
	}

	opts.log().Info("packing ext4 image", "rootfs", opts.RootfsDir, "out", opts.OutPath, "size", opts.Size)

	cmd := exec.CommandContext(ctx, mkfsBin, "-F", "-d", opts.RootfsDir, "-b", "4096", opts.OutPath, opts.Size)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", mkfsBin, err, out)
	}

	opts.log().Info("packed ext4 image", "out", opts.OutPath)
	return nil
}
