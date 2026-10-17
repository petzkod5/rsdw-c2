//go:build windows

package main

import (
	"path/filepath"
	"strings"
)

// shellPath converts a drive path such as C:\a\b to /c/a/b so MSYS/Git Bash sh
// scripts that walk parent directories terminate at "/".
func shellPath(path string) string {
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return filepath.ToSlash(path)
	}
	return "/" + strings.ToLower(volume[:1]) + filepath.ToSlash(path[2:])
}
