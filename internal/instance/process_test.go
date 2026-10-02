package instance

import (
	"os"
	"testing"
)

// processAlive has a different implementation per platform, so these two tests
// are the shared contract both must satisfy. The Windows path cannot be
// exercised on the macOS or Linux runners, but Windows CI cross-compiles the
// package, so the code still has to compile there.

func TestProcessAliveForSelf(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("the current process must be reported alive")
	}
}

func TestProcessAliveForBogusPID(t *testing.T) {
	// Far beyond any real pid on either platform, so neither syscall.Kill's
	// ESRCH nor OpenProcess's invalid-parameter can be a true positive.
	if processAlive(1 << 30) {
		t.Fatal("a pid with no process behind it must be reported dead")
	}
}
