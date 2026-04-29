//go:build windows

package main

// On Windows, cross-filesystem detection isn't a single os.Stat-style
// check (drive letters, junction points, mount points, network shares).
// Be permissive: assume same filesystem and let os.Rename / os.Remove
// surface real errors. Users can still pass --allow-cross-fs explicitly
// if they hit a cross-device rename.
func sameFilesystem(a, b string) bool {
	return true
}
