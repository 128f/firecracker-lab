package vm

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/128f/fctl/state"
	"github.com/128f/fctl/vm/vmtest"
)

// tempLabDir returns a short-path temp dir suitable for unix sockets.
// t.TempDir() nests deeply under macOS's per-process TMPDIR and can blow
// past the ~104-byte sun_path limit once joined with vms/<bin>/<id>/root/run.
func tempLabDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fctltest-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func fixtureVM() *state.VM {
	return &state.VM{
		ID:     "vm0",
		Tap:    "tap0",
		IP:     "172.16.0.2",
		CID:    5,
		VCPUs:  2,
		MemMiB: 512,
	}
}

func newTestRunner(t *testing.T, labDir string) *Runner {
	t.Helper()
	return &Runner{
		LabDir:         labDir,
		JailerBin:      "unused-fake-jailer",
		FirecrackerBin: "firecracker",
		UID:            os.Getuid(),
		GID:            os.Getgid(),
		Logger:         testLogger(),
	}
}

func writeFixtureImages(t *testing.T, labDir string) {
	t.Helper()
	src := func(name string) string { return filepath.Join("testdata", name) }
	for _, name := range []string{"vmlinux.bin", "rootfs.ext4"} {
		data, err := os.ReadFile(src(name))
		if os.IsNotExist(err) {
			t.Skipf("test fixture %s not available", name)
		}
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(labDir, name), data, 0644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
}

func TestRunHappyPath(t *testing.T) {
	labDir := tempLabDir(t)
	writeFixtureImages(t, labDir)

	r := newTestRunner(t, labDir)
	vm := fixtureVM()

	api := vmtest.NewFakeFirecracker(r.SocketPath(vm.ID))
	defer api.Close()
	r.Jailer = &vmtest.FakeJailerLauncher{API: api}
	noop := &vmtest.NoopNetworkProvisioner{}
	r.Net = noop

	if err := r.Run(vm, true); err != nil {
		t.Fatalf("Run: %v", err)
	}

	reqs := api.Requests()
	wantPaths := []string{
		"/boot-source",
		"/drives/rootfs",
		"/machine-config",
		"/network-interfaces/eth0",
		"/actions",
	}
	if len(reqs) != len(wantPaths) {
		t.Fatalf("got %d requests, want %d: %+v", len(reqs), len(wantPaths), reqs)
	}
	for i, want := range wantPaths {
		if reqs[i].Method != "PUT" {
			t.Errorf("request %d: method = %s, want PUT", i, reqs[i].Method)
		}
		if reqs[i].Path != want {
			t.Errorf("request %d: path = %s, want %s", i, reqs[i].Path, want)
		}
	}

	bootArgs, _ := reqs[0].Body["boot_args"].(string)
	if !strings.Contains(bootArgs, vm.IP) {
		t.Errorf("boot_args = %q, want it to contain IP %q", bootArgs, vm.IP)
	}

	mc := reqs[2].Body
	if vcpu, _ := mc["vcpu_count"].(float64); int(vcpu) != vm.VCPUs {
		t.Errorf("vcpu_count = %v, want %d", mc["vcpu_count"], vm.VCPUs)
	}
	if mem, _ := mc["mem_size_mib"].(float64); int(mem) != vm.MemMiB {
		t.Errorf("mem_size_mib = %v, want %d", mc["mem_size_mib"], vm.MemMiB)
	}

	wantMAC := fmt.Sprintf("AA:FC:00:00:%02x:%02x", vm.CID>>8, vm.CID&0xff)
	if mac, _ := reqs[3].Body["guest_mac"].(string); mac != wantMAC {
		t.Errorf("guest_mac = %q, want %q", mac, wantMAC)
	}

	if len(noop.Calls) != 1 || noop.Calls[0] != "setup:"+vm.Tap {
		t.Errorf("network calls = %v, want [setup:%s]", noop.Calls, vm.Tap)
	}
}

func TestRunErrorPathStopsAtFailure(t *testing.T) {
	labDir := tempLabDir(t)
	writeFixtureImages(t, labDir)

	r := newTestRunner(t, labDir)
	vm := fixtureVM()

	api := vmtest.NewFakeFirecracker(r.SocketPath(vm.ID))
	defer api.Close()
	api.FailNext("/machine-config", 500)
	r.Jailer = &vmtest.FakeJailerLauncher{API: api}
	r.Net = &vmtest.NoopNetworkProvisioner{}

	err := r.Run(vm, true)
	if err == nil {
		t.Fatal("Run: expected error, got nil")
	}

	reqs := api.Requests()
	if len(reqs) != 3 {
		t.Fatalf("got %d requests, want 3 (boot-source, drives/rootfs, machine-config): %+v", len(reqs), reqs)
	}
	for _, r := range reqs {
		if r.Path == "/network-interfaces/eth0" || r.Path == "/actions" {
			t.Errorf("unexpected request after failure: %s", r.Path)
		}
	}
}

func TestDestroy(t *testing.T) {
	labDir := tempLabDir(t)
	r := newTestRunner(t, labDir)
	vm := fixtureVM()

	// Stand in for what Run() would have set up: chroot dir + pid file.
	// Use a pid that (almost certainly) doesn't exist so Destroy's kill is
	// a harmless no-op rather than signaling a real process.
	runDir := filepath.Join(r.vmDir(vm.ID), "root", "run")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(r.pidPath(vm.ID), []byte("999999"), 0644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	api := vmtest.NewFakeFirecracker(r.SocketPath(vm.ID))
	defer api.Close()
	if err := api.Start(); err != nil {
		t.Fatalf("api.Start: %v", err)
	}
	noop := &vmtest.NoopNetworkProvisioner{}
	r.Net = noop

	if err := r.Destroy(vm); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	reqs := api.Requests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1: %+v", len(reqs), reqs)
	}
	if reqs[0].Path != "/actions" {
		t.Errorf("path = %s, want /actions", reqs[0].Path)
	}
	if reqs[0].Body["action_type"] != "SendCtrlAltDel" {
		t.Errorf("action_type = %v, want SendCtrlAltDel", reqs[0].Body["action_type"])
	}

	if len(noop.Calls) != 1 || noop.Calls[0] != "teardown:"+vm.Tap {
		t.Errorf("network calls = %v, want [teardown:%s]", noop.Calls, vm.Tap)
	}

	if _, err := os.Stat(r.vmDir(vm.ID)); !os.IsNotExist(err) {
		t.Errorf("vmDir still exists after Destroy: err = %v", err)
	}
}

func TestSetupChroot(t *testing.T) {
	labDir := t.TempDir()
	writeFixtureImages(t, labDir)

	r := newTestRunner(t, labDir)
	vm := fixtureVM()

	if err := r.setupChroot(vm); err != nil {
		t.Fatalf("setupChroot: %v", err)
	}

	root := filepath.Join(r.vmDir(vm.ID), "root")
	kernelDst := filepath.Join(root, "vmlinux.bin")
	rootfsDst := filepath.Join(root, "rootfs.ext4")

	kernelSrc, err := os.ReadFile(filepath.Join(labDir, "vmlinux.bin"))
	if err != nil {
		t.Fatalf("read src kernel: %v", err)
	}
	kernelGot, err := os.ReadFile(kernelDst)
	if err != nil {
		t.Fatalf("read linked kernel: %v", err)
	}
	if string(kernelGot) != string(kernelSrc) {
		t.Errorf("linked kernel content mismatch")
	}

	srcInfo, _ := os.Stat(filepath.Join(labDir, "vmlinux.bin"))
	dstInfo, _ := os.Stat(kernelDst)
	if !os.SameFile(srcInfo, dstInfo) {
		t.Errorf("kernel destination is not a hard link to the source")
	}

	rootfsSrc, err := os.ReadFile(filepath.Join(labDir, "rootfs.ext4"))
	if err != nil {
		t.Fatalf("read src rootfs: %v", err)
	}
	rootfsGot, err := os.ReadFile(rootfsDst)
	if err != nil {
		t.Fatalf("read copied rootfs: %v", err)
	}
	if string(rootfsGot) != string(rootfsSrc) {
		t.Errorf("copied rootfs content mismatch")
	}
}
