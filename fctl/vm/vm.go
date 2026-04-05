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
	"path/filepath"
	"time"

	"github.com/128f/fctl/state"
)

const bridgeName = "br0"

type Runner struct {
	LabDir         string
	JailerBin      string
	FirecrackerBin string
	UID            int
	GID            int
	Logger         *slog.Logger
}

func (r *Runner) log() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

func (r *Runner) vmDir(id string) string {
	return filepath.Join(r.LabDir, "vms", filepath.Base(r.FirecrackerBin), id)
}

func (r *Runner) socketPath(id string) string {
	return filepath.Join(r.vmDir(id), "root", "run", "firecracker.socket")
}

func (r *Runner) ConsolePath(id string) string {
	return filepath.Join(r.vmDir(id), "root", "run", "console.sock")
}

func (r *Runner) pidPath(id string) string {
	return filepath.Join(r.vmDir(id), "firecracker.pid")
}

func (r *Runner) Create(vm *state.VM) error {
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
	kernel := filepath.Join(r.LabDir, "vmlinux.bin")
	kernelDst := filepath.Join(root, "vmlinux.bin")
	if err := os.Link(kernel, kernelDst); err != nil && !os.IsExist(err) {
		return fmt.Errorf("link kernel: %w", err)
	}
	if err := os.Chown(kernelDst, r.UID, r.GID); err != nil {
		return fmt.Errorf("chown kernel: %w", err)
	}

	r.log().Info("copying rootfs", "vm", vm.ID)
	baseRootfs := filepath.Join(r.LabDir, "rootfs.ext4")
	vmRootfs := filepath.Join(root, "rootfs.ext4")
	out, err := exec.Command("cp", "--reflink=auto", baseRootfs, vmRootfs).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cp rootfs: %s: %w", out, err)
	}
	if err := os.Chown(vmRootfs, r.UID, r.GID); err != nil {
		return fmt.Errorf("chown rootfs: %w", err)
	}

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

func (r *Runner) Start(vm *state.VM) error {
	args := []string{
		"--id", vm.ID,
		"--exec-file", r.FirecrackerBin,
		"--uid", fmt.Sprintf("%d", r.UID),
		"--gid", fmt.Sprintf("%d", r.GID),
		"--chroot-base-dir", filepath.Join(r.LabDir, "vms"),
		"--cgroup-version", "2",
		"--",
		"--api-sock", "/run/firecracker.socket",
		"--log-path", "/run/firecracker.log",
		"--level", "Debug",
	}

	cmd := exec.Command(r.JailerBin, args...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("jailer start: %w", err)
	}
	go r.consoleListener(vm.ID, stdin, stdout)

	if err := os.WriteFile(r.pidPath(vm.ID), []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0644); err != nil {
		return err
	}

	sock := r.socketPath(vm.ID)
	fmt.Fprintf(os.Stderr, "waiting for socket: %s\n", sock)
	if err := waitForSocket(sock, 5*time.Second); err != nil {
		return fmt.Errorf("socket never appeared at %s: %w", sock, err)
	}

	return r.bootVM(vm, sock)
}

func (r *Runner) Destroy(vm *state.VM) error {
	r.log().Info("sending halt signal via API", "vm", vm.ID)
	_ = apiPut(r.socketPath(vm.ID), "/actions", map[string]string{"action_type": "SendCtrlAltDel"})

	time.Sleep(500 * time.Millisecond)

	r.log().Info("killing process", "vm", vm.ID)
	if data, err := os.ReadFile(r.pidPath(vm.ID)); err == nil {
		var pid int
		fmt.Sscanf(string(data), "%d", &pid)
		if p, err := os.FindProcess(pid); err == nil {
			p.Kill()
		}
	}

	r.log().Info("removing tap device", "vm", vm.ID, "tap", vm.Tap)
	_ = run("ip", "link", "set", vm.Tap, "down")
	_ = run("ip", "tuntap", "del", vm.Tap, "mode", "tap")

	r.log().Info("removing vm dir", "vm", vm.ID)
	return os.RemoveAll(r.vmDir(vm.ID))
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
		"boot_args": fmt.Sprintf("console=ttyS0 reboot=k panic=1 pci=off ip=%s::172.16.0.1:255.255.255.0::eth0:off", vm.IP),
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
	return apiPut(sock, "/actions", map[string]string{"action_type": "InstanceStart"})
}

func apiPut(sock, path string, body any) error {
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
	req, err := http.NewRequest(http.MethodPut, "http://localhost"+path, bytes.NewReader(data))
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
		return fmt.Errorf("API %s returned %d: %s", path, resp.StatusCode, respBody)
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
