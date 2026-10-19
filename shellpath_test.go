package main

import (
	"runtime"
	"testing"
)

func TestShellPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		if got := shellPath("/var/data/world"); got != "/var/data/world" {
			t.Fatalf("shellPath changed a POSIX path: %q", got)
		}
		return
	}
	for input, want := range map[string]string{
		`C:\Users\dylan\Temp\x`: "/c/Users/dylan/Temp/x",
		`D:\`:                   "/d/",
		`relative\dir`:          "relative/dir",
	} {
		if got := shellPath(input); got != want {
			t.Errorf("shellPath(%q) = %q, want %q", input, got, want)
		}
	}
}
