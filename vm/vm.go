package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/128f/fctl/state"
)

const bridgeName = "br0"

type Runner struct {
	DataDir        string
	SourceDir      string
	JailerBin      string
	FirecrackerBin string
	UID            int
	GID            int
	Logger         *slog.Logger
	Net            NetworkProvisioner // nil => real iproute2 implementation
	Jailer         JailerLauncher     // nil => real jailer exec implementation
}

// NetworkProvisioner sets up and tears down the tap device for a VM.
type NetworkProvisioner interface {
	SetupTap(vm *state.VM) error
	TeardownTap(vm *state.VM) error
}

// JailerLauncher starts the jailer/firecracker process for a VM.
type JailerLauncher interface {
	Launch(vm *state.VM, attach bool) (*exec.Cmd, error)
}

func (r *Runner) net() NetworkProvisioner {
	if r.Net != nil {
		return r.Net
	}
	return &iproute2NetworkProvisioner{r: r}
}

func (r *Runner) jailer() JailerLauncher {
	if r.Jailer != nil {
		return r.Jailer
	}
	return &execJailerLauncher{r: r}
}

func (r *Runner) log() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

func (r *Runner) vmDir(id string) string {
	return filepath.Join(r.DataDir, "vms", filepath.Base(r.FirecrackerBin), id)
}

func (r *Runner) SocketPath(id string) string {
	return filepath.Join(r.vmDir(id), "root", "run", "firecracker.socket")
}

func (r *Runner) ConsolePath(id string) string {
	return filepath.Join(r.vmDir(id), "root", "run", "console.sock")
}

func (r *Runner) VsockPath(id string) string {
	return filepath.Join(r.vmDir(id), "root", "run", "vsock.sock")
}

func (r *Runner) pidPath(id string) string {
	return filepath.Join(r.vmDir(id), "fctl.pid")
}

func (r *Runner) Run(vm *state.VM, imagePath string, attach bool) error {
	if err := r.setupChroot(vm, imagePath); err != nil {
		return err
	}
	if err := r.net().SetupTap(vm); err != nil {
		return err
	}

	cmd, err := r.jailer().Launch(vm, attach)
	if err != nil {
		return err
	}

	// Write the jailer/firecracker process's PID (it execve's into
	// firecracker after chroot/namespace setup, keeping this same PID for
	// the VM's whole lifetime) so destroy/snapshot can kill it later, from
	// a separate fctl invocation.
	if err := os.WriteFile(r.pidPath(vm.ID), []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0644); err != nil {
		return err
	}

	sock := r.SocketPath(vm.ID)
	r.log().Info("waiting for API socket", "vm", vm.ID, "path", sock)
	if err := waitForSocket(sock, 5*time.Second); err != nil {
		return fmt.Errorf("socket never appeared at %s: %w", sock, err)
	}

	if err := r.bootVM(vm, sock); err != nil {
		return err
	}

	return r.waitOrBackground(vm.ID, cmd, attach)
}

// waitOrBackground implements the attach/foreground-vs-detached-background
// split: if attach is false, it logs and returns immediately, leaving the
// VM running in the background; if true, it blocks until the jailer
// process exits or a SIGINT/SIGTERM arrives, killing the process on signal.
func (r *Runner) waitOrBackground(id string, cmd *exec.Cmd, attach bool) error {
	if !attach {
		r.log().Info("VM running in background", "vm", id)
		return nil
	}

	// Foreground: wait for jailer to exit or Ctrl+C.
	r.log().Info("VM running in foreground, press Ctrl+C to stop", "vm", id)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-sig:
		r.log().Info("signal received, killing VM", "vm", id)
		cmd.Process.Kill()
		return nil
	}
}

