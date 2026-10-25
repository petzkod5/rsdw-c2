package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultLogTailLines     = 500
	maxLogTailLines         = 5000
	maxLogLineBytes         = 4 << 10
	maxLogBytes             = 2 << 20
	maxLogStreamsPerSubject = 2
	maxLogStreamsTotal      = 8
)

var (
	logFollowLimit       = 15 * time.Minute
	logHeartbeatInterval = 20 * time.Second
	logContainers        = map[string]bool{"server": true, "metrics": true}
)

type logStreamRequest struct {
	Container string
	Previous  bool
	Follow    bool
	TailLines int
}

type logStreamer interface {
	StreamLogs(context.Context, Server, logStreamRequest, io.Writer) error
}

type outputStreamRunner interface {
	StreamOutput(context.Context, io.Writer, string, ...string) error
}

func parseLogStreamRequest(query url.Values) (logStreamRequest, error) {
	req := logStreamRequest{Container: query.Get("container"), Previous: query.Get("previous") == "1", Follow: query.Get("follow") == "1", TailLines: defaultLogTailLines}
	if req.Container == "" {
		req.Container = "server"
	}
	if !logContainers[req.Container] {
		return req, errors.New("container must be server or metrics")
	}
	if req.Previous && req.Follow {
		return req, errors.New("previous logs cannot be followed")
	}
	if raw := query.Get("tailLines"); raw != "" {
		lines, err := strconv.Atoi(raw)
		if err != nil {
			return req, errors.New("tailLines must be a number")
		}
		req.TailLines = min(max(lines, 1), maxLogTailLines)
	}
	return req, nil
}

func (k *kubeOrchestrator) StreamLogs(ctx context.Context, server Server, req logStreamRequest, w io.Writer) error {
	runner, ok := k.runner.(outputStreamRunner)
	if !ok {
		return errors.New("log streaming is unavailable")
	}
	args := []string{"-n", server.Namespace, "logs", "deployment/" + deploymentName(server.Release), "-c", req.Container, "--tail=" + strconv.Itoa(req.TailLines), "--timestamps"}
	if req.Follow {
		args = append(args, "--follow")
	} else {
		args = append(args, "--limit-bytes="+strconv.Itoa(maxLogBytes))
	}
	if req.Previous {
		args = append(args, "--previous")
	}
	return runner.StreamOutput(ctx, w, k.kubectl, args...)
}

// StreamOutput is RunStream without its 60 second cap. The caller's context
// bounds the run, and cancelling it kills the process.
func (shellRunner) StreamOutput(ctx context.Context, destination io.Writer, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = destination
	var stderr boundedOutput
	stderr.limit = maxCommandStderrBytes
	command.Stderr = &stderr
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if diagnostic := commandDiagnostic(nil, stderr.data); diagnostic != "" {
			return errors.New(diagnostic)
		}
		return fmt.Errorf("%s logs: %w", name, err)
	}
	return nil
}

type logStreamLimiter struct {
	mu        sync.Mutex
	total     int
	bySubject map[string]int
}

func (l *logStreamLimiter) acquire(subject string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= maxLogStreamsTotal || l.bySubject[subject] >= maxLogStreamsPerSubject {
		return false
	}
	if l.bySubject == nil {
		l.bySubject = map[string]int{}
	}
	l.total++
	l.bySubject[subject]++
	return true
}

func (l *logStreamLimiter) release(subject string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if l.bySubject[subject]--; l.bySubject[subject] <= 0 {
		delete(l.bySubject, subject)
	}
}

// sseLogWriter turns raw log output into one server-sent event per line.
type sseLogWriter struct {
	mu       sync.Mutex
	w        io.Writer
	rc       *http.ResponseController
	partial  []byte
	skipping bool
}

func (s *sseLogWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			if !s.skipping {
				s.line(s.partial, false)
			}
			s.partial, s.skipping = s.partial[:0], false
			continue
		}
		if s.skipping {
			continue
		}
		s.partial = append(s.partial, b)
		if len(s.partial) > maxLogLineBytes {
			s.line(s.partial[:maxLogLineBytes], true)
			s.partial, s.skipping = s.partial[:0], true
		}
	}
	return len(p), s.rc.Flush()
}

func (s *sseLogWriter) line(raw []byte, truncated bool) {
	text := strings.ToValidUTF8(strings.TrimSuffix(string(raw), "\r"), "")
	if truncated {
		text += " [truncated]"
	}
	fmt.Fprintf(s.w, "data: %s\n\n", text)
}

func (s *sseLogWriter) send(chunk string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	io.WriteString(s.w, chunk)
	s.rc.Flush()
}

func (s *sseLogWriter) end(reason string, err error) {
	s.mu.Lock()
	if len(s.partial) > 0 && !s.skipping {
		s.line(s.partial, false)
	}
	s.partial = nil
	s.mu.Unlock()
	payload := struct {
		Reason string `json:"reason"`
		Error  string `json:"error,omitempty"`
	}{Reason: reason}
	if err != nil {
		payload.Error = err.Error()
	}
	data, _ := json.Marshal(payload)
	s.send("event: end\ndata: " + string(data) + "\n\n")
}

func (a *App) handleLogStream(w http.ResponseWriter, r *http.Request, server Server) {
	req, err := parseLogStreamRequest(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	streamer, ok := a.orchestrator.(logStreamer)
	if !ok {
		writeError(w, http.StatusNotImplemented, "log streaming is unavailable")
		return
	}
	subject := requestPrincipal(r).Subject
	if !a.logStreams.acquire(subject) {
		writeError(w, http.StatusTooManyRequests, "too many open log streams; close one and try again")
		return
	}
	defer a.logStreams.release(subject)

	ctx, cancel := context.WithCancel(r.Context())
	if req.Follow {
		ctx, cancel = context.WithTimeout(r.Context(), logFollowLimit)
	}
	defer cancel()
	rc := http.NewResponseController(w)
	// The server's 2 minute WriteTimeout would otherwise cut a follow short.
	_ = rc.SetWriteDeadline(time.Now().Add(logFollowLimit + time.Minute))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	out := &sseLogWriter{w: w, rc: rc}
	out.send(": stream open\n\n")

	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(logHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				out.send(": ping\n\n")
			case <-heartbeatDone:
				return
			}
		}
	}()
	err = streamer.StreamLogs(ctx, server, req, out)
	close(heartbeatDone)
	switch {
	case r.Context().Err() != nil:
		return
	case req.Follow && errors.Is(ctx.Err(), context.DeadlineExceeded):
		out.end("timeout", nil)
	case err != nil:
		out.end("error", err)
	default:
		out.end("complete", nil)
	}
}
