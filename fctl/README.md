# fctl

CLI for managing jailed Firecracker microVMs.

## Prerequisites

- `firecracker` and `jailer` binaries (see parent Makefile: `make deps`)
- `vmlinux.bin` kernel
- `rootfs.ext4` base image
- `qemu-img` on PATH
- Run as root

## One-time host setup

Creates the bridge, cgroup parent, and jailer dirs. Run once per boot:

```bash
sudo ./fctl setup
```

This creates:
- `br0` at `172.16.0.1/24` — all VM taps attach here
- `/sys/fs/cgroup/fctl/` — parent cgroup for the VM pool
- `/srv/jailer/firecracker/` — jailer symlink target dir

## Commands

### create

```bash
sudo ./fctl create [flags]
```

Flags:
- `--vcpus 1` — vCPU count per VM
- `--mem 256` — memory in MiB per VM
- `--count 1` — number of VMs to create
- `--jailer path` — path to jailer binary (default: `jailer` on $PATH)
- `--firecracker path` — path to firecracker binary (default: `firecracker` on $PATH)

Example:
```bash
sudo ./fctl create \
  --jailer ./release-v1.14.3-x86_64/jailer-v1.14.3-x86_64 \
  --firecracker ./release-v1.14.3-x86_64/firecracker-v1.14.3-x86_64 \
  --vcpus 1 --mem 256 --count 5
```

For each VM, create:
1. Allocates ID, tap name, IP, vsock CID from state.json
2. Creates `vms/<id>/root/` chroot directory
3. Hard-links `vmlinux.bin` into the chroot (no duplication)
4. Creates `rootfs.qcow2` CoW overlay backed by shared `rootfs.ext4`
5. Symlinks `/srv/jailer/firecracker/<id>` → `vms/<id>`
6. Creates tap device, attaches to `br0`
7. Launches jailer (which exec's firecracker inside chroot + cgroups)
8. Calls Firecracker API: kernel, rootfs, network, machine-config, start
9. Writes allocation to state.json

### destroy

```bash
sudo ./fctl destroy <id>
```

Halts the VM, removes tap, deletes chroot dir and jailer symlink, removes from state.json.

### list

```bash
sudo ./fctl list
```

Lists all VMs with tap, IP, CID, and live/stopped status (checked via `/proc/<pid>`).

### status

```bash
sudo ./fctl list  # per-VM status shown inline
```

## State

- `state.json` — allocation ledger (ID, tap, IP, CID, vcpus, mem). Survives reboots. Written by create/destroy.
- `vms/` — ephemeral runtime state. Wiped on reboot. Managed entirely by fctl.

Each VM's directory:
```
vms/
  vm0/
    root/
      vmlinux.bin       ← hard link to lab root
      rootfs.qcow2      ← CoW overlay backed by rootfs.ext4
      run/
        firecracker.socket
    firecracker.pid
```

## Networking

All VMs share `br0`. Each gets a tap device (`tap0`, `tap1`, ...) and an IP in `172.16.0.0/24` (spills into subsequent /24s for >253 VMs). Gateway is `172.16.0.1` (the bridge).

NAT/forwarding is not configured by fctl — add iptables rules manually if VMs need internet access.

## Resource sharing

- Kernel (`vmlinux.bin`) — one copy on disk, hard-linked into each chroot
- Base rootfs (`rootfs.ext4`) — read-only, shared across all VMs as qcow2 backing file
- Per-VM rootfs (`rootfs.qcow2`) — only diverging writes stored
- Cgroup parent (`/sys/fs/cgroup/fctl/`) — set pool-wide limits here; jailer creates per-VM leaf cgroups beneath it

## Recovery

If VMs are killed without `fctl destroy` (e.g. reboot):
```bash
rm -rf ../vms/
# edit state.json to remove stale entries, or:
rm ../state.json
```
