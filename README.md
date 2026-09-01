# Firecracker Lab

A CLI for managing jailed Firecracker microVMs, and a rust-based guest agent.

The guest agent runs as PID 1 inside each VM and speaks two protocols over
vsock: a raw PTY stream (interactive shell) and a length-prefixed protobuf
control channel (`agentpb`) for status/heartbeat, restore notifications, and
guest-side TCP↔vsock port-forward proxies.

## Prerequisites

- `firecracker` and `jailer` binaries (see `just deps` in this directory)
- `vmlinux.bin` kernel
- `mkfs.ext4` (`e2fsprogs`) — only required for `labctl image build`
- `nft` (nftables) — used by `labctl setup` to configure VM networking/NAT
- `--data-dir` on a **reflink-capable filesystem** (btrfs or xfs) — see
  [Storage](#storage) below
- Run as root

## Environment variables

Every path-ish flag can be set via an env var instead, so a single
`export` at the top of your shell keeps every subcommand pointed at the
same place — no need to retype `--data-dir`/`--firecracker`/etc. on every
invocation (a flag always overrides its env var if both are set). These are
persistent flags on the root command, so they work before or after the
subcommand name:

| Flag             | Env var                  | Default        |
|------------------|---------------------------|----------------|
| `--data-dir`     | `LABCTL_DATA_DIR`           | `/var/lib/labctl`|
| `--source-dir`   | `LABCTL_SOURCE_DIR`         | `.`            |
| `--firecracker`  | `LABCTL_FIRECRACKER_BIN`    | `firecracker`  |
| `--jailer`       | `LABCTL_JAILER_BIN`         | `jailer`  (per-command flag on `run`/`restore`) |

Example:
```bash
export LABCTL_DATA_DIR=/mnt/xfs
export LABCTL_SOURCE_DIR=/mnt/xfs
export LABCTL_FIRECRACKER_BIN=./release-v1.14.3-x86_64/firecracker-v1.14.3-x86_64
export LABCTL_JAILER_BIN=./release-v1.14.3-x86_64/jailer-v1.14.3-x86_64

sudo -E ./labctl setup
sudo -E ./labctl image import ./rootfs.ext4 --name base
sudo -E ./labctl run --image base --count 3
sudo -E ./labctl console vm0
```
Note `sudo -E` — `sudo` strips the environment by default, so without
`-E` these exports won't reach the command.

## One-time host setup

Creates the bridge, cgroup parent, jailer dirs, vm user, data dir, and VM
networking/NAT rules. Run once per boot:

```bash
sudo ./labctl setup
```

Flags:
- `--uid`/`--gid` — uid/gid for the `labctl-vm` system user jailer runs
  VMs as (default `123`/`123`)
- `--wan` — WAN interface for NAT (auto-detected from the default route
  if empty)

This creates:
- `br0` at `172.16.0.1/24` — all VM taps attach here
- `/sys/fs/cgroup/labctl/` — parent cgroup for the VM pool
- `/srv/jailer/firecracker/` — jailer symlink target dir
- `labctl-vm` system user/group (uid/gid `123` by default) that jailer
  runs VM processes as
- `--data-dir` (default `/var/lib/labctl`) — owned by the jailer vm user, holds all runtime state
- An `nftables` ruleset (table `inet labctl`) that lets VMs on `br0` reach
  the internet (NAT via the detected WAN interface) and each other, but
  blocks VMs from reaching the host's LAN or the host itself — see
  [Networking](#networking)

## Commands

### image import / image list

Before running VMs, register at least one base rootfs image:

```bash
sudo ./labctl image import ./rootfs.ext4 --name base
sudo ./labctl image list
```

`image import` copies `<path>` into `<data-dir>/images/<name>.ext4` (a
regular copy — this happens once per image, not once per VM) and records
it in the state DB. Fails if `--name` is already registered; pick a new
name or remove the old one from the DB first. The file is sanity-checked
(size + ext2/3/4 superblock magic) but not fully validated — this is a
single-operator tool, not a multi-tenant upload path.

### image build

Build a bootable ext4 rootfs directly from an OCI/Docker image reference
(e.g. `docker.io/library/ubuntu:24.04`), without needing a pre-built
`.ext4` file:

```bash
./labctl image build ubuntu:24.04 \
  --guest-agent-binary ./guest-agent-bin \
  -o ubuntu-24.04.ext4
sudo ./labctl image import ubuntu-24.04.ext4 --name ubuntu
```

Flags:
- `--guest-agent-binary path` — **required**. Path to a pre-built
  linux `guest-agent` binary. This command does not compile it — see
  [`guest-agent/build.sh`](guest-agent/build.sh) or `just build-guest-agent`.
- `-o, --output path` — **required**. Output `.ext4` file path.
- `--platform linux/amd64` — target platform to pull (this repo assumes
  x86_64 throughout; changing this is unsupported)
- `--init-path /bin/guest-agent` — where inside the rootfs to install the
  guest agent. Must match `vm/vm.go`'s hardcoded `init=` boot arg — do
  not change unless you also update `vm/vm.go`.
- `--size 2048M` — ext4 filesystem size
- `--local` — load `<ref>` from the local Docker daemon instead of
  pulling from a remote registry

**Requires `mkfs.ext4`** (`e2fsprogs`) on PATH — the same way the rest of
`labctl` requires `firecracker`/`jailer`/`ip` on a real Linux host. Pulling
and flattening the image itself is pure Go (via `crane`) and works
anywhere; only the final packing step needs a Linux host with `e2fsprogs`.

This command flattens the image's layers (`docker export` semantics, OCI
whiteouts resolved) into a single rootfs, writes the image's Entrypoint /
Cmd / Env / WorkingDir / User into `/etc/labctl/image-config.json` inside
the rootfs, and installs the guest agent as `/bin/guest-agent`. **This is
currently the only way workload/entrypoint information reaches the guest
agent** — images imported via plain `labctl image import` of a hand-built
`.ext4` (e.g. from `just rootfs-ext4` or `guest-agent/build.sh`) have no
`image-config.json` and the guest agent has nothing to read.

It does **not** register the output into the state DB — run `labctl image
import` afterward, same as any other `.ext4` file.

### image upgrade

Inject a newly built `guest-agent` binary into an already-registered
image in place, without rebuilding the whole rootfs:

```bash
sudo ./labctl image upgrade <name> --guest-agent-binary ./guest-agent-bin
```

Mounts the registered image's `.ext4` file as a loopback device, replaces
`/bin/guest-agent` (or `--init-path`, if customized), and unmounts. Useful
after a guest-agent code change when you don't want to re-pull/re-flatten
the base OCI image.

### run

```bash
sudo ./labctl run [flags]
```

Flags:
- `--vcpus 1` — vCPU count per VM
- `--mem 256` — memory in MiB per VM
- `--count 1` — number of VMs to create
- `--image name` — registered image to boot (default: the only registered image, if there's exactly one; required otherwise)
- `--uid`/`--gid` — uid/gid for the jailer vm user (default `123`/`123`, set up by `labctl setup`)
- `-a, --attach-console` — run the VM in the foreground, attached to its console (default: detached, runs in the background under `systemd-run`)
- `--jailer path` — path to jailer binary (default: `jailer` on $PATH, or `$LABCTL_JAILER_BIN`)

`--data-dir`, `--source-dir`, and `--firecracker` are root-level persistent
flags (see [Environment variables](#environment-variables)) and apply here too.

Example:
```bash
sudo ./labctl run \
  --jailer ./release-v1.14.3-x86_64/jailer-v1.14.3-x86_64 \
  --firecracker ./release-v1.14.3-x86_64/firecracker-v1.14.3-x86_64 \
  --image base --vcpus 1 --mem 256 --count 5
```

For each VM, the run command will:
1. Resolves `--image` to a registered image (path + id) via the state DB
2. Allocates ID, tap name, IP, vsock CID via the state DB (`<data-dir>/labctl.db`)
3. Creates `<data-dir>/vms/<id>/root/` chroot directory
4. Hard-links `vmlinux.bin` (from `--source-dir`) into the chroot (no duplication)
5. Reflink-copies the image into the chroot as `rootfs.ext4` (fails loudly if the data dir isn't reflink-capable — see [Storage](#storage))
6. Symlinks `/srv/jailer/firecracker/<id>` → `<data-dir>/vms/<id>`
7. Creates tap device, attaches to `br0`
8. Launches jailer (via `systemd-run`, unless `--attach-console`), which exec's firecracker inside chroot + cgroups
9. Calls Firecracker API: kernel, rootfs, network, machine-config, start
10. Writes allocation to the state DB

### destroy

```bash
sudo ./labctl destroy <id>
```

Halts the VM, removes tap, deletes chroot dir and jailer symlink, removes from the state DB.

### list

```bash
sudo ./labctl list
```

Lists all VMs with tap, IP, CID, and status. Status is "stopped" if the
systemd unit isn't running, otherwise labctl queries the guest agent over
vsock for its health and shows `running (healthy)`, `running (degraded)`,
or `running` / `unknown (agent unreachable)` if the guest agent inside
hasn't come up yet or can't be reached.

### console

```bash
sudo ./labctl console <id>
```

Attaches an interactive shell to a VM: connects to the guest agent's PTY
vsock listener (port `1235`), which spawns a fresh shell process per
connection and pipes raw bytes to/from it — no framing, just a terminal.
Puts the local terminal into raw mode for the duration of the session;
press `ctrl+]` to detach (detaching kills that shell process; the VM
keeps running).

`run --attach-console` / `restore --attach-console` boot a VM already
attached to this same console.

### vsock

```bash
sudo ./labctl vsock <id> [--port 1234]
```

Connects to an arbitrary guest vsock listener on the given port (default
`1234`), performing the Firecracker UDS-backend `CONNECT` handshake and
then piping the terminal to/from the connection. Raw passthrough like
`console`, but for any vsock service the guest exposes rather than the
agent's dedicated PTY port. Puts the local terminal into raw mode; press
`ctrl+]` to detach.

### ports

```bash
sudo ./labctl ports add <vm-id> <port>
sudo ./labctl ports rm <vm-id> <port>
sudo ./labctl ports list <vm-id>
sudo ./labctl ports reload <vm-id>
```

Forwards a guest-initiated vsock connection to the same-numbered host TCP
port, so a service listening inside the VM becomes reachable at
`127.0.0.1:<port>` on the host. Two halves work together per port:

- Host side: a `socat` systemd unit that accepts on the vsock CID/port and
  bridges to the Firecracker UDS vsock backend (`ports add`/`rm` manage
  this unit; `ports list` shows whether it's active; `ports reload`
  restarts it).
- Guest side: `ports add`/`rm` tell the guest agent (over the `agentpb`
  control channel) to start/stop a `StartTcpVsockProxy`/`StopTcpVsockProxy`
  proxy that accepts the forwarded TCP port inside the guest and bridges it
  to vsock.

Forwarded ports are persisted per-VM in the state DB, so `snapshot`/
`restore` carry them across, and `ports reload` re-establishes both halves
after a guest-agent restart. Adding a port to a VM that isn't currently
running just records it — forwarding starts on its next `restore`.

### snapshot / restore

```bash
sudo ./labctl snapshot <vm-id> --name <name>
sudo ./labctl restore <name>
sudo ./labctl snapshot list
sudo ./labctl snapshot delete <name>
```

`snapshot` pauses a VM, saves a full Firecracker snapshot (memory + device
state) to `<data-dir>/snapshots/<name>/`, records it (along with vCPU/mem
config and forwarded ports) in the state DB, and tears the VM down —
freeing its ID/tap/IP/CID.

`restore` allocates a fresh VM (new ID/tap/IP/CID) and boots it from a
saved snapshot instead of a base image, re-establishing its port forwards.
Accepts the same `--uid`/`--gid`/`-a, --attach-console`/`--jailer` flags as
`run`.

## State

All runtime state lives under `--data-dir` (default `/var/lib/labctl`, or
`$LABCTL_DATA_DIR`), independent of wherever the `labctl` binary itself lives:

- `<data-dir>/labctl.db` — allocation ledger (ID, tap, IP, CID, vcpus, mem, unit, forwarded ports), plus the image and snapshot registries. Survives reboots. Written by `run`/`destroy`/`image`/`snapshot`/`restore`/`ports`.
- `<data-dir>/vms/` — ephemeral runtime state. Wiped on reboot. Managed entirely by labctl.
- `<data-dir>/snapshots/` — saved VM snapshots (memory + device state), one dir per `snapshot --name`.

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

`labctl setup` installs an nftables ruleset (table `inet labctl`) so VMs
get internet access without manual configuration: traffic from `br0` is
masqueraded out the detected (or `--wan`-specified) WAN interface, VM-to-VM
traffic on `br0` is allowed, but VMs are blocked from reaching the host's
own LAN subnet or the host itself. Re-run `labctl setup` if the WAN
interface or its address changes.

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
Expect `btrfs` or `xfs`. On ext4 or anything else, `labctl run` will fail at
the reflink-copy step.

**Known limitation:** this ties the fast path to host filesystem choice.
The filesystem-independent fix is a device-mapper thin-provisioning pool
(instant CoW clones regardless of host filesystem) — that's the
acknowledged next step, not something implemented here.

## Resource sharing

- Kernel (`vmlinux.bin`) — one copy on disk, hard-linked into each chroot
- Per-VM rootfs (`rootfs.ext4`) — reflink-cloned per VM from the registered base image (see [Storage](#storage))
- Cgroup parent (`/sys/fs/cgroup/labctl/`) — set pool-wide limits here; jailer creates per-VM leaf cgroups beneath it

## Recovery

If VMs are killed without `labctl destroy` (e.g. reboot):
```bash
rm -rf <data-dir>/vms/
# edit <data-dir>/labctl.db to remove stale entries, or:
rm <data-dir>/labctl.db
```
