package vm

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/128f/labctl/state"
	"github.com/128f/labctl/vm/vmtest"
)

// tempLabDir returns a short-path temp dir suitable for unix sockets.
// t.TempDir() nests deeply under macOS's per-process TMPDIR and can blow
// past the ~104-byte sun_path limit once joined with vms/<bin>/<id>/root/run.
func tempLabDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "labctltest-")
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
		Unit:   state.UnitName("vm0"),
	}
}

func newTestRunner(t *testing.T, labDir string) *Runner {
	t.Helper()
	return &Runner{
		DataDir:        labDir,
		SourceDir:      labDir,
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

// killTestProcess registers a best-effort cleanup that stops vm's
// supervised process, for tests that launch a (fake) jailer process via
// Run/Restore without tearing it down themselves via Destroy/Snapshot.
func killTestProcess(t *testing.T, r *Runner, vm *state.VM) {
	t.Helper()
	t.Cleanup(func() { r.supervisor().Stop(vm) })
}

func TestRunHappyPath(t *testing.T) {
	labDir := tempLabDir(t)
	writeFixtureImages(t, labDir)

	r := newTestRunner(t, labDir)
	vm := fixtureVM()

	api := vmtest.NewFakeFirecracker(r.SocketPath(vm.ID))
	defer api.Close()
	r.Supervisor = &vmtest.FakeSupervisor{API: api}
	noop := &vmtest.NoopNetworkProvisioner{}
	r.Net = noop

	if err := r.Run(vm, filepath.Join(labDir, "rootfs.ext4"), false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	killTestProcess(t, r, vm)

	reqs := api.Requests()
	wantPaths := []string{
		"/boot-source",
		"/drives/rootfs",
		"/machine-config",
		"/network-interfaces/eth0",
		"/vsock",
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

func TestRunLaunchesPortForwards(t *testing.T) {
	labDir := tempLabDir(t)
	writeFixtureImages(t, labDir)

	r := newTestRunner(t, labDir)
	vm := fixtureVM()
	vm.Ports = []int{11434}

	api := vmtest.NewFakeFirecracker(r.SocketPath(vm.ID))
	defer api.Close()
	r.Supervisor = &vmtest.FakeSupervisor{API: api}
	r.Net = &vmtest.NoopNetworkProvisioner{}
	fwd := &vmtest.FakePortForwarder{}
	r.Forwarder = fwd

	if err := r.Run(vm, filepath.Join(labDir, "rootfs.ext4"), false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	killTestProcess(t, r, vm)

	if !fwd.IsAlive(vm.ID, 11434) {
		t.Errorf("port forward for %s:11434 not launched", vm.ID)
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
	r.Supervisor = &vmtest.FakeSupervisor{API: api}
	r.Net = &vmtest.NoopNetworkProvisioner{}

	err := r.Run(vm, filepath.Join(labDir, "rootfs.ext4"), false)
	if err == nil {
		t.Fatal("Run: expected error, got nil")
	}
	killTestProcess(t, r, vm)

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

	// Destroy's Stop against a unit that was never Launch'd (no
	// vmtest.FakeSupervisor set here) is a harmless no-op, mirroring
	// systemctl stop on an unknown unit.

	api := vmtest.NewFakeFirecracker(r.SocketPath(vm.ID))
	defer api.Close()
	if err := api.Start(); err != nil {
		t.Fatalf("api.Start: %v", err)
	}
	noop := &vmtest.NoopNetworkProvisioner{}
	r.Net = noop
	r.Supervisor = &vmtest.FakeSupervisor{API: api}

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

func TestDestroyStopsPortForwards(t *testing.T) {
	labDir := tempLabDir(t)
	r := newTestRunner(t, labDir)
	vm := fixtureVM()
	vm.Ports = []int{11434}

	api := vmtest.NewFakeFirecracker(r.SocketPath(vm.ID))
	defer api.Close()
	if err := api.Start(); err != nil {
		t.Fatalf("api.Start: %v", err)
	}
	r.Net = &vmtest.NoopNetworkProvisioner{}
	r.Supervisor = &vmtest.FakeSupervisor{API: api}
	fwd := &vmtest.FakePortForwarder{}
	r.Forwarder = fwd
	if err := fwd.Launch(vm, 11434); err != nil {
		t.Fatalf("seed Launch: %v", err)
	}

	if err := r.Destroy(vm); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if fwd.IsAlive(vm.ID, 11434) {
		t.Errorf("port forward for %s:11434 still alive after Destroy", vm.ID)
	}
}

// TestDestroyStopsSupervisedProcess guards against a regression where
// Destroy fails to stop the actual process Launch started for a VM's unit
// (e.g. mixing up units across VMs, or no-oping instead of really
// stopping it).
func TestDestroyStopsSupervisedProcess(t *testing.T) {
	labDir := tempLabDir(t)
	writeFixtureImages(t, labDir)

	r := newTestRunner(t, labDir)
	vm := fixtureVM()

	api := vmtest.NewFakeFirecracker(r.SocketPath(vm.ID))
	defer api.Close()
	launcher := &vmtest.FakeSupervisor{API: api}
	r.Supervisor = launcher
	r.Net = &vmtest.NoopNetworkProvisioner{}

	if err := r.Run(vm, filepath.Join(labDir, "rootfs.ext4"), false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	killTestProcess(t, r, vm) // best-effort backstop if the assertions below fail early

	cmd := launcher.LaunchedProcess(vm)
	if cmd == nil {
		t.Fatal("no process launched")
	}

	if err := r.Destroy(vm); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	// Wait confirms the process actually terminated (and reaps it, so it
	// doesn't linger as a zombie): a "sleep 300" that exits after Destroy,
	// long before its own timeout, must have been killed.
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		if err == nil {
			t.Error("sleep process exited cleanly; expected it to be killed")
		}
	case <-time.After(2 * time.Second):
		t.Error("process did not exit after Destroy")
	}
}

// seedSnapshottableVM creates the on-disk state Snapshot expects to find for
// vm: a chroot root/ dir containing rootfs.ext4, snapshot.bin, and mem.bin
// (standing in for what a live Run + real firecracker's /snapshot/create
// would have produced). Snapshot's Stop against a unit that was never
// Launch'd (no vmtest.FakeSupervisor tracking it) is a harmless no-op, so
// no process needs to be seeded here.
func seedSnapshottableVM(t *testing.T, r *Runner, vm *state.VM) {
	t.Helper()
	root := filepath.Join(r.vmDir(vm.ID), "root")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatalf("MkdirAll root: %v", err)
	}
	for name, content := range map[string]string{
		"rootfs.ext4":  "rootfs-data",
		"snapshot.bin": "snapshot-data",
		"mem.bin":      "mem-data",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatalf("write %s fixture: %v", name, err)
		}
	}
}

func TestSnapshotHappyPath(t *testing.T) {
	labDir := tempLabDir(t)

	r := newTestRunner(t, labDir)
	vm := fixtureVM()
	seedSnapshottableVM(t, r, vm)

	api := vmtest.NewFakeFirecracker(r.SocketPath(vm.ID))
	defer api.Close()
	if err := api.Start(); err != nil {
		t.Fatalf("api.Start: %v", err)
	}
	noop := &vmtest.NoopNetworkProvisioner{}
	r.Net = noop
	r.Supervisor = &vmtest.FakeSupervisor{API: api}

	snapDir := filepath.Join(labDir, "snapshots", "mysnap")
	if err := r.Snapshot(vm, snapDir); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	reqs := api.Requests()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2: %+v", len(reqs), reqs)
	}
	if reqs[0].Method != "PATCH" || reqs[0].Path != "/vm" {
		t.Errorf("request = %s %s, want PATCH /vm", reqs[0].Method, reqs[0].Path)
	}
	if reqs[0].Body["state"] != "Paused" {
		t.Errorf("state = %v, want Paused", reqs[0].Body["state"])
	}
	if reqs[1].Method != "PUT" || reqs[1].Path != "/snapshot/create" {
		t.Errorf("request = %s %s, want PUT /snapshot/create", reqs[1].Method, reqs[1].Path)
	}
	if reqs[1].Body["snapshot_type"] != "Full" {
		t.Errorf("snapshot_type = %v, want Full", reqs[1].Body["snapshot_type"])
	}
	if reqs[1].Body["snapshot_path"] != "/snapshot.bin" {
		t.Errorf("snapshot_path = %v, want /snapshot.bin", reqs[1].Body["snapshot_path"])
	}
	if reqs[1].Body["mem_file_path"] != "/mem.bin" {
		t.Errorf("mem_file_path = %v, want /mem.bin", reqs[1].Body["mem_file_path"])
	}

	for name, want := range map[string]string{
		"snapshot.bin": "snapshot-data",
		"mem.bin":      "mem-data",
		"rootfs.ext4":  "rootfs-data",
	} {
		got, err := os.ReadFile(filepath.Join(snapDir, name))
		if err != nil {
			t.Fatalf("read snapshot dir %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s content = %q, want %q", name, got, want)
		}
	}

	if _, err := os.Stat(r.vmDir(vm.ID)); !os.IsNotExist(err) {
		t.Errorf("vmDir still exists after Snapshot: err = %v", err)
	}

	if len(noop.Calls) != 1 || noop.Calls[0] != "teardown:"+vm.Tap {
		t.Errorf("network calls = %v, want [teardown:%s]", noop.Calls, vm.Tap)
	}
}

func TestSnapshotErrorPathStopsAtFailure(t *testing.T) {
	labDir := tempLabDir(t)

	r := newTestRunner(t, labDir)
	vm := fixtureVM()
	seedSnapshottableVM(t, r, vm)

	api := vmtest.NewFakeFirecracker(r.SocketPath(vm.ID))
	defer api.Close()
	if err := api.Start(); err != nil {
		t.Fatalf("api.Start: %v", err)
	}
	api.FailNext("/snapshot/create", 500)
	noop := &vmtest.NoopNetworkProvisioner{}
	r.Net = noop

	snapDir := filepath.Join(labDir, "snapshots", "mysnap")
	if err := r.Snapshot(vm, snapDir); err == nil {
		t.Fatal("Snapshot: expected error, got nil")
	}

	if _, err := os.Stat(r.vmDir(vm.ID)); err != nil {
		t.Errorf("vmDir should still exist after failed Snapshot: %v", err)
	}
	if len(noop.Calls) != 0 {
		t.Errorf("network calls = %v, want none after failure", noop.Calls)
	}
}

func TestRestoreHappyPath(t *testing.T) {
	labDir := tempLabDir(t)

	snapDir := filepath.Join(labDir, "snapshots", "mysnap")
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		t.Fatalf("MkdirAll snapDir: %v", err)
	}
	fixtures := map[string]string{
		"snapshot.bin": "snapshot-data",
		"mem.bin":      "mem-data",
		"rootfs.ext4":  "rootfs-data",
	}
	for name, content := range fixtures {
		if err := os.WriteFile(filepath.Join(snapDir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}

	r := newTestRunner(t, labDir)
	vm := fixtureVM()

	api := vmtest.NewFakeFirecracker(r.SocketPath(vm.ID))
	defer api.Close()
	launcher := &vmtest.FakeSupervisor{API: api}
	r.Supervisor = launcher
	noop := &vmtest.NoopNetworkProvisioner{}
	r.Net = noop

	if err := r.Restore(vm, snapDir, false); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	killTestProcess(t, r, vm)

	reqs := api.Requests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1: %+v", len(reqs), reqs)
	}
	if reqs[0].Method != "PUT" || reqs[0].Path != "/snapshot/load" {
		t.Fatalf("request = %s %s, want PUT /snapshot/load", reqs[0].Method, reqs[0].Path)
	}
	if reqs[0].Body["snapshot_path"] != "/snapshot.bin" {
		t.Errorf("snapshot_path = %v, want /snapshot.bin", reqs[0].Body["snapshot_path"])
	}
	memBackend, _ := reqs[0].Body["mem_backend"].(map[string]any)
	if memBackend["backend_type"] != "File" || memBackend["backend_path"] != "/mem.bin" {
		t.Errorf("mem_backend = %v, want {backend_type: File, backend_path: /mem.bin}", memBackend)
	}
	if resume, _ := reqs[0].Body["resume_vm"].(bool); !resume {
		t.Errorf("resume_vm = %v, want true", reqs[0].Body["resume_vm"])
	}
	overrides, _ := reqs[0].Body["network_overrides"].([]any)
	if len(overrides) != 1 {
		t.Fatalf("network_overrides = %v, want 1 entry", overrides)
	}
	override, _ := overrides[0].(map[string]any)
	if override["iface_id"] != "eth0" || override["host_dev_name"] != vm.Tap {
		t.Errorf("network_overrides[0] = %v, want {iface_id: eth0, host_dev_name: %s}", override, vm.Tap)
	}

	root := filepath.Join(r.vmDir(vm.ID), "root")
	for name, want := range fixtures {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read chroot %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s content = %q, want %q", name, got, want)
		}
	}

	if len(noop.Calls) != 1 || noop.Calls[0] != "setup:"+vm.Tap {
		t.Errorf("network calls = %v, want [setup:%s]", noop.Calls, vm.Tap)
	}

	if launcher.LaunchedProcess(vm) == nil {
		t.Error("no process launched")
	}
}

func TestRestoreLaunchesPortForwards(t *testing.T) {
	labDir := tempLabDir(t)

	snapDir := filepath.Join(labDir, "snapshots", "mysnap")
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		t.Fatalf("MkdirAll snapDir: %v", err)
	}
	for _, name := range []string{"snapshot.bin", "mem.bin", "rootfs.ext4"} {
		if err := os.WriteFile(filepath.Join(snapDir, name), []byte(name), 0644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}

	r := newTestRunner(t, labDir)
	vm := fixtureVM()
	vm.Ports = []int{11434}

	api := vmtest.NewFakeFirecracker(r.SocketPath(vm.ID))
	defer api.Close()
	r.Supervisor = &vmtest.FakeSupervisor{API: api}
	r.Net = &vmtest.NoopNetworkProvisioner{}
	fwd := &vmtest.FakePortForwarder{}
	r.Forwarder = fwd

	if err := r.Restore(vm, snapDir, false); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	killTestProcess(t, r, vm)

	if !fwd.IsAlive(vm.ID, 11434) {
		t.Errorf("port forward for %s:11434 not launched", vm.ID)
	}
}

func TestRestoreErrorPathStopsAtFailure(t *testing.T) {
	labDir := tempLabDir(t)

	snapDir := filepath.Join(labDir, "snapshots", "mysnap")
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		t.Fatalf("MkdirAll snapDir: %v", err)
	}
	for _, name := range []string{"snapshot.bin", "mem.bin", "rootfs.ext4"} {
		if err := os.WriteFile(filepath.Join(snapDir, name), []byte(name), 0644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}

	r := newTestRunner(t, labDir)
	vm := fixtureVM()

	api := vmtest.NewFakeFirecracker(r.SocketPath(vm.ID))
	defer api.Close()
	api.FailNext("/snapshot/load", 500)
	r.Supervisor = &vmtest.FakeSupervisor{API: api}
	r.Net = &vmtest.NoopNetworkProvisioner{}

	if err := r.Restore(vm, snapDir, false); err == nil {
		t.Fatal("Restore: expected error, got nil")
	}
	killTestProcess(t, r, vm)
}

func TestSetupChroot(t *testing.T) {
	labDir := t.TempDir()
	writeFixtureImages(t, labDir)

	r := newTestRunner(t, labDir)
	vm := fixtureVM()

	if err := r.setupChroot(vm, filepath.Join(labDir, "rootfs.ext4")); err != nil {
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
