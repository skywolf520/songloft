//go:build !windows

package handlers

import "syscall"

func getDiskSpace(path string) (total, free int64) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err == nil {
		total = int64(fs.Blocks) * int64(fs.Bsize)
		free = int64(fs.Bavail) * int64(fs.Bsize)
	}
	return
}
