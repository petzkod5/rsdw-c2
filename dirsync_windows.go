//go:build windows

package main

import "os"

// syncDirectoryHandle is a no-op on Windows: a directory cannot be opened for
// writing, so FlushFileBuffers on it fails with "Access is denied". NTFS
// journals directory metadata, which is the durability this call exists for on
// Unix filesystems.
func syncDirectoryHandle(*os.File) error {
	return nil
}
