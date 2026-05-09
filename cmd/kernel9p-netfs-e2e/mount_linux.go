//go:build linux

package main

import (
	"fmt"
	"syscall"
)

// After chroot /mnt/9, invoking /bbin/mount from u-root fails (that path lives
// on the old initrd root). Use the kernel syscall directly inside the guest.
func platformMount9P(addr, port, mountpoint string) error {
	opts := fmt.Sprintf("trans=tcp,version=9p2000.L,msize=262144,port=%s", port)
	return syscall.Mount(addr, mountpoint, "9p", 0, opts)
}
