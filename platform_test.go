package main

import "runtime"

// hasPOSIXPermissions reports whether file mode bits are enforced and reported faithfully.
func hasPOSIXPermissions() bool {
	return runtime.GOOS != "windows"
}
