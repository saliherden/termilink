package terminal

import (
	"strings"
	"testing"
)

func TestTruncateOutput(t *testing.T) {
	data := []byte(strings.Repeat("x", 100))
	got := TruncateOutput(data, 40)
	if len(got) > 40 {
		t.Fatalf("truncated length = %d, want <= 40", len(got))
	}
	if !strings.Contains(string(got), "truncated") {
		t.Fatalf("truncated output missing marker: %q", got)
	}
	if got := TruncateOutput(data, 0); string(got) != string(data) {
		t.Fatal("zero cap must pass through data")
	}
	if TruncateOutput(nil, 10) != nil {
		t.Fatal("nil must stay nil")
	}
}
