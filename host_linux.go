//go:build linux

package main

import "syscall"

// diskUsage reports total and free bytes for the filesystem holding path.
//
// Statfs, not `df`: df on a dead NFS mount blocks in uninterruptible sleep and
// took the prototype's whole status collector with it. This returns an error
// instead, which the caller renders as "unreadable" rather than as zero space.
func diskUsage(path string) (total, free int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := int64(st.Bsize)
	return int64(st.Blocks) * bs, int64(st.Bavail) * bs, nil
}
