package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileLockExcludesOtherHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".writer.lock")
	first, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	if err := lockFileExclusive(first); err != nil {
		t.Fatalf("lock first handle: %v", err)
	}
	acquired := make(chan error, 1)
	go func() { acquired <- lockFileExclusive(second) }()
	select {
	case err := <-acquired:
		t.Fatalf("second handle acquired a held lock (err=%v)", err)
	case <-time.After(150 * time.Millisecond):
	}

	if err := unlockFile(first); err != nil {
		t.Fatalf("unlock first handle: %v", err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("lock second handle after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second handle never acquired the released lock")
	}
	if err := unlockFile(second); err != nil {
		t.Fatalf("unlock second handle: %v", err)
	}
}

func TestWriterLockSerializesActions(t *testing.T) {
	repository := &LocalBackupRepository{root: t.TempDir()}
	running := false
	done := make(chan struct{})
	for range 2 {
		go func() {
			defer func() { done <- struct{}{} }()
			err := repository.withWriterLock(func() error {
				if running {
					t.Error("writer actions overlapped")
				}
				running = true
				time.Sleep(30 * time.Millisecond)
				running = false
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	<-done
	<-done
}
