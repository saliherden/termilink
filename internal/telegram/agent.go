package telegram

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/saliherden/termilink/internal/agent"
	"github.com/saliherden/termilink/internal/audit"
	"github.com/saliherden/termilink/internal/session"
	"github.com/saliherden/termilink/internal/terminal"
)

const (
	agentFrameCap    = 3400
	agentDebounce    = 350 * time.Millisecond
	agentTypingEvery = 4 * time.Second
	agentBootDelay   = 400 * time.Millisecond

	// A history view is one screenful of lines, minus a small overlap: stepping
	// by a full page would jump over lines, while the overlap keeps scrolling
	// continuous without skipping anything.
	agentHistoryPage    = 40
	agentHistoryOverlap = 4
	agentHistoryStep    = agentHistoryPage - agentHistoryOverlap
)

// agentRun is the live state of one Telegram chat's agent TUI bridge.
type agentRun struct {
	chatID  int64
	project string
	dir     string
	sess    *agent.Session
	bot     *tg.Bot

	// bin is the resolved agent CLI, kept so the session id can be asked of the
	// same program that was launched. startMs is when it was launched, in Unix
	// milliseconds, and bounds which session row belongs to this run — see
	// resolveAgentSessionID.
	bin     string
	startMs int64

	// pngMode relays screen frames as colored images; false falls back to
	// plain text code blocks. Only the relay goroutine reads/writes it.
	pngMode bool

	mu         sync.Mutex
	msgID      int
	stopped    bool
	lastPkt    time.Time
	lastTyping time.Time

	// Scrollback reader state: one message is created for the whole view and
	// repainted in place as the owner scrolls, so reading the history never
	// floods the chat or disturbs the live screen relay.
	histMsgID int
	histEnd   int
	// histHints controls the hint line drawn under the reader image; /agent
	// off drops it while leaving the image in place.
	histHints bool
}

// histBounds returns the current view position and message id together, so a
// repaint reads them as one consistent snapshot.
func (r *agentRun) histBounds() (end, msgID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.histEnd, r.histMsgID
}

func (r *agentRun) setHistEnd(end int) {
	r.mu.Lock()
	r.histEnd = end
	r.mu.Unlock()
}

func (r *agentRun) setHistMsgID(id int) {
	r.mu.Lock()
	r.histMsgID = id
	r.mu.Unlock()
}

func (r *agentRun) setHistHints(on bool) {
	r.mu.Lock()
	r.histHints = on
	r.mu.Unlock()
}

func (r *agentRun) histHintState() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.histHints
}

func (r *agentRun) notePacket() {
	r.mu.Lock()
	r.lastPkt = time.Now()
	r.mu.Unlock()
}

func (r *agentRun) resetTurn() {
	r.mu.Lock()
	r.msgID = 0
	r.mu.Unlock()
}

// claim reports whether this goroutine is the one that finalized the run.
func (r *agentRun) claim() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return false
	}
	r.stopped = true
	return true
}

// handleAgentCommand dispatches /agent <sub>.
func (h *Handler) handleAgentCommand(ctx context.Context, b *tg.Bot, chatID int64, userID int64, st *session.State, args []string) {
	if len(args) == 0 || args[0] == "status" {
		h.handleAgentStatus(ctx, b, chatID)
		return
	}
	switch args[0] {
	case "stop", "exit":
		// /agent stop and /agent exit are the same thing under two names: the
		// agent session goes away, the shell does not. Closing the reader only
		// is /agent history exit, and closing the shell is /exit.
		h.handleAgentStop(ctx, b, chatID, userID, args[0])
	case "history":
		h.handleAgentHistory(ctx, b, chatID, userID, args)
	case "up", "down", "top", "bottom", "off":
		// Scrolling is reached through slash subcommands on purpose: the bare
		// words "up" and "down" are the TUI's own arrow-key aliases, so they
		// must keep going to the agent untouched.
		h.handleAgentHistory(ctx, b, chatID, userID, append([]string{"history"}, args[0]))
	default:
		h.send(ctx, b, chatID, formatErr("unknown /agent subcommand; use `/agent stop` (`/agent exit`), `/agent status` or `/agent history`."))
	}
}

