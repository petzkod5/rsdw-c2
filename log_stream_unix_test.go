//go:build !windows

package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLogStreamOutlivesServerTimeoutsAndKillsKubectlOnDisconnect(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	script := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho $$ > "+pidFile+"\ni=0\nwhile true; do i=$((i+1)); echo \"line $i\"; sleep 0.2; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	app := newTestApp(t, true)
	app.orchestrator = &kubeOrchestrator{runner: shellRunner{}, kubectl: script}
	var server Server
	for _, candidate := range app.store.Snapshot().Servers {
		if candidate.Status != StatusStopped {
			server = candidate
		}
	}
	httpServer := httptest.NewUnstartedServer(app)
	httpServer.Config.ReadTimeout = 500 * time.Millisecond
	httpServer.Config.WriteTimeout = 500 * time.Millisecond
	httpServer.Start()
	defer httpServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/servers/"+server.ID+"/logs/stream?follow=1", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(res.Body)
	deadline := time.Now().Add(2 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("stream broke after %q: %v", last, err)
		}
		if strings.HasPrefix(line, "data: line") {
			last = strings.TrimSpace(line)
		}
	}
	if last == "" {
		t.Fatal("no lines streamed")
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	fmt.Sscan(string(raw), &pid)
	cancel()
	res.Body.Close()
	for start := time.Now(); syscall.Kill(pid, 0) == nil; {
		if time.Since(start) > 3*time.Second {
			t.Fatalf("kubectl pid %d still running after disconnect", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
