#!/usr/bin/env bash
set -euo pipefail

# Run go9p-netfs kernel-client e2e using the v9fs/test methodology.
# This script is meant to run inside ghcr.io/v9fs/docker:v2.0.0.

VMLINUX_TAG="${V9FS_TEST_KERNEL_TAG:-kernel-main}"
KERNEL_IMAGE="${KERNEL_IMAGE:-/opt/v9fs/Image}"
INITRD="${INITRD:-/opt/v9fs/initrd-netfs.cpio}"
QEMULOG="${QEMULOG:-/opt/v9fs/qemu.log}"
PIDFILE="${PIDFILE:-/opt/v9fs/qemu.pid}"

NETFS_PORT="${NETFS_PORT:-564}"
ECHO_PORT="${ECHO_PORT:-7777}"

echo "[host] fetching kernel Image (${VMLINUX_TAG})"
curl -fsSL "https://github.com/v9fs/test/releases/download/${VMLINUX_TAG}/Image" -o "${KERNEL_IMAGE}"

echo "[host] building netfs + guest binaries"
# Keep all build artifacts outside the bind-mounted repo. The workspace is often
# mounted read-only (or non-root-unwritable) inside v9fs/docker, and -o
# /opt/v9fs/go9p-netfs targets a *directory* which makes Go emit
# .../go9p-netfs/go9p-netfs and fail with permission denied.
BIN_DIR="${BIN_DIR:-/opt/v9fs/_bin}"
mkdir -p "${BIN_DIR}"
cd /opt/v9fs/go9p-netfs
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -buildvcs=false -o "${BIN_DIR}/go9p-netfs" ./cmd/go9p-netfs
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -buildvcs=false -o "${BIN_DIR}/kernel9p-netfs-e2e" ./cmd/kernel9p-netfs-e2e
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -buildvcs=false -o "${BIN_DIR}/host-tcp-echo" ./cmd/host-tcp-echo

echo "[host] building u-root initrd (uinitcmd: mount hostshare -> chroot -> guest-e2e-netfs.sh)"
UROOTVERS="${UROOTVERS:-v0.16.0}"
UROOT_DIR="$(GO111MODULE=on go list -f '{{.Dir}}' -m github.com/u-root/u-root@${UROOTVERS})"
mkdir -p /opt/v9fs/uimage-netfs
cd /opt/v9fs/uimage-netfs
rm -f go.work go.work.sum "${INITRD}" || true
go work init "${UROOT_DIR}"

GOWORK=/opt/v9fs/uimage-netfs/go.work /opt/v9fs/go/bin/u-root \
  -o "${INITRD}" \
  -files /opt/v9fs/go9p-netfs/scripts/v9fs/guest-e2e-netfs.sh:guest-e2e-netfs.sh \
  -initcmd=/bbin/init \
  -uinitcmd="/bbin/gosh -c \"mkdir -p /mnt/9; mount -t 9p -o trans=virtio,version=9p2000.L,msize=262144 hostshare /mnt/9; KERNEL9P_TCP_ADDR=10.0.2.2 KERNEL9P_TCP_PORT=${NETFS_PORT} NETFS_ECHO_PORT=${ECHO_PORT} chroot /mnt/9 /bin/bash /opt/v9fs/go9p-netfs/scripts/v9fs/guest-e2e-netfs.sh; shutdown -h now\"" \
  github.com/u-root/u-root/cmds/core/{init,gosh,mount,chroot,shutdown,poweroff,mkdir}

echo "[host] starting tcp echo server on 127.0.0.1:${ECHO_PORT}"
ECHO_ADDR="127.0.0.1:${ECHO_PORT}" "${BIN_DIR}/host-tcp-echo" >/dev/null 2>&1 &
ECHO_PID=$!
trap 'kill ${ECHO_PID} >/dev/null 2>&1 || true' EXIT

echo "[host] starting go9p-netfs server on :${NETFS_PORT}"
"${BIN_DIR}/go9p-netfs" -addr "0.0.0.0:${NETFS_PORT}" >/dev/null 2>&1 &
NETFS_PID=$!
trap 'kill ${NETFS_PID} >/dev/null 2>&1 || true' EXIT

echo "[host] starting QEMU"
rm -f "${PIDFILE}" || true
ARCH=aarch64 INITRD="${INITRD}" KERNEL="${KERNEL_IMAGE}" QEMULOG="${QEMULOG}" PIDFILE="${PIDFILE}" \
  bash /opt/v9fs/go9p-netfs/scripts/v9fs/qemu.bash

QEMUPID="$(cat "${PIDFILE}")"
echo "[host] QEMU pid=${QEMUPID}"

echo "[host] waiting for QEMU exit"
while kill -0 "${QEMUPID}" >/dev/null 2>&1; do
  sleep 2
done

echo "--- QEMU log tail (${QEMULOG}) ---"
tail -250 "${QEMULOG}" || true

grep -q "PASS: kernel9p netfs e2e" "${QEMULOG}"
echo "[host] PASS"

