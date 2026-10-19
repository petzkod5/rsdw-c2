//go:build unix

package main

// shellPath returns path in the form the POSIX sh on this host expects.
func shellPath(path string) string {
	return path
}
