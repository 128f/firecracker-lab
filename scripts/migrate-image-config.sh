#!/usr/bin/env bash
# Moves etc/fctl/image-config.json to etc/labctl/image-config.json inside an
# ext4 image, for images built before the fctl -> labctl rename. Run this
# alongside `labctl image upgrade` (which swaps the guest-agent binary) --
# the new guest-agent binary reads its config from the labctl path.
#
# Usage: sudo ./scripts/migrate-image-config.sh <path-to-image.ext4>
set -euo pipefail

if [ "$#" -ne 1 ]; then
    echo "usage: $0 <path-to-image.ext4>" >&2
    exit 1
fi

image="$1"
old_path="etc/fctl/image-config.json"
new_path="etc/labctl/image-config.json"

if [ ! -f "$image" ]; then
    echo "no such file: $image" >&2
    exit 1
fi

mount_dir="$(mktemp -d)"
cleanup() {
    umount "$mount_dir" 2>/dev/null || true
    rmdir "$mount_dir" 2>/dev/null || true
}
trap cleanup EXIT

mount -o loop "$image" "$mount_dir"

if [ ! -f "$mount_dir/$old_path" ]; then
    echo "$old_path not found in $image, nothing to do" >&2
    exit 1
fi

if [ -f "$mount_dir/$new_path" ]; then
    echo "$new_path already exists in $image, nothing to do" >&2
    exit 1
fi

mkdir -p "$mount_dir/$(dirname "$new_path")"
mv "$mount_dir/$old_path" "$mount_dir/$new_path"
rmdir "$mount_dir/$(dirname "$old_path")" 2>/dev/null || true

echo "moved $old_path -> $new_path in $image"