// handleAgentStart opens the interactive agent CLI in the selected project
// directory. Only the owner can start it, so the TUI (which can run arbitrary
// code) stays under the owner's sole control.
func (h *Handler) handleAgentStart(ctx context.Context, b *tg.Bot, chatID int64, userID int64, st *session.State, prompt string) {
	isOwner := h.authorizer.IsOwner(userID)
	if !isOwner {
		h.auditEvent(entryFor(chatID, userID, false, audit.ActionAccessDenied, "agent"))
		h.send(ctx, b, chatID, "🔒 Only the owner can start an agent session.")
		return
	}
	if !h.agentCfg.Enabled {
		h.send(ctx, b, chatID, formatErr("the agent feature is disabled in config (agent.enabled)."))
		return
	}
	if h.agentSessionFor(chatID) != nil {
		h.send(ctx, b, chatID, "🤖 An agent session is already running; use `/agent stop` to end it.")
		return
	}
	if st.Project == "" {
		h.send(ctx, b, chatID, formatErr("select a project first with /project <name> — agent sessions are bound to the project directory."))
		return
	}
	p, ok := h.projects[st.Project]
	if !ok {
		h.send(ctx, b, chatID, formatErr("unknown project: "+st.Project))
		return
	}
	if _, err := os.Stat(p.Path); err != nil {
		h.send(ctx, b, chatID, formatErr("project path not reachable: "+p.Path))
		return
	}
	bin, err := agent.Resolve(h.agentCfg.Command)
	if err != nil {
		h.send(ctx, b, chatID, formatAgentNotFound(h.agentCfg.Command))
		return
	}

	// startMs is recorded immediately before the launch, not after, so a session
	// created during startup still falls inside the run's lifetime.
	startMs := time.Now().UnixMilli()

	// The run is built before the PTY starts so notePacket can be handed over as
	// the onPkt callback. onPkt fires from the pump goroutine the moment the
	// process writes anything, and it is what keeps lastPkt current; without it
	// maybeTyping compares against the zero time, the condition is never true and
	// the "typing…" indicator silently never appears. notePacket only touches the
	// run's own mutex-guarded timestamp, so passing the method value is safe even
	// though sess is not assigned yet.
	run := &agentRun{
		chatID:  chatID,
		project: st.Project,
		dir:     p.Path,
		bot:     b,
		bin:     bin,
		startMs: startMs,
		pngMode: h.agentCfg.Screen.Mode != "text",
	}

	sess, err := agent.Start([]string{bin}, p.Path, h.projectEnv(st), run.notePacket)
	if err != nil {
		h.auditEvent(entryFor(chatID, userID, true, audit.ActionAgentError, prompt))
		h.send(ctx, b, chatID, formatErr("failed to start agent: "+err.Error()))
		return
	}
	run.sess = sess
	h.agentMu.Lock()
	h.agents[chatID] = run
	h.agentMu.Unlock()

	e := entryFor(chatID, userID, true, audit.ActionAgentStart, prompt)
	e.Detail = p.Path + "; pid=" + strconv.Itoa(sess.PID())
	h.auditEvent(e)

	h.send(ctx, b, chatID, formatAgentStarted(st.Project, p.Path, sess.PID()))
	go h.relay(run)

	if prompt != "" {
		// Give the TUI a moment to open its input box, then type the prompt as
		// the first user turn.
		time.Sleep(agentBootDelay)
		h.deliverAgentInput(ctx, b, chatID, userID, run, prompt)
	}
}

// handleAgentMessage writes a chat message into the running agent's TUI as
// input. Plain text is typed into the agent's input box (Enter appended);
// `^p`-style sequences and /up /do/→ helpers map to real terminal keys.
func (h *Handler) handleAgentMessage(ctx context.Context, b *tg.Bot, chatID int64, userID int64, run *agentRun, text string) {
	if !h.authorizer.IsOwner(userID) {
		h.auditEvent(entryFor(chatID, userID, false, audit.ActionAccessDenied, "agent input"))
		h.send(ctx, b, chatID, "🔒 Only the owner can write to the agent session.")
		return
	}
	h.deliverAgentInput(ctx, b, chatID, userID, run, text)
}

