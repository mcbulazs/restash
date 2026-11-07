//go:build unix

package server

import "syscall"

// diskUsage returns the size and used space of the filesystem holding dir.
func diskUsage(dir string) (total, used int64) {
	var st syscall.Statfs_t
	if dir == "" || syscall.Statfs(dir, &st) != nil {
		return 0, 0
	}
	total = int64(st.Blocks) * int64(st.Bsize)
	free := int64(st.Bfree) * int64(st.Bsize)
	return total, total - free
}
