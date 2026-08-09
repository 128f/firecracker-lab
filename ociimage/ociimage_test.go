package ociimage

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// buildTar writes the given entries into an in-memory tar stream.
type tarEntry struct {
	name     string
	typeflag byte
	mode     int64
	linkname string
	body     []byte
}

func buildTar(t *testing.T, entries []tarEntry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Mode:     e.mode,
			Linkname: e.linkname,
			Size:     int64(len(e.body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", e.name, err)
		}
		if len(e.body) > 0 {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatalf("write body %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	return &buf
}

func TestExtractTarRegularFilesDirsSymlinks(t *testing.T) {
	entries := []tarEntry{
		{name: "etc/", typeflag: tar.TypeDir, mode: 0755},
		{name: "etc/motd", typeflag: tar.TypeReg, mode: 0640, body: []byte("hello\n")},
		{name: "bin/", typeflag: tar.TypeDir, mode: 0755},
		{name: "bin/sh", typeflag: tar.TypeSymlink, mode: 0777, linkname: "busybox"},
	}
	buf := buildTar(t, entries)

	destDir := t.TempDir()
	if err := extractTar(context.Background(), buf, destDir); err != nil {
		t.Fatalf("extractTar: %v", err)
	}

	motd := filepath.Join(destDir, "etc/motd")
	data, err := os.ReadFile(motd)
	if err != nil {
		t.Fatalf("read %s: %v", motd, err)
	}
	if string(data) != "hello\n" {
		t.Errorf("motd content = %q, want %q", data, "hello\n")
	}
	info, err := os.Stat(motd)
	if err != nil {
		t.Fatalf("stat %s: %v", motd, err)
	}
	if info.Mode().Perm() != 0640 {
		t.Errorf("motd mode = %o, want %o", info.Mode().Perm(), 0640)
	}

	link := filepath.Join(destDir, "bin/sh")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink %s: %v", link, err)
	}
	if target != "busybox" {
		t.Errorf("symlink target = %q, want %q", target, "busybox")
	}
}

func TestExtractTarPathTraversalContained(t *testing.T) {
	entries := []tarEntry{
		{name: "../../etc/passwd", typeflag: tar.TypeReg, mode: 0644, body: []byte("evil\n")},
	}
	buf := buildTar(t, entries)

	destDir := t.TempDir()
	// Either extractTar errors on the malicious entry, or the system tar
	// silently strips/contains it under destDir. Either way, nothing must
	// land outside destDir.
	_ = extractTar(context.Background(), buf, destDir)

	escaped := filepath.Join(filepath.Dir(filepath.Dir(destDir)), "etc/passwd")
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("path traversal entry escaped destDir: found %s", escaped)
	}
}

func TestWriteImageConfig(t *testing.T) {
	cfg := &v1.ConfigFile{
		Config: v1.Config{
			Entrypoint: []string{"/bin/myapp"},
			Cmd:        []string{"--flag"},
			Env:        []string{"FOO=bar"},
			WorkingDir: "/app",
			User:       "1000:1000",
		},
	}

	dir := t.TempDir()
	got, err := writeImageConfig(dir, cfg)
	if err != nil {
		t.Fatalf("writeImageConfig: %v", err)
	}
	if got.WorkingDir != "/app" {
		t.Errorf("returned WorkingDir = %q, want /app", got.WorkingDir)
	}

	data, err := os.ReadFile(filepath.Join(dir, imageConfigPath))
	if err != nil {
		t.Fatalf("read image config: %v", err)
	}
	var written ImageConfig
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatalf("unmarshal image config: %v", err)
	}
	if len(written.Entrypoint) != 1 || written.Entrypoint[0] != "/bin/myapp" {
		t.Errorf("Entrypoint = %v, want [/bin/myapp]", written.Entrypoint)
	}
	if len(written.Cmd) != 1 || written.Cmd[0] != "--flag" {
		t.Errorf("Cmd = %v, want [--flag]", written.Cmd)
	}
	if len(written.Env) != 1 || written.Env[0] != "FOO=bar" {
		t.Errorf("Env = %v, want [FOO=bar]", written.Env)
	}
	if written.User != "1000:1000" {
		t.Errorf("User = %q, want 1000:1000", written.User)
	}
}

func TestInstallGuestAgent(t *testing.T) {
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "guest-agent")
	if err := os.WriteFile(src, []byte("fake binary"), 0644); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	rootfsDir := t.TempDir()
	if err := installGuestAgent(rootfsDir, src, "/bin/guest-agent"); err != nil {
		t.Fatalf("installGuestAgent: %v", err)
	}

	dst := filepath.Join(rootfsDir, "bin/guest-agent")
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat %s: %v", dst, err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("installed mode = %o, want 0755 (source was 0644)", info.Mode().Perm())
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read %s: %v", dst, err)
	}
	if string(data) != "fake binary" {
		t.Errorf("content = %q, want %q", data, "fake binary")
	}
}
