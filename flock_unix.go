//go:build unix

package main

import (
	"os"
	"syscall"
)

// lockFileExclusive blocks until it holds an exclusive advisory lock on file.
func lockFileExclusive(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

func unlockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
