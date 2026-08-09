//go:build integration

package ociimage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestBuildPullsAndExtractsAlpine exercises the real crane.Pull/crane.Export
// path against a small public image. Requires network access.
func TestBuildPullsAndExtractsAlpine(t *testing.T) {
	agentSrc := filepath.Join(t.TempDir(), "fake-guest-agent")
	if err := os.WriteFile(agentSrc, []byte("fake"), 0755); err != nil {
		t.Fatalf("write fake guest agent: %v", err)
	}

	b := &Builder{
		Platform:         "linux/amd64",
		GuestAgentBinary: agentSrc,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := b.Build(ctx, "alpine:3.20")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer os.RemoveAll(result.RootfsDir)

	for _, p := range []string{"etc/os-release", "bin/busybox", "etc/fctl/image-config.json", "bin/guest-agent"} {
		if _, err := os.Stat(filepath.Join(result.RootfsDir, p)); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
}

// TestPackExt4RealMkfs exercises the real mkfs.ext4 shell-out. Skipped if
// mkfs.ext4 isn't on PATH, which is the expected case on macOS dev
// machines without e2fsprogs installed — this can only run for real on
// the Linux host fctl actually runs on.
func TestPackExt4RealMkfs(t *testing.T) {
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 not on PATH (expected on non-Linux dev machines)")
	}

	rootfsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootfsDir, "hello"), []byte("hi\n"), 0644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "test.ext4")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := PackExt4(ctx, PackExt4Options{
		RootfsDir: rootfsDir,
		OutPath:   outPath,
		Size:      "64M",
	}); err != nil {
		t.Fatalf("PackExt4: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if info.Size() == 0 {
		t.Error("output ext4 file is empty")
	}

	// Sanity-check the ext2/3/4 superblock magic, same check
	// cmd/image.go's checkExt4 performs on import.
	f, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer f.Close()
	magic := make([]byte, 2)
	if _, err := f.ReadAt(magic, 1024+56); err != nil {
		t.Fatalf("read superblock: %v", err)
	}
	if magic[0] != 0x53 || magic[1] != 0xEF {
		t.Errorf("bad ext4 superblock magic: %x", magic)
	}
}
