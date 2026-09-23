package telegram

import (
	"testing"

	"github.com/saliherden/termilink/internal/security"
	"github.com/saliherden/termilink/internal/session"
)

func TestWorkspaceVet(t *testing.T) {
	p, err := security.NewPolicy("", nil, []string{"/Users/s/projects"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Options{Policy: p})

	allowed := []string{
		"ls",
		"cat /Users/s/projects/mobile/README.md",
		"grep -r foo /Users/s/projects/mobile/src",
	}
	for _, raw := range allowed {
		if _, blocked := h.workspaceVet(raw); blocked {
			t.Errorf("workspaceVet(%q) blocked unexpectedly", raw)
		}
	}

	blocked := []string{
		"cat /etc/passwd",
		"cat ~/.ssh/id_rsa",
		"cat $HOME/notes",
		"cat /Users/s/elsewhere/x",
		"cat ../secret",
	}
	for _, raw := range blocked {
		if _, blocked := h.workspaceVet(raw); !blocked {
			t.Errorf("workspaceVet(%q) escaped", raw)
		}
	}
}

func TestWorkspaceVetDisabledWhenEmpty(t *testing.T) {
	p, _ := security.NewPolicy("", nil, nil)
	h := NewHandler(Options{Policy: p})
	if _, blocked := h.workspaceVet("cat /etc/passwd"); blocked {
		t.Fatal("vet must be off when workspace roots are empty")
	}
}

func TestAuthorizeCommandWorkerMatrix(t *testing.T) {
	p, _ := security.NewPolicy("", nil, []string{"/Users/s/projects"})
	h := NewHandler(Options{Policy: p})
	st := &session.State{ID: "chat", Project: "app", Cwd: "/Users/s/projects/app"}

	if _, ok := h.authorizeCommand(st, true, "cd /tmp && cat /etc/passwd"); !ok {
		t.Fatal("owner must bypass everything")
	}
	if _, ok := h.authorizeCommand(st, false, "cd /tmp"); ok {
		t.Fatal("worker cd must be blocked")
	}
	noProj := &session.State{ID: "chat"}
	if _, ok := h.authorizeCommand(noProj, false, "ls"); ok {
		t.Fatal("worker without project must be blocked")
	}
	if _, ok := h.authorizeCommand(st, false, "cat /etc/passwd"); ok {
		t.Fatal("worker out-of-workspace must be blocked")
	}
	if _, ok := h.authorizeCommand(st, false, "ls -la"); !ok {
		t.Fatal("worker in-scope command must pass")
	}
}

func TestApprovalVerdict(t *testing.T) {
	for _, yes := range []string{"evet", "EVET", "yes", "ok", "onay", " onayla "} {
		if approvalVerdict(yes) != answerApprove {
			t.Errorf("approvalVerdict(%q) not approve", yes)
		}
	}
	for _, no := range []string{"hayır", "hayir", "no", "cancel", "iptal", "reddet"} {
		if approvalVerdict(no) != answerDeny {
			t.Errorf("approvalVerdict(%q) not deny", no)
		}
	}
	if approvalVerdict("belki") != answerUnknown {
		t.Fatal("unexpected answer must be unknown")
	}
}

func TestNeedsApprovalBypassAfterResolve(t *testing.T) {
	p, _ := security.NewPolicy("", nil, nil)
	// After the pending request is consumed the raw command must still be
	// gateable when it reaches runCommand independently; the approval path
	// routes around DangerMatch on purpose. Verify the precondition: the raw
	// command is dangerous and gated for workers.
	if !p.NeedsApproval(false, "sudo whoami") {
		t.Fatal("sudo must be gated")
	}
	// And a safe command never is.
	if p.NeedsApproval(false, "ls") {
		t.Fatal("ls must not be gated")
	}
}