func (h *Handler) deliverAgentInput(ctx context.Context, b *tg.Bot, chatID int64, userID int64, run *agentRun, text string) {
	if text == "" {
		return
	}
	raw, label, _ := agent.MapInput(text)
	if err := run.sess.Write(raw); err != nil {
		// A write failure means the PTY is gone, so the run is dead whether or
		// not this goroutine wins the cleanup race. Report it either way: a
		// silent return reads as "my message vanished".
		if h.agentCleanup(chatID, run) {
			h.send(ctx, b, chatID, formatErr("agent write failed: "+err.Error()))
		} else {
			h.send(ctx, b, chatID, "🤖 The agent session closed before that message could be delivered.")
		}
		return
	}
	run.resetTurn()
	h.auditEvent(entryFor(chatID, userID, h.authorizer.IsOwner(userID), audit.ActionAgentInput, label))
}

// handleAgentStop closes the agent session. verb is the word the owner actually
// typed ("stop" or "exit") and is recorded in the audit log so the two names
// stay distinguishable after the fact.
func (h *Handler) handleAgentStop(ctx context.Context, b *tg.Bot, chatID int64, userID int64, verb string) {
	run := h.agentSessionFor(chatID)
	if run == nil {
		h.send(ctx, b, chatID, "🤖 No active agent session.")
		return
	}
	h.auditEvent(entryFor(chatID, userID, h.authorizer.IsOwner(userID), audit.ActionAgentStop, verb))
	if !h.agentCleanup(chatID, run) {
		// Another teardown already won. agentCleanup removes the run from the
		// active map before returning, so reaching here means the map was
		// refilled with a new run that we must not tear down; the loser only has
		// to report that its own run is gone.
		h.send(ctx, b, chatID, "🤖 The agent session closed on its own — nothing left to stop.")
		return
	}
	// Screen first, then close: see captureAgentExitScreen. This path used to
	// do the opposite, which is why /agent stop kept reporting a couple of stray
	// words — the capture landed after the TUI had already redrawn over the
	// session and cleared itself.
	screen := captureAgentExitScreen(run)
	_ = run.sess.Close()
	h.announceAgentExit(run, screen)
	h.send(ctx, b, chatID, "🛑 Agent session closed.")
}

func (h *Handler) handleAgentStatus(ctx context.Context, b *tg.Bot, chatID int64) {
	run := h.agentSessionFor(chatID)
	if run == nil {
		h.send(ctx, b, chatID, "🤖 No active agent session. Start one with `agent <prompt>` inside a project.")
		return
	}
	up := time.Since(run.sess.StartTime()).Round(time.Second)
	status := "alive"
	if !run.sess.IsAlive() {
		status = "exited"
	}
	h.send(ctx, b, chatID, fmt.Sprintf(
		"🤖 Agent session\n\nProject: `%s`\nWorking dir: `%s`\nPID: `%d`\nUptime: `%s`\nStatus: `%s`",
		escapeCode(run.project), escapeCode(run.dir), run.sess.PID(), up, status))
}

