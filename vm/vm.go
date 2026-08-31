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
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/128f/labctl/state"
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
	Supervisor     VMSupervisor       // nil => real systemd-run/systemctl implementation
	Forwarder      PortForwarder      // nil => real systemd-run/systemctl implementation
}

// NetworkProvisioner sets up and tears down the tap device for a VM.
type NetworkProvisioner interface {
	SetupTap(vm *state.VM) error
	TeardownTap(vm *state.VM) error
}

// VMSupervisor launches a VM's jailer/firecracker process under a
// supervised, deterministically-named unit (vm.Unit) and provides the
// primitives needed to stop it, wait for it to exit, and check whether
// it's still running — all keyed by that unit name rather than a raw pid,
// so there's never a live *os.Process handle for another labctl invocation
// to lose track of.
type VMSupervisor interface {
	// Launch starts vm's jailer process under vm.Unit and returns once the
	// unit has been accepted and started (not once the VM has booted).
	Launch(vm *state.VM) error
	// Stop stops vm's unit, blocking until it and its whole cgroup have
	// exited. A no-op, returning nil, if the unit doesn't exist.
	Stop(vm *state.VM) error
	// Wait blocks until vm's unit leaves the active/activating state,
	// returning the terminal state reached (e.g. "failed", "inactive").
	Wait(vm *state.VM) (string, error)
	// IsAlive reports whether vm's unit is currently active.
	IsAlive(vm *state.VM) bool
}

// PortForwarder starts and stops the socat process that bridges a single
// guest-initiated vsock port to a host TCP port, each running under its
// own transient, deterministically-named systemd unit — the same
// systemd-run/systemctl mechanism VMSupervisor uses for the VM itself.
type PortForwarder interface {
	// Launch starts forwarding vm's vsock port to 127.0.0.1:port, binding
	// the forwarder's unit to vm.Unit so systemd stops it automatically if
	// the VM unit dies. Idempotent: re-launching an already-running
	// forward is not an error.
	Launch(vm *state.VM, port int) error
	// Stop stops the forwarder for vmID/port. A no-op, returning nil, if
	// no such unit exists.
	Stop(vmID string, port int) error
	// Restart bounces the forwarder unit for vmID/port in place.
	Restart(vmID string, port int) error
	// IsAlive reports whether the forwarder unit for vmID/port is active.
	IsAlive(vmID string, port int) bool
}

func (r *Runner) net() NetworkProvisioner {
	if r.Net != nil {
		return r.Net
	}
	return &iproute2NetworkProvisioner{r: r}
}

func (r *Runner) supervisor() VMSupervisor {
	if r.Supervisor != nil {
		return r.Supervisor
	}
	return &systemdSupervisor{r: r}
}

