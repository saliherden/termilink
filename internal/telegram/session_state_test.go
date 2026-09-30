package telegram

import (
	"os"
	"regexp"
	"sync"
	"testing"

	"github.com/saliherden/termilink/internal/config"
	"github.com/saliherden/termilink/internal/security"
	"github.com/saliherden/termilink/internal/session"
)

var (
	statusProjectRe = regexp.MustCompile("Project: `([^`]*)`")
	statusCwdRe     = regexp.MustCompile("Working directory:\n`([^`]*)`")
)

// statusContext pulls the two fields that have to agree with each other out of a
// rendered /status: the project binding and the working directory it implies.
func statusContext(out string) (project, cwd string) {
	if m := statusProjectRe.FindStringSubmatch(out); m != nil {
		project = m[1]
	}
	if m := statusCwdRe.FindStringSubmatch(out); m != nil {
		cwd = m[1]
	}
	return project, cwd
}

// TestStatusNeverShowsATornSession is the regression for the live-pointer race at
// the handler level. formatSessionStatus used to take the *State the manager
// stored, and it read Cwd, Project and LastCmd off it one at a time — while the
// goroutine running the user's command was rewriting those same fields. The
// reply could then pair a project with a directory that belonged to a different
// one, which is worse than either answer being late: it reads as authoritative.
//
// Handler now takes a value, so the three fields come from one snapshot. The
// writer below flips the session between two states that are each internally
// coherent, and every rendered status has to be exactly one of them.
func TestStatusNeverShowsATornSession(t *testing.T) {
	h := NewHandler(Options{
		Authorizer: security.New(1, []int64{1}),
		Sessions:   session.NewManager(),
		Projects:   map[string]config.ProjectConfig{"app": {Path: "/srv/app"}},
	})
	const id = "170"
	home, err := os.UserHomeDir()
	if err != nil {
		home = "$HOME"
	}

	type binding struct{ project, cwd string }
	inApp := binding{"app", "/srv/app"}
	atHome := binding{"", home}
	set := func(b binding) {
		h.mutateSession(id, func(s *session.State) {
			s.Cwd = b.cwd
			s.Project = b.project
		})
	}
	set(inApp)

	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				set(inApp)
			} else {
				set(atHome)
			}
		}
	}()

	for i := 0; i < 400; i++ {
		st, ok := h.sessions.Snapshot(id)
		if !ok {
			close(stop)
			writer.Wait()
			t.Fatal("session vanished")
		}
		got := binding{}
		got.project, got.cwd = statusContext(h.formatSessionStatus(st))
		if got != inApp && got != atHome {
			close(stop)
			writer.Wait()
			t.Fatalf("status paired fields from two different states: %+v", got)
		}
	}
	close(stop)
	writer.Wait()
}

// TestStatusSeesAWriteImmediately pins the other half: because the handler reads
// a snapshot rather than a pointer it happened to be handed earlier, a change
// made by this update is visible to the next one and to nothing in between.
func TestStatusSeesAWriteImmediately(t *testing.T) {
	h := NewHandler(Options{
		Authorizer: security.New(1, []int64{1}),
		Sessions:   session.NewManager(),
		Projects:   map[string]config.ProjectConfig{"app": {Path: "/srv/app"}},
	})
	const id = "171"
	if _, err := h.sessions.Ensure(id); err != nil {
		t.Fatal(err)
	}
	before, _ := h.sessions.Snapshot(id)
	if p, c := statusContext(h.formatSessionStatus(before)); p != "" || c == "" {
		t.Fatalf("fresh session should report no project: project=%q cwd=%q", p, c)
	}

	h.mutateSession(id, func(s *session.State) {
		s.Cwd = "/srv/app"
		s.Project = "app"
	})
	after, _ := h.sessions.Snapshot(id)
	if p, c := statusContext(h.formatSessionStatus(after)); p != "app" || c != "/srv/app" {
		t.Fatalf("status did not pick up the write: project=%q cwd=%q", p, c)
	}
}
