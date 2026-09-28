package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"sync"
	"time"

	"github.com/saliherden/termilink/internal/audit"
)

// sessionListArgs is how an opencode-compatible CLI is asked for its sessions as
// JSON. It is a probe, not a contract: a CLI that does not understand these
// subcommands (claude, codex, gemini) fails, and the whole feature quietly steps
// aside instead of reporting a wrong id.
var sessionListArgs = []string{"session", "list", "--format", "json", "--max-count", "200"}

// sessionProbeTimeout bounds the one subprocess an id lookup runs. The owner is
// waiting on the exit message, so a hung CLI must not hold the chat hostage.
const sessionProbeTimeout = 5 * time.Second

// sessionProbeGrace absorbs clock skew, and a CLI that stamps `created` a moment
// before we record the start time — which would otherwise hide the very session
// we just ran.
const sessionProbeGrace = 2 * time.Second

// agentSession is one row of the CLI's session list. Only the fields the id
// lookup needs are decoded; opencode also returns a title, which is unused
// because the note is about which session to reopen, not what it was about.
type agentSession struct {
	ID        string `json:"id"`
	Updated   int64  `json:"updated"`
	Created   int64  `json:"created"`
	Directory string `json:"directory"`
}

// sessionProbeState is the cached answer, kept apart from the mutex that guards
// it so it can be copied as a plain value.
type sessionProbeState struct {
	tried bool
	ok    bool
}

// sessionProbe caches whether the configured agent CLI can list its sessions. A
// failed probe is a property of the binary rather than a transient outage, so it
// is remembered: otherwise every close of a claude or codex session would pay a
// doomed subprocess. The zero state means "not tried yet", which must still
// probe — that is why the field is "tried" rather than "untried".
var sessionProbe struct {
	mu    sync.Mutex
	state sessionProbeState
}

// canProbeSessions reports whether a session lookup is worth attempting. It
// answers yes before the first probe, because the result is only knowable by
// trying.
func canProbeSessions() bool {
	sessionProbe.mu.Lock()
	defer sessionProbe.mu.Unlock()
	return !sessionProbe.state.tried || sessionProbe.state.ok
}

// recordSessionProbe caches the outcome of a session-list attempt.
func recordSessionProbe(ok bool) {
	sessionProbe.mu.Lock()
	defer sessionProbe.mu.Unlock()
	sessionProbe.state = sessionProbeState{tried: true, ok: ok}
}

var errNoSessionList = errors.New("agent cli has no session list")

// listAgentSessions asks the agent CLI for its sessions and returns the ones
// belonging to dir. The CLI runs in the project directory because that is how
// it decides which sessions are "here"; the directory field is checked as well,
// so a CLI that ignored its working directory could not leak another project's
// session into the answer.
func listAgentSessions(bin, dir string) ([]agentSession, error) {
	if bin == "" || dir == "" {
		return nil, errNoSessionList
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, sessionListArgs...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var rows []agentSession
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	mine := make([]agentSession, 0, len(rows))
	for _, r := range rows {
		if r.ID == "" {
			continue
		}
		if r.Directory != "" && r.Directory != dir {
			continue
		}
		mine = append(mine, r)
	}
	return mine, nil
}

// resolveAgentSessionID works out which session the run used, or "" when it
// cannot say so with confidence.
//
// opencode does not put its session id anywhere worth scraping out of the TUI,
// so the id comes from asking the CLI once the agent is gone. That raises the
// question of which row is ours when the project already has sessions. Deciding
// by recency alone would be a guess, and a wrong id is worse than none: it looks
// authoritative and walks the owner into the wrong conversation. So the rows
// created inside this run's own lifetime are collected, and the id is reported
// only when that leaves exactly one candidate. Zero means no session was ever
// created (the agent exited before its first turn); more than one means
// something else started a session here at the same moment, and we decline to
// choose.
func resolveAgentSessionID(bin, dir string, startMs int64) string {
	if bin == "" || dir == "" {
		// A run that was never wired up to ask is not a CLI that cannot answer,
		// so this must not reach the cache below: doing so would disable the
		// lookup for every properly configured chat in the process.
		return ""
	}
	if !canProbeSessions() {
		return ""
	}
	rows, err := listAgentSessions(bin, dir)
	if err != nil {
		// An exec or parse failure means the subcommand is not understood, which
		// is a property of the binary and is cached so the next close does not
		// try again. A timeout fails the same way and is likewise not worth
		// retrying in the middle of an exit.
		recordSessionProbe(false)
		return ""
	}
	recordSessionProbe(true)

	cutoff := startMs - sessionProbeGrace.Milliseconds()
	var found []agentSession
	for _, r := range rows {
		if r.Created >= cutoff {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		return ""
	}
	return found[0].ID
}

// appendAgentSessionID follows the exit message with the session id, when one can
// be established. It runs after the screen text has already been delivered so
// that a slow or unsupported CLI cannot delay or reorder the output the owner
// asked for; a failure here leaves the screen message exactly as it was.
func (h *Handler) appendAgentSessionID(run *agentRun) {
	id := resolveAgentSessionID(run.bin, run.dir, run.startMs)
	if id == "" {
		return
	}
	// No user made this request, so the entry is not attributed to one.
	h.auditEvent(entryFor(run.chatID, 0, false, audit.ActionAgentSession, id))
	h.send(context.Background(), run.bot, run.chatID, formatAgentSessionID(id))
}

// formatAgentSessionID renders the id together with the command that reopens it.
// The command is included because an id on its own is only actionable if you
// already know what to do with it, and reaching the session from this message is
// the point of reporting it.
func formatAgentSessionID(id string) string {
	return "🆔 Session `" + id + "` — resume with `opencode -s " + id + "`"
}
