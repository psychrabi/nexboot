//go:build unix

package assets

import (
	"os"
	"syscall"
)

// sourceDataSize reports actual source usage as the minimum of file length and allocated blocks,
// counting only allocated data for sparse raw images. Returns false for inaccessible files.
func sourceDataSize(path string) (int64, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	size := info.Size()
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if allocated := int64(st.Blocks) * 512; allocated < size {
			size = allocated
		}
	}
	return size, true
}
