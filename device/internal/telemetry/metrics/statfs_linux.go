package metrics

import "syscall"

// Statfs is the size of the filesystem at path and what of it is free to write, in bytes.
func Statfs(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	// the counts are in fragments; a kernel that reports none counts in blocks
	unit := uint64(st.Frsize) //nolint:gosec // never negative; int32 on arm, int64 elsewhere
	if unit == 0 {
		unit = uint64(st.Bsize) //nolint:gosec // as above
	}
	return st.Blocks * unit, st.Bavail * unit, nil
}
