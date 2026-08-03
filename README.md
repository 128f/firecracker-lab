# Firecracker Lab

A CLI for managing jailed Firecracker microVMs, and a rust-based guest agent.

The guest agent is planned to include an api, shell and heartbeat over vsock.

## Prerequisites

- `firecracker` and `jailer` binaries (see `just deps` in this directory)
- `vmlinux.bin` kernel
- `--data-dir` on a **reflink-capable filesystem** (btrfs or xfs) — see
  [Storage](#storage) below
- Run as root

## Environment variables

Every path-ish flag can be set via an env var instead, so a single
`export` at the top of your shell keeps every subcommand pointed at the
same place — no need to retype `--data-dir`/`--firecracker`/etc. on every
invocation (a flag always overrides its env var if both are set):

| Flag             | Env var                  | Default        |
|------------------|---------------------------|----------------|
| `--data-dir`     | `FCTL_DATA_DIR`           | `/var/lib/fctl`|
| `--source-dir`   | `FCTL_SOURCE_DIR`         | `.`            |
| `--firecracker`  | `FCTL_FIRECRACKER_BIN`    | `firecracker`  |
| `--jailer`       | `FCTL_JAILER_BIN`         | `jailer`       |

Example:
```bash
export FCTL_DATA_DIR=/mnt/xfs
export FCTL_SOURCE_DIR=/mnt/xfs
export FCTL_FIRECRACKER_BIN=./release-v1.14.3-x86_64/firecracker-v1.14.3-x86_64
export FCTL_JAILER_BIN=./release-v1.14.3-x86_64/jailer-v1.14.3-x86_64

sudo -E ./fctl setup
sudo -E ./fctl image import ./rootfs.ext4 --name base
sudo -E ./fctl run --image base --count 3
sudo -E ./fctl console vm0
```
Note `sudo -E` — `sudo` strips the environment by default, so without
`-E` these exports won't reach the command.

## One-time host setup

Creates the bridge, cgroup parent, jailer dirs, and data dir. Run once per boot:

```bash
sudo ./fctl setup
```

This creates:
- `br0` at `172.16.0.1/24` — all VM taps attach here
- `/sys/fs/cgroup/fctl/` — parent cgroup for the VM pool
- `/srv/jailer/firecracker/` — jailer symlink target dir
- `--data-dir` (default `/var/lib/fctl`) — owned by the jailer vm user, holds all runtime state

## Commands

### image import / image list

Before running VMs, register at least one base rootfs image:

```bash
sudo ./fctl image import ./rootfs.ext4 --name base
sudo ./fctl image list
```

`image import` copies `<path>` into `<data-dir>/images/<name>.ext4` (a
regular copy — this happens once per image, not once per VM) and records
it in the state DB. Fails if `--name` is already registered; pick a new
name or remove the old one from the DB first. The file is sanity-checked
(size + ext2/3/4 superblock magic) but not fully validated — this is a
single-operator tool, not a multi-tenant upload path.

### create

```bash
sudo ./fctl run [flags]
```

Flags:
- `--vcpus 1` — vCPU count per VM
- `--mem 256` — memory in MiB per VM
- `--count 1` — number of VMs to run
- `--image name` — registered image to boot (default: the only registered image, if there's exactly one; required otherwise)
- `--jailer path` — path to jailer binary (default: `jailer` on $PATH)
- `--firecracker path` — path to firecracker binary (default: `firecracker` on $PATH)
- `--data-dir path` — directory for VM state, images, and the state DB (default: `/var/lib/fctl`, or `$FCTL_DATA_DIR`)
- `--source-dir path` — directory containing build-time inputs (`vmlinux.bin`) (default: current directory)

Example:
```bash
sudo ./fctl run \
  --jailer ./release-v1.14.3-x86_64/jailer-v1.14.3-x86_64 \
  --firecracker ./release-v1.14.3-x86_64/firecracker-v1.14.3-x86_64 \
  --image base --vcpus 1 --mem 256 --count 5
```

For each VM, the run command will:
1. Resolves `--image` to a registered image (path + id) via the state DB
2. Allocates ID, tap name, IP, vsock CID via the state DB (`<data-dir>/fctl.db`)
3. Creates `<data-dir>/vms/<id>/root/` chroot directory
4. Hard-links `vmlinux.bin` (from `--source-dir`) into the chroot (no duplication)
5. Reflink-copies the image into the chroot as `rootfs.ext4` (fails loudly if the data dir isn't reflink-capable — see [Storage](#storage))
6. Symlinks `/srv/jailer/firecracker/<id>` → `<data-dir>/vms/<id>`
7. Creates tap device, attaches to `br0`
8. Launches jailer (which exec's firecracker inside chroot + cgroups)
9. Calls Firecracker API: kernel, rootfs, network, machine-config, start
10. Writes allocation to the state DB

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

All runtime state lives under `--data-dir` (default `/var/lib/fctl`, or
`$FCTL_DATA_DIR`), independent of wherever the `fctl` binary itself lives:

- `<data-dir>/fctl.db` — allocation ledger (ID, tap, IP, CID, vcpus, mem). Survives reboots. Written by create/destroy.
- `<data-dir>/vms/` — ephemeral runtime state. Wiped on reboot. Managed entirely by fctl.

Each VM's directory:
```
<data-dir>/vms/
  vm0/
    root/
      vmlinux.bin       ← hard link from --source-dir
      rootfs.ext4        ← copy of base rootfs (reflink when possible)
      run/
        firecracker.socket
    firecracker.pid
```

## Networking

All VMs share `br0`. Each gets a tap device (`tap0`, `tap1`, ...) and an IP in `172.16.0.0/24` (spills into subsequent /24s for >253 VMs). Gateway is `172.16.0.1` (the bridge).

NAT/forwarding is not configured by fctl — add iptables rules manually if VMs need internet access.

## Storage

**`--data-dir` must be on a reflink-capable filesystem (btrfs or xfs).**
Each VM's rootfs is created with `cp --reflink=always` from the registered
base image — an instant, near-zero-space copy-on-write clone. If the data
dir isn't reflink-capable, this fails loudly with an explicit error rather
than silently falling back to a full byte-for-byte copy (which, at
hundreds of MB–several GB per image, would make `--count` scale linearly
in both time and disk).

Check before use:
```bash
findmnt -no FSTYPE <data-dir>
# or
stat -f --format=%T <data-dir>
```
Expect `btrfs` or `xfs`. On ext4 or anything else, `fctl run` will fail at
the reflink-copy step.

**Known limitation:** this ties the fast path to host filesystem choice.
The filesystem-independent fix is a device-mapper thin-provisioning pool
(instant CoW clones regardless of host filesystem) — that's the
acknowledged next step, not something implemented here.

## Resource sharing

- Kernel (`vmlinux.bin`) — one copy on disk, hard-linked into each chroot
- Per-VM rootfs (`rootfs.ext4`) — reflink-cloned per VM from the registered base image (see [Storage](#storage))
- Cgroup parent (`/sys/fs/cgroup/fctl/`) — set pool-wide limits here; jailer creates per-VM leaf cgroups beneath it

## Recovery

If VMs are killed without `fctl destroy` (e.g. reboot):
```bash
rm -rf <data-dir>/vms/
# edit <data-dir>/fctl.db to remove stale entries, or:
rm <data-dir>/fctl.db
```
