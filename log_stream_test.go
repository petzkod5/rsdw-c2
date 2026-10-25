package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type logStreamRunner struct {
	CommandRunner
	mu    sync.Mutex
	calls [][]string
	emit  func(ctx context.Context, w io.Writer) error
}

func (r *logStreamRunner) StreamOutput(ctx context.Context, w io.Writer, name string, args ...string) error {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string{name}, args...))
	r.mu.Unlock()
	if r.emit == nil {
		return nil
	}
	return r.emit(ctx, w)
}

func (r *logStreamRunner) lastCall() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1]
}

func logStreamApp(t *testing.T, runner *logStreamRunner) (*App, Server) {
	t.Helper()
	app := newTestApp(t, true)
	app.orchestrator = &kubeOrchestrator{runner: runner, kubectl: "kubectl"}
	for _, server := range app.store.Snapshot().Servers {
		if server.Status != StatusStopped {
			return app, server
		}
	}
	t.Fatal("no running demo server")
	return nil, Server{}
}

func TestLogStreamBuildsKubectlArguments(t *testing.T) {
	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"-c", "server", "--tail=500", "--timestamps", "--limit-bytes=2097152"}},
		{"?container=metrics&previous=1&tailLines=20", []string{"-c", "metrics", "--tail=20", "--timestamps", "--limit-bytes=2097152", "--previous"}},
		{"?follow=1&tailLines=99999", []string{"-c", "server", "--tail=5000", "--timestamps", "--follow"}},
		{"?tailLines=0", []string{"-c", "server", "--tail=1", "--timestamps", "--limit-bytes=2097152"}},
	}
	for _, tc := range cases {
		runner := &logStreamRunner{}
		app, server := logStreamApp(t, runner)
		res := requestJSON(t, app, http.MethodGet, "/api/servers/"+server.ID+"/logs/stream"+tc.query, "")
		if res.Code != http.StatusOK {
			t.Fatalf("%q status = %d body = %s", tc.query, res.Code, res.Body.String())
		}
		want := append([]string{"kubectl", "-n", server.Namespace, "logs", "deployment/" + deploymentName(server.Release)}, tc.want...)
		if got := runner.lastCall(); !reflect.DeepEqual(got, want) {
			t.Fatalf("%q args = %q, want %q", tc.query, got, want)
		}
	}
}

func TestLogStreamRejectsBadInputWithoutRunningKubectl(t *testing.T) {
	for _, query := range []string{"?container=game", "?container=server%20--all-containers", "?previous=1&follow=1"} {
		runner := &logStreamRunner{}
		app, server := logStreamApp(t, runner)
		res := requestJSON(t, app, http.MethodGet, "/api/servers/"+server.ID+"/logs/stream"+query, "")
		if res.Code != http.StatusBadRequest || runner.lastCall() != nil {
			t.Fatalf("%q status = %d, calls = %v", query, res.Code, runner.calls)
		}
	}
}