func (r *Runner) setupChroot(vm *state.VM, imagePath string) error {
	root := filepath.Join(r.vmDir(vm.ID), "root")
	runDir := filepath.Join(root, "run")

	if err := os.MkdirAll(runDir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	for _, d := range []string{root, runDir} {
		if err := os.Chown(d, r.UID, r.GID); err != nil {
			return fmt.Errorf("chown %s: %w", d, err)
		}
	}

	r.log().Info("hard-linking kernel", "vm", vm.ID)
	kernel := filepath.Join(r.SourceDir, "vmlinux.bin")
	kernelDst := filepath.Join(root, "vmlinux.bin")
	if err := os.Link(kernel, kernelDst); err != nil && !os.IsExist(err) {
		return fmt.Errorf("link kernel: %w", err)
	}
	if err := os.Chown(kernelDst, r.UID, r.GID); err != nil {
		return fmt.Errorf("chown kernel: %w", err)
	}

	r.log().Info("copying rootfs", "vm", vm.ID)
	vmRootfs := filepath.Join(root, "rootfs.ext4")
	if err := reflinkCopy(imagePath, vmRootfs); err != nil {
		return err
	}
	if err := os.Chown(vmRootfs, r.UID, r.GID); err != nil {
		return fmt.Errorf("chown rootfs: %w", err)
	}

	return nil
}

// reflinkCopy copies src to dst using a reflink where possible.
//
// --reflink is GNU-cp-only; BSD/macOS cp rejects the flag outright, so
// skip it off Linux — a test/dev-only concession, not a production
// fallback path. On Linux, --reflink=always never silently falls back
// to a full copy: it fails loudly if the data dir isn't reflink-capable.
func reflinkCopy(src, dst string) error {
	cpArgs := []string{"--reflink=always", src, dst}
	if runtime.GOOS != "linux" {
		cpArgs = []string{src, dst}
	}
	out, err := exec.Command("cp", cpArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("reflink copy %s -> %s failed (data dir must be on a reflink-capable filesystem — btrfs or xfs): %s: %w", src, dst, out, err)
	}
	return nil
}

// iproute2NetworkProvisioner shells out to `ip` to manage the tap device.
type iproute2NetworkProvisioner struct{ r *Runner }

func (n *iproute2NetworkProvisioner) SetupTap(vm *state.VM) error {
	r := n.r
	r.log().Info("configuring tap device", "vm", vm.ID, "tap", vm.Tap)
	if err := run("ip", "tuntap", "add", vm.Tap, "mode", "tap"); err != nil {
		return fmt.Errorf("create tap: %w", err)
	}
	if err := run("ip", "link", "set", vm.Tap, "master", bridgeName); err != nil {
		return fmt.Errorf("attach tap to bridge: %w", err)
	}
	if err := run("ip", "link", "set", vm.Tap, "up"); err != nil {
		return fmt.Errorf("tap up: %w", err)
	}
	return nil
}

func (n *iproute2NetworkProvisioner) TeardownTap(vm *state.VM) error {
	r := n.r
	r.log().Info("removing tap device", "vm", vm.ID, "tap", vm.Tap)
	_ = run("ip", "link", "set", vm.Tap, "down")
	_ = run("ip", "tuntap", "del", vm.Tap, "mode", "tap")
	return nil
}

// execJailerLauncher execs the real jailer/firecracker binaries.
type execJailerLauncher struct{ r *Runner }

func (j *execJailerLauncher) Launch(vm *state.VM, attach bool) (*exec.Cmd, error) {
	r := j.r
	args := []string{
		"--id", vm.ID,
		"--exec-file", r.FirecrackerBin,
		"--uid", fmt.Sprintf("%d", r.UID),
		"--gid", fmt.Sprintf("%d", r.GID),
		"--chroot-base-dir", filepath.Join(r.DataDir, "vms"),
		"--cgroup-version", "2",
		"--",
		"--api-sock", "/run/firecracker.socket",
		"--log-path", "/run/firecracker.log",
		"--level", "Debug",
	}

	cmd := exec.Command(r.JailerBin, args...)
	cmd.Stderr = os.Stderr

	if !attach {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("stdin pipe: %w", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("stdout pipe: %w", err)
		}
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("jailer start: %w", err)
		}
		go r.consoleListener(vm.ID, stdin, stdout)
	} else {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("jailer start: %w", err)
		}
	}

	return cmd, nil
}

