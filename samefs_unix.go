//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// sameFilesystem reports whether path a and the directory containing
// path b reside on the same filesystem. Used by the auto-delete path
// to refuse renames across filesystems unless --allow-cross-fs is set.
func sameFilesystem(a, b string) bool {
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(filepath.Dir(b))
	if errA != nil || errB != nil {
		return false
	}
	stA, okA := sa.Sys().(*syscall.Stat_t)
	stB, okB := sb.Sys().(*syscall.Stat_t)
	if !okA || !okB {
		return false
	}
	return stA.Dev == stB.Dev
}
