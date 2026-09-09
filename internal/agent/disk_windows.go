package agent

import "golang.org/x/sys/windows"

func freeDisk(path string) int64 {
	var free, total, avail uint64
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return -1
	}
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &avail); err != nil {
		return -1
	}
	return int64(free)
}
