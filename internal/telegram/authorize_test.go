package telegram

import (
	"testing"

	"github.com/saliherden/termilink/internal/session"
)

func TestAuthorizeCommandOwner(t *testing.T) {
	h := &Handler{}
	st := &session.State{}
	reason, ok := h.authorizeCommand(st, true, "cd /etc")
	if !ok {
		t.Fatalf("owner cd should be allowed, got reason %q", reason)
	}
	reason, ok = h.authorizeCommand(st, true, "rm -rf /")
	if !ok {
		t.Fatalf("owner command should be allowed, got reason %q", reason)
	}
}

func TestAuthorizeCommandWorker(t *testing.T) {
	h := &Handler{}
	cases := []struct {
		name string
		st   *session.State
		raw  string
		ok   bool
	}{
		{"cd blocked", &session.State{Project: "p"}, "cd /etc", false},
		{"bare cd blocked", &session.State{Project: "p"}, "cd", false},
		{"no project blocked", &session.State{}, "ls", false},
		{"no project cd blocked", &session.State{}, "cd /tmp", false},
		{"with project allowed", &session.State{Project: "p"}, "make build", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := h.authorizeCommand(tc.st, false, tc.raw)
			if ok != tc.ok {
				t.Fatalf("authorizeCommand(worker, %q) = %v, want %v", tc.raw, ok, tc.ok)
			}
		})
	}
}
