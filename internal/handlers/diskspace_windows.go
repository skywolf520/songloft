package handlers

import "golang.org/x/sys/windows"

func getDiskSpace(path string) (total, free int64) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	if err = windows.GetDiskFreeSpaceEx(p, &freeBytesAvailable, &totalBytes, &totalFreeBytes); err != nil {
		return
	}
	total = int64(totalBytes)
	free = int64(freeBytesAvailable)
	return
}