func TestLogStreamWritesServerSentEvents(t *testing.T) {
	long := strings.Repeat("x", maxLogLineBytes+50)
	runner := &logStreamRunner{emit: func(_ context.Context, w io.Writer) error {
		_, _ = io.WriteString(w, "2026-10-24T20:00:00Z first line\n2026-10-24T20:00:01Z JoinCode R2AB-PN0P\n"+long+"\npartial")
		return nil
	}}
	app, server := logStreamApp(t, runner)
	res := requestJSON(t, app, http.MethodGet, "/api/servers/"+server.ID+"/logs/stream", "")
	if got := res.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q", got)
	}
	body := res.Body.String()
	for _, want := range []string{
		"data: 2026-10-24T20:00:00Z first line\n\n",
		"data: 2026-10-24T20:00:01Z JoinCode R2AB-PN0P\n\n",
		"data: " + strings.Repeat("x", maxLogLineBytes) + " [truncated]\n\n",
		"data: partial\n\n",
		"event: end\ndata: {\"reason\":\"complete\"}\n\n",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestLogStreamReportsKubectlFailure(t *testing.T) {
	runner := &logStreamRunner{emit: func(context.Context, io.Writer) error {
		return errors.New("previous terminated container \"server\" not found")
	}}
	app, server := logStreamApp(t, runner)
	body := requestJSON(t, app, http.MethodGet, "/api/servers/"+server.ID+"/logs/stream?previous=1", "").Body.String()
	if !strings.Contains(body, "event: end\ndata: {\"reason\":\"error\",\"error\":\"previous terminated container \\\"server\\\" not found\"}\n\n") {
		t.Fatalf("body = %s", body)
	}
}

func TestLogStreamFollowEndsAtLimitAndSendsHeartbeats(t *testing.T) {
	defer setLogStreamTimings(400*time.Millisecond, 50*time.Millisecond)()
	runner := &logStreamRunner{emit: func(ctx context.Context, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	app, server := logStreamApp(t, runner)
	started := time.Now()
	body := requestJSON(t, app, http.MethodGet, "/api/servers/"+server.ID+"/logs/stream?follow=1", "").Body.String()
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("follow ran %s past its limit", elapsed)
	}
	if !strings.Contains(body, ": ping\n\n") || !strings.Contains(body, "event: end\ndata: {\"reason\":\"timeout\"}\n\n") {
		t.Fatalf("body = %s", body)
	}
}

func TestLogStreamLimitsConcurrentStreams(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 16)
	runner := &logStreamRunner{emit: func(ctx context.Context, _ io.Writer) error {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}}
	app, server := logStreamApp(t, runner)
	path := "/api/servers/" + server.ID + "/logs/stream?follow=1"
	var wg sync.WaitGroup
	for range maxLogStreamsPerSubject {
		wg.Add(1)
		go func() { defer wg.Done(); requestJSON(t, app, http.MethodGet, path, "") }()
		<-started
	}
	if res := requestJSON(t, app, http.MethodGet, path, ""); res.Code != http.StatusTooManyRequests {
		t.Fatalf("stream over the per-user limit = %d", res.Code)
	}
	close(release)
	wg.Wait()
	if res := requestJSON(t, app, http.MethodGet, "/api/servers/"+server.ID+"/logs/stream", ""); res.Code != http.StatusOK {
		t.Fatalf("stream after release = %d", res.Code)
	}

	streams := &logStreamLimiter{}
	for i := range maxLogStreamsTotal {
		if !streams.acquire(fmt.Sprintf("user-%d", i)) {
			t.Fatalf("stream %d rejected under the total limit", i)
		}
	}
	if streams.acquire("another-user") {
		t.Fatal("stream over the total limit was accepted")
	}
	streams.release("user-0")
	if !streams.acquire("another-user") {
		t.Fatal("released slot was not reusable")
	}
}

func setLogStreamTimings(follow, heartbeat time.Duration) func() {
	previousFollow, previousHeartbeat := logFollowLimit, logHeartbeatInterval
	logFollowLimit, logHeartbeatInterval = follow, heartbeat
	return func() { logFollowLimit, logHeartbeatInterval = previousFollow, previousHeartbeat }
}

func TestLogStreamOneShotReadHasItsOwnTimeout(t *testing.T) {
	previous := logReadLimit
	logReadLimit = 200 * time.Millisecond
	defer func() { logReadLimit = previous }()
	runner := &logStreamRunner{emit: func(ctx context.Context, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	app, server := logStreamApp(t, runner)
	started := time.Now()
	body := requestJSON(t, app, http.MethodGet, "/api/servers/"+server.ID+"/logs/stream", "").Body.String()
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("one-shot read ran %s", elapsed)
	}
	if !strings.Contains(body, "event: end\ndata: {\"reason\":\"error\",\"error\":\"reading logs took too long\"}\n\n") {
		t.Fatalf("body = %s", body)
	}
}

func TestLogStreamStripsCarriageReturnsSoLinesCannotForgeEvents(t *testing.T) {
	runner := &logStreamRunner{emit: func(_ context.Context, w io.Writer) error {
		_, _ = io.WriteString(w, "boom\revent: end\rdata: {}\r\n")
		return nil
	}}
	app, server := logStreamApp(t, runner)
	body := requestJSON(t, app, http.MethodGet, "/api/servers/"+server.ID+"/logs/stream", "").Body.String()
	if strings.Contains(body, "\r") || !strings.Contains(body, "data: boomevent: enddata: {}\n\n") {
		t.Fatalf("body = %q", body)
	}
}

func TestLogStreamRejectsSignedInViewer(t *testing.T) {
	app, issuer := oidcTestApp(t)
	runner := &logStreamRunner{}
	app.orchestrator = &kubeOrchestrator{runner: runner, kubectl: "kubectl"}
	cookie, _ := loginAs(t, app, issuer, "viewer")
	res := authRequest(app, "GET", "/api/servers/scuffedtards/logs/stream?follow=1", cookie, "", "", "")
	if res.Code != http.StatusForbidden {
		t.Fatalf("viewer log stream = %d: %s", res.Code, res.Body.String())
	}
	if call := runner.lastCall(); call != nil {
		t.Fatalf("viewer request ran kubectl: %v", call)
	}
}

func TestLogStreamWriterDropsOutputAfterClose(t *testing.T) {
	recorder := httptest.NewRecorder()
	out := &sseLogWriter{w: recorder, rc: http.NewResponseController(recorder)}
	out.close()
	if n, err := out.Write([]byte("late line\n")); n != 10 || err != nil {
		t.Fatalf("Write after close = %d, %v", n, err)
	}
	if body := recorder.Body.String(); body != "" {
		t.Fatalf("late output reached the response: %q", body)
	}
}
