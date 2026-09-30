package terminal

type Result struct {
	ExitCode int
	Output   []byte
	PID      int
}

type Runner struct {
	shell     string
	maxOutput int
}

// NewRunner opens persistent shells that live until they are closed, so there
// is no per-command deadline to hold here: the timeout is enforced by the
// caller as a context deadline around the run. A timeout field on the Runner
// would only ever be a second, unread copy of that value.
func NewRunner(shell string, maxOutput int) *Runner {
	return &Runner{shell: shell, maxOutput: maxOutput}
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