// handleAgentHistory opens or moves the scrollback reader. The live screen relay
// is untouched: the reader is its own message, repainted in place as the owner
// scrolls. It is driven by slash subcommands (/agent up, /agent down, …) because
// the bare words "up" and "down" belong to the TUI as arrow-key aliases.
func (h *Handler) handleAgentHistory(ctx context.Context, b *tg.Bot, chatID int64, userID int64, args []string) {
	run := h.agentSessionFor(chatID)
	if run == nil {
		h.send(ctx, b, chatID, "🤖 No active agent session, so there is no history to read.")
		return
	}
	if !h.authorizer.IsOwner(userID) {
		h.auditEvent(entryFor(chatID, userID, false, audit.ActionAccessDenied, "agent history"))
		h.send(ctx, b, chatID, "🔒 Only the owner can read the agent session history.")
		return
	}

	arg := ""
	if len(args) > 1 {
		arg = strings.ToLower(args[1])
	}
	switch arg {
	case "off", "exit":
		// The image stays in the chat for re-reading; only the hint line goes
		// away, so the reader stops nagging about scroll commands.
		run.setHistHints(false)
		if total := run.sess.ViewCount(); total > 0 {
			h.renderAgentHistory(ctx, b, chatID, userID, run, total)
		}
		h.send(ctx, b, chatID, "🗂 Hint hidden — the image above stays put. `/agent history` brings it back.")
		return
	case "up":
		h.moveAgentHistory(ctx, b, chatID, userID, run, -1)
		return
	case "down":
		h.moveAgentHistory(ctx, b, chatID, userID, run, 1)
		return
	case "top":
		h.moveAgentHistory(ctx, b, chatID, userID, run, 0)
		return
	case "bottom":
		total := run.sess.ViewCount()
		if total == 0 {
			h.send(ctx, b, chatID, "🤖 Nothing to read yet — the agent has not drawn any lines.")
			return
		}
		run.setHistEnd(total)
		h.renderAgentHistory(ctx, b, chatID, userID, run, total)
		return
	case "":
	default:
		if n, err := strconv.Atoi(arg); err == nil && n > 0 {
			h.send(ctx, b, chatID, formatErr("the reader is scrolled, not paged: use `/agent up` and `/agent down` instead of a page number."))
			return
		}
		h.send(ctx, b, chatID, formatErr("usage: `/agent history` to open, then `/agent up`, `/agent down`, `/agent top`, `/agent bottom`, `/agent off` (or `/agent history exit`) to drop the hints."))
		return
	}

	// Plain `/agent history` opens the reader at the newest line, with hints.
	run.setHistHints(true)
	total := run.sess.ViewCount()
	if total == 0 {
		h.send(ctx, b, chatID, "🤖 Nothing to read yet — the agent has not drawn any lines.")
		return
	}
	run.setHistEnd(total)
	h.renderAgentHistory(ctx, b, chatID, userID, run, total)
}

// moveAgentHistory repositions the reader. dir is -1 for one screenful of older
// lines, +1 for newer ones, and 0 to jump to the very top of the view.
func (h *Handler) moveAgentHistory(ctx context.Context, b *tg.Bot, chatID int64, userID int64, run *agentRun, dir int) {
	total := run.sess.ViewCount()
	if total == 0 {
		h.send(ctx, b, chatID, "🤖 Nothing to read yet — the agent has not drawn any lines.")
		return
	}
	end, _ := run.histBounds()
	if end == 0 {
		end = total // never positioned yet: behave as if it opened at the newest
	}
	if dir == 0 {
		end = agentHistoryTop(total)
	} else {
		end = agentHistoryViewBounds(total, end, dir)
	}
	// Scrolling is an explicit request to read, so the scroll commands bring the
	// hints back even if /agent history exit hid them.
	run.setHistHints(true)
	run.setHistEnd(end)
	h.renderAgentHistory(ctx, b, chatID, userID, run, total)
}

// agentHistoryTop is the top position of the view: a full page from the first
// line, or everything when the view is shorter than a page.
func agentHistoryTop(total int) int {
	if total > agentHistoryPage {
		return agentHistoryPage
	}
	return total
}

// agentHistoryViewBounds clamps a scroll request against the length of the
// view. The reader starts at the newest line (end == total) and moves toward
// older lines as end shrinks. Stepping keeps an overlap so consecutive views
// always overlap instead of jumping over lines.
//
// Near the top the window is allowed to become shorter than a page: the reader
// shows what is actually left above rather than refusing to move, because
// forcing a full page there used to skip every line between the shortened window
// and the page size (at 57 lines, one `/agent up` jumped from 57 straight to 40
// and hid lines 41–57).
func agentHistoryViewBounds(total, end, dir int) int {
	if total <= 0 {
		return 0
	}
	if dir < 0 {
		end -= agentHistoryStep
	} else {
		end += agentHistoryStep
	}
	if end > total {
		end = total
	}
	if end < 1 {
		end = 1
	}
	return end
}