func (r *Runner) portForwarder() PortForwarder {
	if r.Forwarder != nil {
		return r.Forwarder
	}
	return &systemdPortForwarder{r: r}
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

func (r *Runner) VsockPath(id string) string {
	return filepath.Join(r.vmDir(id), "root", "run", "vsock.sock")
}

// BootLogPath returns the path to the file the VM's serial console
// (ttyS0) boot output is dumped to.
func (r *Runner) BootLogPath(id string) string {
	return filepath.Join(r.vmDir(id), "console.log")
}

func (r *Runner) Run(vm *state.VM, imagePath string, attach bool) error {
	if err := r.setupChroot(vm, imagePath); err != nil {
		return err
	}
	if err := r.net().SetupTap(vm); err != nil {
		return err
	}

	if err := r.supervisor().Launch(vm); err != nil {
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

	if err := r.LaunchPortForwards(vm); err != nil {
		return err
	}
	r.startGuestPortProxies(vm)

	return r.waitOrBackground(vm, attach)
}

// waitOrBackground implements the attach/foreground-vs-detached-background
// split: if attach is false, it logs and returns immediately, leaving the
// VM running in the background. If true, it attaches to the VM's shell
// (like the `console` command) and blocks until the jailer process exits,
// the user detaches with ctrl+], or a SIGINT/SIGTERM arrives — the first
// two leave the VM running, the signal kills it.
func (r *Runner) waitOrBackground(vm *state.VM, attach bool) error {
	id := vm.ID
	if !attach {
		r.log().Info("VM running in background", "vm", id)
		return nil
	}

	r.log().Info("VM running in foreground (ctrl+] to detach, ctrl+c to stop)", "vm", id)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	exited := make(chan error, 1)
	go func() {
		state, err := r.supervisor().Wait(vm)
		if err != nil {
			exited <- err
			return
		}
		if state == "failed" {
			exited <- fmt.Errorf("unit %s exited in failed state", vm.Unit)
			return
		}
		exited <- nil
	}()

	consoleDone := make(chan error, 1)
	go func() { consoleDone <- r.AttachConsole(id) }()

	select {
	case err := <-exited:
		return err
	case <-sig:
		r.log().Info("signal received, killing VM", "vm", id)
		r.supervisor().Stop(vm)
		// Wait for the console session to unwind (killing the process
		// tears down the vsock backend, which unblocks AttachConsole) so
		// its deferred terminal restore runs before we return.
		<-consoleDone
		return nil
	case err := <-consoleDone:
		if err != nil {
			return err
		}
		// The shell connection also closes when the VM exits on its own
		// (io.Copy hits EOF); prefer that outcome over "detached" if it's
		// available.
		select {
		case err := <-exited:
			return err
		default:
			r.log().Info("detached, VM continues running in background", "vm", id)
			return nil
		}
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
	if err := run("ip", "tuntap", "del", vm.Tap, "mode", "tap"); err != nil {
		return fmt.Errorf("delete tap: %w", err)
	}
	return nil
}

// systemdSupervisor runs the real jailer/firecracker process as a
// transient systemd service unit, named vm.Unit, and manages it via
// systemd-run/systemctl.
type systemdSupervisor struct{ r *Runner }

func (s *systemdSupervisor) Launch(vm *state.VM) error {
	r := s.r

	// The guest's serial console (ttyS0, kernel boot output only — the
	// guest agent no longer forwards a shell to it) is just dumped to a
	// log file; nothing needs to read or write it interactively anymore.
	// Truncate/create up front: systemd's StandardOutput=file: property
	// appends rather than truncating.
	logPath := r.BootLogPath(vm.ID)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open console log: %w", err)
	}
	logFile.Close()

	args := []string{
		"--unit", vm.Unit,
		"--collect",
		"--property", "Delegate=yes",
		"--property", "StandardOutput=file:" + logPath,
		"--property", "StandardError=file:" + logPath,
		"--",
		r.JailerBin,
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

	if out, err := exec.Command("systemd-run", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("systemd-run: %s: %w", out, err)
	}
	return nil
}

// isKnownUnit reports whether systemd has any record of unit (loaded,
// active, or otherwise) as opposed to it never having existed.
func isKnownUnit(unit string) bool {
	out, err := exec.Command("systemctl", "show", "-p", "LoadState", "--value", unit).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != "not-found"
}

func (s *systemdSupervisor) Stop(vm *state.VM) error {
	s.r.log().Info("stopping unit", "vm", vm.ID, "unit", vm.Unit)
	out, err := exec.Command("systemctl", "stop", vm.Unit).CombinedOutput()
	if err != nil && isKnownUnit(vm.Unit) {
		return fmt.Errorf("systemctl stop %s: %s: %w", vm.Unit, out, err)
	}
	return nil
}

func (s *systemdSupervisor) IsAlive(vm *state.VM) bool {
	err := exec.Command("systemctl", "is-active", "--quiet", vm.Unit).Run()
	return err == nil
}

// Wait polls systemctl show for ActiveState until it leaves
// active/activating/deactivating, returning the terminal state string.
func (s *systemdSupervisor) Wait(vm *state.VM) (string, error) {
	for {
		out, err := exec.Command("systemctl", "show", "-p", "ActiveState", "--value", vm.Unit).Output()
		if err != nil {
			return "", fmt.Errorf("systemctl show %s: %w", vm.Unit, err)
		}
		switch state := strings.TrimSpace(string(out)); state {
		case "active", "activating", "deactivating":
			time.Sleep(300 * time.Millisecond)
		default:
			return state, nil
		}
	}
}

// socatUnitName returns the deterministic transient-unit name (bare,
// without a .service suffix) for the socat forwarder bound to vmID/port.
func socatUnitName(vmID string, port int) string {
	return fmt.Sprintf("%s-socat-%d", state.UnitName(vmID), port)
}

// systemdPortForwarder runs socat as a transient systemd service unit,
// bridging a VM's guest-initiated vsock port to a host TCP port, managed
// via systemd-run/systemctl exactly like systemdSupervisor manages the
// VM's own unit.
type systemdPortForwarder struct{ r *Runner }

func (f *systemdPortForwarder) Launch(vm *state.VM, port int) error {
	r := f.r
	unit := socatUnitName(vm.ID, port)
	sockPath := fmt.Sprintf("%s_%d", r.VsockPath(vm.ID), port)

	args := []string{
		"--unit", unit,
		"--collect",
		"--property", "BindsTo=" + vm.Unit + ".service",
		"--property", "After=" + vm.Unit + ".service",
		// Firecracker connects to this socket as r.UID/r.GID (the jailer's
		// --uid/--gid); socat must create it as the same user or that
		// connect(2) fails with EACCES (surfaced to the guest as ECONNRESET).
		"--property", fmt.Sprintf("User=%d", r.UID),
		"--property", fmt.Sprintf("Group=%d", r.GID),
		"--",
		"socat",
		fmt.Sprintf("UNIX-LISTEN:%s,fork,unlink-early", sockPath),
		fmt.Sprintf("TCP:127.0.0.1:%d", port),
	}

	r.log().Info("starting port forward", "vm", vm.ID, "port", port, "unit", unit)
	if out, err := exec.Command("systemd-run", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("systemd-run (socat %s port %d): %s: %w", vm.ID, port, out, err)
	}
	return nil
}

func (f *systemdPortForwarder) Stop(vmID string, port int) error {
	unit := socatUnitName(vmID, port)
	f.r.log().Info("stopping port forward", "vm", vmID, "port", port, "unit", unit)
	out, err := exec.Command("systemctl", "stop", unit).CombinedOutput()
	if err != nil && isKnownUnit(unit) {
		return fmt.Errorf("systemctl stop %s: %s: %w", unit, out, err)
	}
	return nil
}

func (f *systemdPortForwarder) Restart(vmID string, port int) error {
	unit := socatUnitName(vmID, port)
	f.r.log().Info("restarting port forward", "vm", vmID, "port", port, "unit", unit)
	if out, err := exec.Command("systemctl", "restart", unit).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart %s: %s: %w", unit, out, err)
	}
	return nil
}

func (f *systemdPortForwarder) IsAlive(vmID string, port int) bool {
	unit := socatUnitName(vmID, port)
	err := exec.Command("systemctl", "is-active", "--quiet", unit).Run()
	return err == nil
}

// LaunchPortForwards starts a socat forwarder for each of vm.Ports.
func (r *Runner) LaunchPortForwards(vm *state.VM) error {
	for _, port := range vm.Ports {
		if err := r.portForwarder().Launch(vm, port); err != nil {
			return err
		}
	}
	return nil
}

// StopPortForwards stops the socat forwarder for each of vm.Ports.
func (r *Runner) StopPortForwards(vm *state.VM) error {
	for _, port := range vm.Ports {
		if err := r.portForwarder().Stop(vm.ID, port); err != nil {
			return err
		}
	}
	return nil
}

// RestartPortForwards restarts the socat forwarder unit for each of
// vm.Ports in place, without changing which ports are forwarded.
func (r *Runner) RestartPortForwards(vm *state.VM) error {
	for _, port := range vm.Ports {
		if err := r.portForwarder().Restart(vm.ID, port); err != nil {
			return err
		}
	}
	return nil
}

// PortForwardAlive reports whether the socat forwarder unit for vmID/port
// is currently active.
func (r *Runner) PortForwardAlive(vmID string, port int) bool {
	return r.portForwarder().IsAlive(vmID, port)
}

// startGuestPortProxies tells vm's guest agent to start its side (the
// guest-local TCP listener) of each of vm.Ports, best-effort: this runs
// right after boot/restore, when the guest agent may not be reachable yet,
// so a failure is only logged — same tolerance as NotifyRestore.
func (r *Runner) startGuestPortProxies(vm *state.VM) {
	for _, port := range vm.Ports {
		if err := r.StartTcpVsockProxy(vm.ID, port); err != nil {
			r.log().Warn("failed to start guest-side port proxy", "vm", vm.ID, "port", port, "error", err)
		}
	}
}

func (r *Runner) Destroy(vm *state.VM) error {
	r.log().Info("sending halt signal via API", "vm", vm.ID)
	_ = apiPut(r.SocketPath(vm.ID), "/actions", map[string]string{"action_type": "SendCtrlAltDel"})

	time.Sleep(500 * time.Millisecond)

	if err := r.supervisor().Stop(vm); err != nil {
		return err
	}

	if err := r.StopPortForwards(vm); err != nil {
		return err
	}

	if err := r.net().TeardownTap(vm); err != nil {
		return err
	}

	r.log().Info("removing vm dir", "vm", vm.ID)
	return os.RemoveAll(r.vmDir(vm.ID))
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

	if err := r.supervisor().Stop(vm); err != nil {
		return err
	}

	if err := r.StopPortForwards(vm); err != nil {
		return err
	}

	if err := r.net().TeardownTap(vm); err != nil {
		return err
	}

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

	if err := r.supervisor().Launch(vm); err != nil {
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

	if err := r.NotifyRestore(vm.ID); err != nil {
		r.log().Warn("failed to notify guest agent of restore", "vm", vm.ID, "error", err)
	}

	if err := r.LaunchPortForwards(vm); err != nil {
		return err
	}
	r.startGuestPortProxies(vm)

	return r.waitOrBackground(vm, attach)
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

func (r *Runner) IsAlive(vm *state.VM) bool {
	return r.supervisor().IsAlive(vm)
}

// AttachConsole connects to the guest agent's interactive PTY vsock port
// and attaches a session to it, spawning a fresh shell process inside the
// guest. It returns when the connection closes (VM exited or the guest
// process exited) or the user detaches — either way the VM keeps running;
// detaching just disconnects this session and hangs up its shell process.
func (r *Runner) AttachConsole(id string) error {
	conn, err := r.DialVsockRetry(id, guestPtyPort, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect to guest shell: %w", err)
	}
	fmt.Fprintf(os.Stderr, "connected to %s shell (ctrl+] to detach)\r\n", id)
	return AttachSession(conn)
}

// AttachSession puts the terminal in raw mode and streams stdin/stdout to
// conn until conn closes, the user presses ctrl+] to detach, or an
// interrupt arrives — restoring the terminal before returning in every
// case. Closing conn (if that's the right thing to do) is the caller's
// responsibility.
func AttachSession(conn io.ReadWriteCloser) error {
	defer conn.Close()

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	defer term.Restore(fd, oldState)

	done := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	var once sync.Once
	closeDone := func() { once.Do(func() { close(done) }) }

	go func() {
		io.Copy(conn, &ctrlBracketReader{r: os.Stdin, detach: closeDone})
		closeDone()
	}()
	go func() {
		io.Copy(os.Stdout, conn)
		closeDone()
	}()

	select {
	case <-done:
	case <-sig:
	}
	return nil
}

// ctrlBracketReader wraps r and calls detach when it sees ctrl+] (0x1d),
// letting a console session detach without tearing anything down.
type ctrlBracketReader struct {
	r      io.Reader
	detach func()
}

func (c *ctrlBracketReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	for i := range n {
		if p[i] == 0x1d {
			c.detach()
			return i, io.EOF
		}
	}
	return n, err
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
