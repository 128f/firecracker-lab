// Package vmtest provides fakes for exercising vm.Runner's orchestration
// logic without a real Linux host, KVM, jailer, or firecracker binary.
package vmtest

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/128f/labctl/state"
)

// RecordedRequest is one HTTP request captured by FakeFirecracker.
type RecordedRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

// FakeFirecracker serves the subset of the Firecracker HTTP API that
// vm.Runner talks to, over a unix socket, recording every request it sees.
type FakeFirecracker struct {
	sockPath string

	mu       sync.Mutex
	requests []RecordedRequest
	failNext map[string]int

	listener net.Listener
	server   *http.Server
}

// NewFakeFirecracker creates a fake API server bound to sockPath. Call
// Start to begin listening once the socket's parent directory exists.
func NewFakeFirecracker(sockPath string) *FakeFirecracker {
	return &FakeFirecracker{
		sockPath: sockPath,
		failNext: make(map[string]int),
	}
}

var apiRoutes = []string{
	"/boot-source",
	"/drives/rootfs",
	"/machine-config",
	"/network-interfaces/eth0",
	"/vsock",
	"/actions",
	"/vm",
	"/snapshot/create",
	"/snapshot/load",
}

// Start creates the socket's parent directory (if needed) and begins
// serving. It is idempotent-unsafe to call twice; callers should call it
// exactly once, after the directory that will hold the socket exists.
func (f *FakeFirecracker) Start() error {
	if err := os.MkdirAll(filepath.Dir(f.sockPath), 0755); err != nil {
		return fmt.Errorf("mkdir socket dir: %w", err)
	}
	_ = os.Remove(f.sockPath)

	ln, err := net.Listen("unix", f.sockPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", f.sockPath, err)
	}
	f.listener = ln

	mux := http.NewServeMux()
	for _, route := range apiRoutes {
		mux.HandleFunc(route, f.handler(route))
	}
	f.server = &http.Server{Handler: mux}
	go f.server.Serve(ln)
	return nil
}

func (f *FakeFirecracker) handler(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		if req.Body != nil {
			_ = json.NewDecoder(req.Body).Decode(&body)
		}

		f.mu.Lock()
		f.requests = append(f.requests, RecordedRequest{
			Method: req.Method,
			Path:   path,
			Body:   body,
		})
		status := http.StatusNoContent
		if s, ok := f.failNext[path]; ok {
			status = s
			delete(f.failNext, path)
		}
		f.mu.Unlock()

		w.WriteHeader(status)
	}
}

// Requests returns the requests seen so far, in order.
func (f *FakeFirecracker) Requests() []RecordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]RecordedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

// FailNext makes the next request to path return status instead of 204.
func (f *FakeFirecracker) FailNext(path string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext[path] = status
}

// Close shuts down the fake server and removes its socket.
func (f *FakeFirecracker) Close() {
	if f.server != nil {
		f.server.Close()
	}
	if f.listener != nil {
		f.listener.Close()
	}
	_ = os.Remove(f.sockPath)
}

// FakeSupervisor is a vm.VMSupervisor that starts a FakeFirecracker and
// tracks a real, killable "sleep" child process per unit, instead of
// shelling out to systemd-run/systemctl — so tests exercise Stop/Wait/
// IsAlive against a genuine process without requiring a real systemd
// instance.
type FakeSupervisor struct {
	API *FakeFirecracker

	mu    sync.Mutex
	procs map[string]*exec.Cmd // keyed by vm.Unit
}

func (f *FakeSupervisor) Launch(vm *state.VM) error {
	if err := f.API.Start(); err != nil {
		return err
	}
	// Stand in for the real jailer with a genuine, killable child process,
	// so callers that later stop it (Destroy, Snapshot) exercise real kill
	// semantics instead of a no-op.
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start fake supervised process: %w", err)
	}
	f.mu.Lock()
	if f.procs == nil {
		f.procs = make(map[string]*exec.Cmd)
	}
	f.procs[vm.Unit] = cmd
	f.mu.Unlock()
	return nil
}

func (f *FakeSupervisor) Stop(vm *state.VM) error {
	cmd := f.process(vm)
	if cmd == nil {
		return nil // mirrors systemctl stop on an unknown unit
	}
	if err := cmd.Process.Kill(); err != nil {
		return nil
	}
	cmd.Wait() // reap; this is our own child in tests
	return nil
}

func (f *FakeSupervisor) Wait(vm *state.VM) (string, error) {
	cmd := f.process(vm)
	if cmd == nil {
		return "inactive", nil
	}
	cmd.Wait()
	return "failed", nil
}

func (f *FakeSupervisor) IsAlive(vm *state.VM) bool {
	cmd := f.process(vm)
	if cmd == nil {
		return false
	}
	return cmd.Process.Signal(syscall.Signal(0)) == nil
}

func (f *FakeSupervisor) process(vm *state.VM) *exec.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.procs[vm.Unit]
}

// LaunchedProcess returns the *exec.Cmd most recently started for vm's
// unit, so tests can inspect or Wait on it (e.g. to confirm a kill
// actually terminated it, and to reap it rather than leaving a zombie).
func (f *FakeSupervisor) LaunchedProcess(vm *state.VM) *exec.Cmd {
	return f.process(vm)
}

// FakePortForwarder is a vm.PortForwarder that records calls without
// shelling out to systemd-run/systemctl/socat.
type FakePortForwarder struct {
	mu    sync.Mutex
	alive map[string]bool // keyed by "<vmID>:<port>"
	Calls []string
}

func key(vmID string, port int) string {
	return fmt.Sprintf("%s:%d", vmID, port)
}

func (f *FakePortForwarder) Launch(vm *state.VM, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.alive == nil {
		f.alive = make(map[string]bool)
	}
	f.alive[key(vm.ID, port)] = true
	f.Calls = append(f.Calls, fmt.Sprintf("launch:%s:%d", vm.ID, port))
	return nil
}

func (f *FakePortForwarder) Stop(vmID string, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.alive, key(vmID, port))
	f.Calls = append(f.Calls, fmt.Sprintf("stop:%s:%d", vmID, port))
	return nil
}

func (f *FakePortForwarder) Restart(vmID string, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, fmt.Sprintf("restart:%s:%d", vmID, port))
	return nil
}

func (f *FakePortForwarder) IsAlive(vmID string, port int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[key(vmID, port)]
}

// NoopNetworkProvisioner is a vm.NetworkProvisioner that records calls
// without shelling out to `ip`.
type NoopNetworkProvisioner struct {
	mu    sync.Mutex
	Calls []string
}

func (n *NoopNetworkProvisioner) SetupTap(vm *state.VM) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Calls = append(n.Calls, "setup:"+vm.Tap)
	return nil
}

func (n *NoopNetworkProvisioner) TeardownTap(vm *state.VM) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Calls = append(n.Calls, "teardown:"+vm.Tap)
	return nil
}