// renderAgentHistory paints the current view, creating the reader message on
// first use and replacing it in place afterwards. The image keeps the agent's
// own colors and carries a title bar naming the visible slice, so it reads as a
// terminal window rather than a wall of text.
func (h *Handler) renderAgentHistory(ctx context.Context, b *tg.Bot, chatID int64, userID int64, run *agentRun, total int) {
	end, msgID := run.histBounds()
	rows := run.sess.ViewRows(end, agentHistoryPage)
	if len(rows) == 0 {
		return
	}
	first := end - len(rows) + 1
	where := fmt.Sprintf("%d–%d of %d", first, end, total)
	h.auditEvent(entryFor(chatID, userID, true, audit.ActionAgentHistory, where))

	hints := run.histHintState()
	caption := fmt.Sprintf("🗂 Agent history · %s", where)
	footer := ""
	if hints {
		hintLine := "/agent up older · /agent down newer · /agent top · /agent bottom · /agent off"
		caption += "\n`" + hintLine + "`"
		footer = hintLine
	}

	if h.agentCfg.Screen.Mode != "text" {
		title := fmt.Sprintf(" AGENT SCROLLBACK · %s", where)
		if img, err := agent.RenderRowsPNG(rows, title, footer); err == nil {
			h.repaintHistory(ctx, b, run, msgID, img, caption)
			return
		}
	}

	lines := rowTexts(rows)
	var bld strings.Builder
	bld.WriteString("🗂 *Agent history* · " + escapeCode(where) + "\n")
	if hints {
		bld.WriteString("`/agent up` older · `/agent down` newer · `/agent top` · `/agent bottom` · `/agent off`\n")
	}
	bld.WriteString("\n")
	bld.WriteString(formatAgentFrame(strings.Join(lines, "\n")))
	h.repaintHistoryText(ctx, b, run, msgID, bld.String())
}

// rowTexts flattens captured rows back to plain lines for the text fallback.
func rowTexts(rows []agent.Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out
}

// repaintHistory uploads the reader image, creating the message on the first
// call and swapping the photo in place on every later scroll.
// repaintHistory shows the reader image: the first render creates the message,
// every later scroll replaces its media in place so the reader stays one
// message instead of flooding the chat.
//
// It uploads a document rather than a photo, for the same reason the final
// screen does: a page of scrollback is as wide as the TUI (up to 100 columns,
// ~800px), and Telegram scales a photo down to the chat bubble, which lands the
// text at roughly 3px per cell. A document is not scaled, so it opens at native
// resolution. Keeping the single-message design costs nothing here because
// EditMessageMedia accepts a document as well as a photo.
func (h *Handler) repaintHistory(ctx context.Context, b *tg.Bot, run *agentRun, msgID int, img []byte, caption string) {
	if b == nil {
		return
	}
	if msgID == 0 {
		m, err := b.SendDocument(ctx, &tg.SendDocumentParams{
			ChatID:   run.chatID,
			Document: &models.InputFileUpload{Filename: "agent-history.png", Data: bytes.NewReader(img)},
			Caption:  caption,
		})
		if err != nil {
			return
		}
		run.setHistMsgID(m.ID)
		return
	}
	_, _ = b.EditMessageMedia(ctx, &tg.EditMessageMediaParams{
		ChatID:    run.chatID,
		MessageID: msgID,
		Media: &models.InputMediaDocument{
			Media:           "attach://agent-history.png",
			MediaAttachment: bytes.NewReader(img),
			Caption:         caption,
		},
	})
}

func (h *Handler) repaintHistoryText(ctx context.Context, b *tg.Bot, run *agentRun, msgID int, text string) {
	if b == nil {
		return
	}
	if msgID == 0 {
		m, err := b.SendMessage(ctx, &tg.SendMessageParams{ChatID: run.chatID, Text: text, ParseMode: models.ParseModeMarkdown})
		if err != nil {
			h.send(ctx, b, run.chatID, text)
			return
		}
		run.setHistMsgID(m.ID)
		return
	}
	if _, err := b.EditMessageText(ctx, &tg.EditMessageTextParams{
		ChatID:    run.chatID,
		MessageID: msgID,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	}); err != nil {
		_, _ = b.EditMessageText(ctx, &tg.EditMessageTextParams{ChatID: run.chatID, MessageID: msgID, Text: text})
	}
}

// sendAgentPhoto uploads a rendered screen image to the chat.
// relay mirrors the agent TUI screen into Telegram: one conversation message
// per user turn is live-edited while the TUI renders and stays as the final
// frame once output goes quiet.
func (h *Handler) relay(run *agentRun) {
	ticker := time.NewTicker(agentDebounce)
	defer ticker.Stop()
	for {
		select {
		case <-run.sess.Done():
			h.onAgentExit(run)
			return
		case <-ticker.C:
			// Guard each frame instead of the whole loop: a renderer fault drops
			// back to text for this run rather than killing the relay (and with it
			// the process, since nothing else recovers a background goroutine).
			if err := h.frameSafely(run); err != nil {
				h.log.Error("agent frame render failed", "err", err, "stack", string(debug.Stack()))
				run.pngMode = false
			}
		}
	}
}

