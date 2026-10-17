//go:build unix

package main

import "os"

// syncDirectoryHandle flushes directory entry changes to stable storage.
func syncDirectoryHandle(directory *os.File) error {
	return directory.Sync()
}
