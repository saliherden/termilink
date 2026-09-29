package terminal

import (
	"time"
)

type Result struct {
	ExitCode int
	Output   []byte
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