// frameSafely renders one frame, converting a panic into an error so the relay
// goroutine can fall back to text instead of taking the process down.
func (h *Handler) frameSafely(run *agentRun) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("frame render panic: %v", r)
		}
	}()
	h.renderFrame(run)
	return nil
}

func (h *Handler) renderFrame(run *agentRun) {
	if run.bot == nil {
		return
	}
	if run.pngMode {
		img, changed, err := run.sess.SniffPNG()
		if err != nil {
			run.pngMode = false // renderer failed: stick to text for this run
		} else if changed {
			h.renderFramePNG(run, img)
			return
		} else {
			return
		}
	}
	frame, changed := run.sess.Sniff()
	if !changed {
		return
	}
	h.maybeTyping(run)
	text := formatAgentFrame(frame)

	run.mu.Lock()
	if run.stopped {
		run.mu.Unlock()
		return
	}
	msgID := run.msgID
	run.mu.Unlock()

	if msgID == 0 {
		m, err := run.bot.SendMessage(context.Background(), &tg.SendMessageParams{ChatID: run.chatID, Text: text, ParseMode: models.ParseModeMarkdown})
		if err != nil {
			_, _ = run.bot.SendMessage(context.Background(), &tg.SendMessageParams{ChatID: run.chatID, Text: text})
			return
		}
		run.mu.Lock()
		run.msgID = m.ID
		run.mu.Unlock()
		return
	}

	_, editErr := run.bot.EditMessageText(context.Background(), &tg.EditMessageTextParams{
		ChatID:    run.chatID,
		MessageID: msgID,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	})
	if editErr != nil {
		// Markdown incompatibility (or the message is gone): resend plain.
		_, _ = run.bot.EditMessageText(context.Background(), &tg.EditMessageTextParams{ChatID: run.chatID, MessageID: msgID, Text: text})
	}
}

// renderFramePNG sends a screen frame as a photo: the first frame creates the
// message, later frames replace the photo in place via edit, so a triggered
// TUI stays as one live message instead of spamming the chat.
func (h *Handler) renderFramePNG(run *agentRun, img []byte) {
	h.maybeTyping(run)
	upload := &models.InputFileUpload{Filename: "agent.png", Data: bytes.NewReader(img)}

	run.mu.Lock()
	if run.stopped {
		run.mu.Unlock()
		return
	}
	msgID := run.msgID
	run.mu.Unlock()

	if msgID == 0 {
		m, err := run.bot.SendPhoto(context.Background(), &tg.SendPhotoParams{ChatID: run.chatID, Photo: upload, Caption: "🤖 Agent screen"})
		if err != nil {
			// Photo send failed; the session must stay visible, so drop to text.
			run.pngMode = false
			return
		}
		run.mu.Lock()
		run.msgID = m.ID
		run.mu.Unlock()
		return
	}

	_, _ = run.bot.EditMessageMedia(context.Background(), &tg.EditMessageMediaParams{
		ChatID:    run.chatID,
		MessageID: msgID,
		Media: &models.InputMediaPhoto{
			Media:           "attach://agent.png",
			MediaAttachment: bytes.NewReader(img),
		},
	})
}

// maybeTyping keeps the "typing…" indicator alive while the screen is actively
// redrawing, so the relay feels like a live terminal.
func (h *Handler) maybeTyping(run *agentRun) {
	now := time.Now()
	// Both stamps are read under the mutex. lastPkt in particular is written
	// from the PTY pump goroutine (onPkt -> notePacket), so reading it bare here
	// is a data race the moment the agent actually renders.
	run.mu.Lock()
	lastPkt, lastTyping := run.lastPkt, run.lastTyping
	run.mu.Unlock()
	if now.Sub(lastPkt) < agentDebounce*4 && now.Sub(lastTyping) > agentTypingEvery {
		_, _ = run.bot.SendChatAction(context.Background(), &tg.SendChatActionParams{ChatID: run.chatID, Action: models.ChatActionTyping})
		run.mu.Lock()
		run.lastTyping = now
		run.mu.Unlock()
	}
}

