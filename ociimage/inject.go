package ociimage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// InjectGuestAgentOptions configures InjectGuestAgent.
type InjectGuestAgentOptions struct {
	// ImagePath is an existing ext4 image, modified in place.
	ImagePath string
	// GuestAgentBinary is the path to a pre-built linux guest-agent binary.
	GuestAgentBinary string
	// InitPath is where inside the rootfs to install the guest agent.
	// Defaults to "/bin/guest-agent", matching vm/vm.go's boot_args.
	InitPath string
	Logger   *slog.Logger
}

func (o InjectGuestAgentOptions) log() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
}

func (o InjectGuestAgentOptions) initPath() string {
	if o.InitPath != "" {
		return o.InitPath
	}
	return defaultInitPath
}

// InjectGuestAgent writes GuestAgentBinary into an existing ext4 image at
// InitPath, in place, clobbering whatever is already there. This is the
// upgrade path for images already registered with labctl.
//
// It works by loopback-mounting the image and writing through the real
// ext4 driver (requires root, same as the rest of labctl's Linux-host
// assumptions), rather than poking at the filesystem structures directly
// -- a prior version of this used debugfs's raw rm/write commands, which
// left the directory index inconsistent and corrupted the image (a
// dangling reference to a "deleted" inode that panicked the guest kernel
// on boot). Mounting lets the kernel handle the directory/journal
// bookkeeping correctly.
func InjectGuestAgent(ctx context.Context, opts InjectGuestAgentOptions) error {
	if opts.ImagePath == "" {
		return fmt.Errorf("ImagePath is required")
	}
	if opts.GuestAgentBinary == "" {
		return fmt.Errorf("GuestAgentBinary is required")
	}

	info, err := os.Stat(opts.GuestAgentBinary)
	if err != nil {
		return fmt.Errorf("stat guest agent binary %s: %w", opts.GuestAgentBinary, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory, not a file", opts.GuestAgentBinary)
	}

	if _, err := os.Stat(opts.ImagePath); err != nil {
		return fmt.Errorf("stat image %s: %w", opts.ImagePath, err)
	}

	for _, bin := range []string{"mount", "umount"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s not found on PATH: %w", bin, err)
		}
	}

	mountDir, err := os.MkdirTemp("", "labctl-image-upgrade-*")
	if err != nil {
		return fmt.Errorf("mkdir temp mount dir: %w", err)
	}
	defer os.Remove(mountDir)

	opts.log().Info("mounting image", "image", opts.ImagePath, "at", mountDir)
	mountCmd := exec.CommandContext(ctx, "mount", "-o", "loop", opts.ImagePath, mountDir)
	if out, err := mountCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount %s: %w: %s", opts.ImagePath, err, out)
	}
	defer func() {
		umountCmd := exec.Command("umount", mountDir)
		if out, err := umountCmd.CombinedOutput(); err != nil {
			opts.log().Error("umount failed", "dir", mountDir, "error", err, "output", string(out))
		}
	}()

	dst := filepath.Join(mountDir, filepath.Clean("/"+opts.initPath()))
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}

	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove existing %s: %w", dst, err)
	}

	opts.log().Info("writing guest agent", "src", opts.GuestAgentBinary, "dst", dst)
	if err := copyExecutable(opts.GuestAgentBinary, dst); err != nil {
		return fmt.Errorf("write guest agent: %w", err)
	}

	opts.log().Info("injected guest agent into image", "image", opts.ImagePath, "dst", opts.initPath())
	return nil
}

func copyExecutable(srcPath, dstPath string) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if err := out.Sync(); err != nil {
		return err
	}
	return os.Chmod(dstPath, 0755)
}