func (r *Runner) Destroy(vm *state.VM) error {
	r.log().Info("sending halt signal via API", "vm", vm.ID)
	_ = apiPut(r.SocketPath(vm.ID), "/actions", map[string]string{"action_type": "SendCtrlAltDel"})

	time.Sleep(500 * time.Millisecond)

	r.killPid(vm.ID)

	_ = r.net().TeardownTap(vm)

	r.log().Info("removing vm dir", "vm", vm.ID)
	return os.RemoveAll(r.vmDir(vm.ID))
}

// killPid kills the process whose pid was recorded at r.pidPath(id), if any.
func (r *Runner) killPid(id string) {
	r.log().Info("killing process", "vm", id)
	if data, err := os.ReadFile(r.pidPath(id)); err == nil {
		var pid int
		fmt.Sscanf(string(data), "%d", &pid)
		if p, err := os.FindProcess(pid); err == nil {
			p.Kill()
		}
	}
}

// snapshotFiles are the names, relative to both a VM's chroot root/ dir and
// a snapshot directory, of the files a snapshot is made of.
var snapshotFiles = []string{"snapshot.bin", "mem.bin", "rootfs.ext4"}

// Snapshot pauses vm, takes a full Firecracker snapshot into snapshotDir,
// then tears the VM down exactly like Destroy (kill process, teardown tap,
// remove vmDir) — the VM is gone until Restore'd from snapshotDir.
func (r *Runner) Snapshot(vm *state.VM, snapshotDir string) error {
	sock := r.SocketPath(vm.ID)

	r.log().Info("pausing VM", "vm", vm.ID)
	if err := apiPatch(sock, "/vm", map[string]string{"state": "Paused"}); err != nil {
		return err
	}

	r.log().Info("creating snapshot", "vm", vm.ID)
	if err := apiPut(sock, "/snapshot/create", map[string]any{
		"snapshot_type": "Full",
		"snapshot_path": "/snapshot.bin",
		"mem_file_path": "/mem.bin",
	}); err != nil {
		return err
	}

	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		return fmt.Errorf("mkdir snapshot dir: %w", err)
	}
	root := filepath.Join(r.vmDir(vm.ID), "root")
	for _, f := range snapshotFiles {
		if err := reflinkCopy(filepath.Join(root, f), filepath.Join(snapshotDir, f)); err != nil {
			return fmt.Errorf("copy %s out of chroot: %w", f, err)
		}
	}

	r.killPid(vm.ID)

	_ = r.net().TeardownTap(vm)

	r.log().Info("removing vm dir", "vm", vm.ID)
	return os.RemoveAll(r.vmDir(vm.ID))
}

// Restore boots a new jailer/firecracker process for vm (a freshly
// allocated identity, distinct from whatever VM was originally
// snapshotted) and loads it from the snapshot in snapshotDir. Firecracker's
// network_overrides remaps the frozen network config's host_dev_name to
// vm's newly created tap device.
func (r *Runner) Restore(vm *state.VM, snapshotDir string, attach bool) error {
	if err := r.setupRestoreChroot(vm, snapshotDir); err != nil {
		return err
	}
	if err := r.net().SetupTap(vm); err != nil {
		return err
	}

	cmd, err := r.jailer().Launch(vm, attach)
	if err != nil {
		return err
	}

	if err := os.WriteFile(r.pidPath(vm.ID), []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0644); err != nil {
		return err
	}

	sock := r.SocketPath(vm.ID)
	r.log().Info("waiting for API socket", "vm", vm.ID, "path", sock)
	if err := waitForSocket(sock, 5*time.Second); err != nil {
		return fmt.Errorf("socket never appeared at %s: %w", sock, err)
	}

	r.log().Info("loading snapshot", "vm", vm.ID)
	if err := apiPut(sock, "/snapshot/load", map[string]any{
		"snapshot_path": "/snapshot.bin",
		"mem_backend": map[string]string{
			"backend_type": "File",
			"backend_path": "/mem.bin",
		},
		"resume_vm": true,
		"network_overrides": []map[string]string{
			{"iface_id": "eth0", "host_dev_name": vm.Tap},
		},
	}); err != nil {
		return err
	}

	return r.waitOrBackground(vm.ID, cmd, attach)
}

