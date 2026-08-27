firecracker_version := "v1.14.3"
arch := "x86_64"
firecracker_tgz := "firecracker-" + firecracker_version + "-" + arch + ".tgz"
firecracker_release_dir := "release-" + firecracker_version + "-" + arch

firecracker_url := "https://github.com/firecracker-microvm/firecracker/releases/download/" + firecracker_version + "/" + firecracker_tgz
vmlinux_url := "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.14/" + arch + "/vmlinux-6.1.155"
rootfs_url := "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.14/" + arch + "/ubuntu-24.04.squashfs"
rootfs_size := "512M"

deps: firecracker-release vmlinux-bin rootfs-ext4

build:
    go build -o labctl .

[private]
firecracker-tgz:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ ! -f "{{firecracker_tgz}}" ]; then
        curl -Lo "{{firecracker_tgz}}" "{{firecracker_url}}"
    fi

[private]
firecracker-release: firecracker-tgz
    #!/usr/bin/env bash
    set -euo pipefail
    if [ ! -d "{{firecracker_release_dir}}" ]; then
        tar -xzf "{{firecracker_tgz}}"
    fi

vmlinux-bin:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ ! -f vmlinux.bin ]; then
        curl -Lo vmlinux.bin "{{vmlinux_url}}"
    fi

[private]
ubuntu-squashfs:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ ! -f ubuntu-24.04.squashfs ]; then
        curl -Lo ubuntu-24.04.squashfs "{{rootfs_url}}"
    fi

rootfs-ext4: ubuntu-squashfs
    #!/usr/bin/env bash
    set -euo pipefail
    if [ ! -f rootfs.ext4 ]; then
        unsquashfs -d .rootfs-squash ubuntu-24.04.squashfs
        truncate -s {{rootfs_size}} rootfs.ext4
        mke2fs -t ext4 -d .rootfs-squash rootfs.ext4
        rm -rf .rootfs-squash
    fi

clean:
    rm -f {{firecracker_tgz}} vmlinux.bin ubuntu-24.04.squashfs rootfs.ext4
    rm -rf {{firecracker_release_dir}} .rootfs-squash
