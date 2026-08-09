// Package ociimage builds a bootable ext4 rootfs image from an OCI/Docker
// image reference, for use with fctl's guest-agent-as-PID1 boot model.
package ociimage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// imageConfigPath is where the pulled image's runtime config is written,
// relative to the rootfs root, so the guest agent (running as PID1) can
// read it to know what workload to execute.
const imageConfigPath = "etc/fctl/image-config.json"

// defaultInitPath matches vm/vm.go's hardcoded boot_args init= path.
// Changing InitPath away from this requires also updating vm/vm.go.
const defaultInitPath = "/bin/guest-agent"

const defaultPlatform = "linux/amd64"

// Builder pulls an OCI image reference and assembles it into a rootfs
// directory ready to be packed into an ext4 image.
type Builder struct {
	// Platform is "os/arch", e.g. "linux/amd64". Defaults to linux/amd64.
	Platform string
	// GuestAgentBinary is the path to a pre-built linux guest-agent binary.
	// This package does not compile it.
	GuestAgentBinary string
	// InitPath is where inside the rootfs to install the guest agent.
	// Defaults to "/bin/guest-agent", matching vm/vm.go's boot_args.
	InitPath string
	Logger   *slog.Logger
}

func (b *Builder) log() *slog.Logger {
	if b.Logger != nil {
		return b.Logger
	}
	return slog.Default()
}

func (b *Builder) platform() string {
	if b.Platform != "" {
		return b.Platform
	}
	return defaultPlatform
}

func (b *Builder) initPath() string {
	if b.InitPath != "" {
		return b.InitPath
	}
	return defaultInitPath
}

// ImageConfig is the subset of the OCI image config written into the
// rootfs for the guest agent to read.
type ImageConfig struct {
	Entrypoint []string `json:"entrypoint,omitempty"`
	Cmd        []string `json:"cmd,omitempty"`
	Env        []string `json:"env,omitempty"`
	WorkingDir string   `json:"working_dir,omitempty"`
	User       string   `json:"user,omitempty"`
}

// BuildResult is the output of Builder.Build. RootfsDir is a temp
// directory the caller owns and must os.RemoveAll when done.
type BuildResult struct {
	RootfsDir string
	Config    *ImageConfig
}

// Build pulls ref, flattens it into a fresh temp directory, writes its
// runtime config to imageConfigPath, and installs the guest agent binary
// at InitPath.
func (b *Builder) Build(ctx context.Context, ref string) (*BuildResult, error) {
	if b.GuestAgentBinary == "" {
		return nil, fmt.Errorf("GuestAgentBinary is required")
	}

	platform, err := parsePlatform(b.platform())
	if err != nil {
		return nil, err
	}

	b.log().Info("pulling image", "ref", ref, "platform", b.platform())
	img, err := crane.Pull(ref, crane.WithPlatform(platform), crane.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("pull %s: %w", ref, err)
	}

	cfgFile, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("read image config: %w", err)
	}

	dir, err := os.MkdirTemp("", "fctl-image-build-*")
	if err != nil {
		return nil, fmt.Errorf("mkdir temp dir: %w", err)
	}

	if err := b.export(ctx, img, dir); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	imgCfg, err := writeImageConfig(dir, cfgFile)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	if err := installGuestAgent(dir, b.GuestAgentBinary, b.initPath()); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	return &BuildResult{RootfsDir: dir, Config: imgCfg}, nil
}

// export flattens img's layers (docker-export semantics, OCI whiteouts
// resolved) as a tar stream and extracts it into dir.
func (b *Builder) export(ctx context.Context, img v1.Image, dir string) error {
	b.log().Info("exporting and extracting image filesystem", "dir", dir)

	pr, pw := io.Pipe()
	exportErr := make(chan error, 1)
	go func() {
		exportErr <- crane.Export(img, pw)
		pw.Close()
	}()

	if err := extractTar(ctx, pr, dir); err != nil {
		pr.CloseWithError(err)
		<-exportErr
		return fmt.Errorf("extract image filesystem: %w", err)
	}

	if err := <-exportErr; err != nil {
		return fmt.Errorf("export image filesystem: %w", err)
	}
	return nil
}

// extractTar extracts a tar stream into destDir by shelling out to the
// system tar binary, rather than hand-rolling extraction (path-traversal
// sanitization, mode/umask handling, symlink/hardlink/device-entry edge
// cases are all things a real tar implementation already gets right).
// Present by default on both macOS (bsdtar) and Linux (GNU tar).
func extractTar(ctx context.Context, r io.Reader, destDir string) error {
	cmd := exec.CommandContext(ctx, "tar", "-xf", "-", "-C", destDir)
	cmd.Stdin = r
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tar: %w: %s", err, stderr.String())
	}
	return nil
}

// writeImageConfig writes cfg's runtime fields (Entrypoint/Cmd/Env/
// WorkingDir/User) as JSON into rootfsDir at imageConfigPath.
func writeImageConfig(rootfsDir string, cfg *v1.ConfigFile) (*ImageConfig, error) {
	imgCfg := &ImageConfig{
		Entrypoint: cfg.Config.Entrypoint,
		Cmd:        cfg.Config.Cmd,
		Env:        cfg.Config.Env,
		WorkingDir: cfg.Config.WorkingDir,
		User:       cfg.Config.User,
	}

	dst := filepath.Join(rootfsDir, imageConfigPath)
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}

	data, err := json.MarshalIndent(imgCfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal image config: %w", err)
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return nil, fmt.Errorf("write %s: %w", dst, err)
	}
	return imgCfg, nil
}

// installGuestAgent copies the binary at srcPath into rootfsDir at
// initPath, mode 0755 regardless of the source file's mode.
func installGuestAgent(rootfsDir, srcPath, initPath string) error {
	info, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("stat guest agent binary %s: %w", srcPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory, not a file", srcPath)
	}

	dst := filepath.Join(rootfsDir, filepath.Clean("/"+initPath))
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}

	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy guest agent binary: %w", err)
	}
	if err := out.Sync(); err != nil {
		return err
	}
	return os.Chmod(dst, 0755)
}

// parsePlatform parses "os/arch" into a *v1.Platform.
func parsePlatform(s string) (*v1.Platform, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid --platform %q (want os/arch, e.g. linux/amd64)", s)
	}
	return &v1.Platform{OS: parts[0], Architecture: parts[1]}, nil
}