// setupRestoreChroot builds vm's chroot and copies the saved snapshot
// files into it, mirroring setupChroot but sourcing from a snapshot
// directory instead of a registered image. No kernel is needed — restoring
// from a snapshot never replays /boot-source.
func (r *Runner) setupRestoreChroot(vm *state.VM, snapshotDir string) error {
	root := filepath.Join(r.vmDir(vm.ID), "root")
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	for _, d := range []string{root, runDir} {
		if err := os.Chown(d, r.UID, r.GID); err != nil {
			return fmt.Errorf("chown %s: %w", d, err)
		}
	}

	r.log().Info("copying snapshot files into chroot", "vm", vm.ID)
	for _, f := range snapshotFiles {
		dst := filepath.Join(root, f)
		if err := reflinkCopy(filepath.Join(snapshotDir, f), dst); err != nil {
			return fmt.Errorf("copy %s into chroot: %w", f, err)
		}
		if err := os.Chown(dst, r.UID, r.GID); err != nil {
			return fmt.Errorf("chown %s: %w", f, err)
		}
	}
	return nil
}

func (r *Runner) IsAlive(id string) bool {
	data, err := os.ReadFile(r.pidPath(id))
	if err != nil {
		return false
	}
	var pid int
	fmt.Sscanf(string(data), "%d", &pid)
	_, err = os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}

func (r *Runner) consoleListener(id string, stdin io.WriteCloser, stdout io.ReadCloser) {
	path := r.ConsolePath(id)
	ln, err := net.Listen("unix", path)
	if err != nil {
		r.log().Error("console listener", "vm", id, "err", err)
		return
	}
	defer ln.Close()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go io.Copy(stdin, conn)
		io.Copy(conn, stdout)
		conn.Close()
	}
}

func (r *Runner) bootVM(vm *state.VM, sock string) error {
	if err := apiPut(sock, "/boot-source", map[string]string{
		"kernel_image_path": "/vmlinux.bin",
		"boot_args":         fmt.Sprintf("console=ttyS0 reboot=k panic=1 pci=off init=/bin/guest-agent ip=%s::172.16.0.1:255.255.255.0::eth0:off", vm.IP),
	}); err != nil {
		return err
	}
	if err := apiPut(sock, "/drives/rootfs", map[string]any{
		"drive_id":       "rootfs",
		"path_on_host":   "/rootfs.ext4",
		"is_root_device": true,
		"is_read_only":   false,
	}); err != nil {
		return err
	}
	if err := apiPut(sock, "/machine-config", map[string]any{
		"vcpu_count":   vm.VCPUs,
		"mem_size_mib": vm.MemMiB,
	}); err != nil {
		return err
	}
	if err := apiPut(sock, "/network-interfaces/eth0", map[string]string{
		"iface_id":      "eth0",
		"guest_mac":     fmt.Sprintf("AA:FC:00:00:%02x:%02x", vm.CID>>8, vm.CID&0xff),
		"host_dev_name": vm.Tap,
	}); err != nil {
		return err
	}
	if err := apiPut(sock, "/vsock", map[string]any{
		"vsock_id":  "vsock0",
		"guest_cid": vm.CID,
		"uds_path":  "/run/vsock.sock",
	}); err != nil {
		return err
	}
	return apiPut(sock, "/actions", map[string]string{"action_type": "InstanceStart"})
}

func apiPut(sock, path string, body any) error {
	return apiRequest(sock, http.MethodPut, path, body)
}

func apiPatch(sock, path string, body any) error {
	return apiRequest(sock, http.MethodPatch, path, body)
}

func apiRequest(sock, method, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
	}
	req, err := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API %s %s returned %d: %s", method, path, resp.StatusCode, respBody)
	}
	return nil
}

func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", path)
}

func run(args ...string) error {
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", args[0], out)
	}
	return nil
}
