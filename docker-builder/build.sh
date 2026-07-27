docker build . -t firecracker-rootfs:24.04
CID=$(docker create firecracker-rootfs:24.04)
docker export "$CID" -o rootfs.tar
docker rm "$CID"

mkdir -p rootfs-dir && tar -xf rootfs.tar -C rootfs-dir
mkfs.ext4 -F -d rootfs-dir -b 4096 rootfs.ext4 2048M
