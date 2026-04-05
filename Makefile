FIRECRACKER_VERSION := v1.14.3
ARCH := x86_64
FIRECRACKER_TGZ := firecracker-$(FIRECRACKER_VERSION)-$(ARCH).tgz
FIRECRACKER_RELEASE_DIR := release-$(FIRECRACKER_VERSION)-$(ARCH)

FIRECRACKER_URL := https://github.com/firecracker-microvm/firecracker/releases/download/$(FIRECRACKER_VERSION)/$(FIRECRACKER_TGZ)
VMLINUX_URL := https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.14/$(ARCH)/vmlinux-6.1.155
ROOTFS_URL := https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.14/$(ARCH)/ubuntu-24.04.squashfs
ROOTFS_SIZE := 512M

.PHONY: deps fctl clean

deps: $(FIRECRACKER_RELEASE_DIR) vmlinux.bin rootfs.ext4

fctl:
	cd fctl && go build -o ../fctl .

$(FIRECRACKER_TGZ):
	curl -Lo $@ $(FIRECRACKER_URL)

$(FIRECRACKER_RELEASE_DIR): $(FIRECRACKER_TGZ)
	tar -xzf $<

vmlinux.bin:
	curl -Lo $@ $(VMLINUX_URL)

ubuntu-24.04.squashfs:
	curl -Lo $@ $(ROOTFS_URL)

rootfs.ext4: ubuntu-24.04.squashfs
	unsquashfs -d .rootfs-squash $<
	truncate -s $(ROOTFS_SIZE) $@
	mke2fs -t ext4 -d .rootfs-squash $@
	rm -rf .rootfs-squash

clean:
	rm -f $(FIRECRACKER_TGZ) vmlinux.bin ubuntu-24.04.squashfs rootfs.ext4
	rm -rf $(FIRECRACKER_RELEASE_DIR) .rootfs-squash
