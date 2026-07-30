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

	"github.com/128f/fctl/state"
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
	"/actions",
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

// FakeJailerLauncher is a vm.JailerLauncher that starts a FakeFirecracker
// instead of exec'ing the real jailer/firecracker binaries.
type FakeJailerLauncher struct {
	API *FakeFirecracker
}

func (f *FakeJailerLauncher) Launch(vm *state.VM, detach bool) (*exec.Cmd, error) {
	if err := f.API.Start(); err != nil {
		return nil, err
	}
	return nil, nil
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
