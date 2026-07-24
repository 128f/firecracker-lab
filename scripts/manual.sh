#
# this is an old script that illustrates manual steps
# these were converted into a CLI tool
echo "adding kernel image"
curl -X PUT 'http://localhost/boot-source' \
  --unix-socket /tmp/firecracker.socket \
  -H 'Content-Type: application/json' \
  -d '{
    "kernel_image_path": "/home/cburke/firecracker-lab/vmlinux.bin",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off"
  }'

sleep 2

echo "adding rootfs"
curl -X PUT 'http://localhost/drives/rootfs' \
  --unix-socket /tmp/firecracker.socket \
  -H 'Content-Type: application/json' \
  -d '{
    "drive_id": "rootfs",
    "path_on_host": "/home/cburke/firecracker-lab/rootfs.ext4",
    "is_root_device": true,
    "is_read_only": false
  }'


sleep 2

echo "configuring vm specs"
curl -X PUT 'http://localhost/machine-config' \
  --unix-socket /tmp/firecracker.socket \
  -H 'Content-Type: application/json' \
  -d '{
    "vcpu_count": 2,
    "mem_size_mib": 1024
  }'

sleep 2

echo "attaching tap device"
curl -X PUT 'http://localhost/network-interfaces/eth0' \
  --unix-socket /tmp/firecracker.socket \
  -H 'Content-Type: application/json' \
  -d '{
    "iface_id": "eth0",
    "guest_mac": "AA:FC:00:00:00:01",
    "host_dev_name": "tap0"
  }'

sleep 2

echo "booting..."
curl -X PUT 'http://localhost/actions' \
  --unix-socket /tmp/firecracker.socket \
  -H 'Content-Type: application/json' \
  -d '{"action_type": "InstanceStart"}'