// onAgentExit is the relay's teardown path. It removes the run from the active
// map and announces a natural exit exactly once (the /agent stop path already
// announced its own closure).
func (h *Handler) onAgentExit(run *agentRun) {
	// Losing this race is not a silent failure: the winner is always either
	// onAgentExit, /agent stop or /exit, and all of those tell the owner what
	// happened. Staying quiet here is what keeps a deliberate /agent stop from
	// producing two messages for one closure.
	if !h.agentCleanup(run.chatID, run) {
		return
	}
	// The relay is the only caller whose session has already ended, so there is
	// nothing left to close: the screen is read from the frozen terminal, which is
	// still the session because the process exited without a redraw.
	h.announceAgentExit(run, captureAgentExitScreen(run))
}

// captureAgentExitScreen snapshots the agent's screen as text before the session
// is torn down.
//
// This has to happen before Close. Close sends Ctrl-C and SIGINT, and a TUI
// answers that by redrawing and clearing, so a screen read afterwards is a
// transcript of the shutdown. Reading first costs nothing — the process is alive
// to be interrupted either way.
//
// Text, not an image: the owner asked for what was on the shell to arrive as
// written output. A rendered PNG of the same screen was neither copyable nor
// searchable, and the last live relay photo already covers the "what did it look
// like" case while the session is running.
func captureAgentExitScreen(run *agentRun) string {
	return run.sess.Frame()
}

// announceAgentExit posts the agent's final screen as text. It is separate from
// the teardown because more than one caller wins a closure: the relay (natural
// exit), /agent stop, and /exit. Keeping the announcement in one place is what
// stops a single closure from being reported two different ways.
//
// The screen is passed in rather than read here because it must be captured
// before the session is closed — see captureAgentExitScreen.
func (h *Handler) announceAgentExit(run *agentRun, screen string) {
	var bld strings.Builder
	bld.WriteString("✖ Agent exited.")
	if strings.TrimSpace(screen) != "" {
		bld.WriteString(" Last screen:\n")
		bld.WriteString(formatAgentFrame(screen))
	}
	h.send(context.Background(), run.bot, run.chatID, bld.String())
	// The session id is resolved after the text has gone out, so a slow or
	// missing `session list` can never hold up the output the owner asked for.
	go h.appendAgentSessionID(run)
}

// agentCleanup removes the run from the active map and reports whether this
// call actually finalised it (so only one goroutine announces the exit).
func (h *Handler) agentCleanup(chatID int64, run *agentRun) bool {
	h.agentMu.Lock()
	if h.agents[chatID] == run {
		delete(h.agents, chatID)
	}
	h.agentMu.Unlock()
	return run.claim()
}

func (h *Handler) agentSessionFor(chatID int64) *agentRun {
	h.agentMu.Lock()
	defer h.agentMu.Unlock()
	return h.agents[chatID]
}

// formatAgentFrame renders a screen frame inside a markdown code fence that is
// trimmed, escape-safe and capped under the Telegram message limit.
func formatAgentFrame(frame string) string {
	frame = strings.TrimRight(frame, "\n")
	frame = strings.ReplaceAll(frame, "```", "'''")
	if len(frame) > agentFrameCap {
		frame = string(terminal.TruncateOutput([]byte(frame), agentFrameCap))
		frame = strings.TrimRight(frame, "\n")
	}
	return "```\n" + frame + "\n```"
}

func formatAgentStarted(project, dir string, pid int) string {
	return fmt.Sprintf(
		"🤖 Agent started.\n\nProject: `%s`\nWorking dir: `%s`\nPID: `%d`\n\nSend messages as prompts. Control keys: `^p` palette, arrows `↑↓←→` or `/up` etc., `/esc`. End with `/agent stop`.",
		escapeCode(project), escapeCode(dir), pid)
}

func formatAgentNotFound(command string) string {
	if command != "" {
		return fmt.Sprintf("🤖 The configured agent command `%s` was not found. Check `agent.command` in config.yaml.", escapeCode(command))
	}
	return "🤖 No coding agent CLI found. Install one of opencode, claude, codex or gemini on the machine, or point the bot at a binary with `agent.command` in config.yaml."
}
