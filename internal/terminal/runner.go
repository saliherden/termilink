package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

type StreamType int

const (
	StreamStdout StreamType = iota
	StreamStderr
)

type Chunk struct {
	Stream StreamType
	Data   []byte
}

type Result struct {
	ExitCode int
	Output   []byte
	TimedOut bool
	Capped   bool
	PID      int
}

type Runner struct {
	shell     string
	timeout   time.Duration
	maxOutput int
}

func NewRunner(shell string, timeout time.Duration, maxOutput int) *Runner {
	return &Runner{shell: shell, timeout: timeout, maxOutput: maxOutput}
}

func (r *Runner) CommandTimeout() time.Duration {
	return r.timeout
}

func (r *Runner) Execute(ctx context.Context, dir string, command string, onChunk func(Chunk)) (Result, error) {
	if r.shell == "" {
		return Result{}, fmt.Errorf("terminal: no shell configured")
	}

	runCtx := ctx
	cancel := func() {}
	if r.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, r.timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, r.shell, "-c", command)
	if dir != "" {
		cmd.Dir = dir
	}

	var out bytes.Buffer
	writer := &outputWriter{out: &out, onChunk: onChunk, max: r.maxOutput}
	cmd.Stdout = writer.forStream(StreamStdout)
	cmd.Stderr = writer.forStream(StreamStderr)

	err := cmd.Run()
	res := Result{
		Output:   out.Bytes(),
		TimedOut: errors.Is(runCtx.Err(), context.DeadlineExceeded),
		Capped:   writer.capped,
	}
	if cmd.Process != nil {
		res.PID = cmd.Process.Pid
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	} else {
		res.ExitCode = -1
	}
	if err != nil && !res.TimedOut {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		}
	}
	return res, nil
}

type outputWriter struct {
	mu      sync.Mutex
	out     *bytes.Buffer
	onChunk func(Chunk)
	max     int
	capped  bool
}

func (w *outputWriter) forStream(stream StreamType) io.Writer {
	return &streamWriter{stream: stream, parent: w}
}

func (w *outputWriter) Write(stream StreamType, data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.max > 0 && w.out.Len() < w.max {
		room := w.max - w.out.Len()
		if len(data) > room {
			data = data[:room]
			w.capped = true
		}
		w.out.Write(data)
	}
	if w.onChunk != nil && len(data) > 0 {
		w.onChunk(Chunk{Stream: stream, Data: data})
	}
	return len(data), nil
}

type streamWriter struct {
	stream StreamType
	parent *outputWriter
}

func (s streamWriter) Write(p []byte) (int, error) {
	return s.parent.Write(s.stream, p)
}

func TruncateOutput(data []byte, maxBytes int) []byte {
	if data == nil {
		return nil
	}
	if maxBytes <= 0 || len(data) <= maxBytes {
		return data
	}
	marker := []byte("\n… [truncated] …\n")
	if maxBytes <= len(marker) {
		return append([]byte(nil), data[:maxBytes]...)
	}
	keepEach := (maxBytes - len(marker)) / 2
	if keepEach < 0 {
		keepEach = 0
	}
	head := data[:keepEach]
	tail := data[len(data)-keepEach:]
	buf := make([]byte, 0, keepEach+len(marker)+keepEach)
	buf = append(buf, head...)
	buf = append(buf, marker...)
	buf = append(buf, tail...)
	return buf
}
