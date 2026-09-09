package agent

// FreeDisk returns free bytes on the volume holding path.
func FreeDisk(path string) int64 { return freeDisk(path) }
