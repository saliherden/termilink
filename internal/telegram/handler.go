package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/saliherden/termilink/internal/config"
	"github.com/saliherden/termilink/internal/security"
	"github.com/saliherden/termilink/internal/session"
	"github.com/saliherden/termilink/internal/terminal"
)

const (
	defaultMaxMsgLen  = 3500
	msgMaxLength      = 4096
	defaultCmdTimeout = 30 * time.Minute
	approvalTTL       = 2 * time.Minute
)

type pendingApproval struct {
	userID  int64
	raw     string
	expires time.Time
}

type Handler struct {
	authorizer *security.Authorizer
	policy     *security.Policy
	runner     *terminal.Runner
	sessions   *session.Manager
	projects   map[string]config.ProjectConfig
	maxMsgLen  int
	timeout    time.Duration
	log        *slog.Logger

	shellMu sync.Mutex
	shells  map[string]*terminal.Shell

	pendingMu sync.Mutex
	pending   map[int64]pendingApproval
}

type Options struct {
	Authorizer *security.Authorizer
	Policy     *security.Policy
	Runner     *terminal.Runner
	Sessions   *session.Manager
	Projects   map[string]config.ProjectConfig
	MaxMsgLen  int
	Timeout    time.Duration
	Logger     *slog.Logger
}

func NewHandler(opts Options) *Handler {
	h := &Handler{
		authorizer: opts.Authorizer,
		policy:     opts.Policy,
		runner:     opts.Runner,
		sessions:   opts.Sessions,
		projects:   opts.Projects,
		maxMsgLen:  opts.MaxMsgLen,
		timeout:    opts.Timeout,
		log:        opts.Logger,
		shells:     map[string]*terminal.Shell{},
		pending:    map[int64]pendingApproval{},
	}
	if h.maxMsgLen <= 0 || h.maxMsgLen > msgMaxLength {
		h.maxMsgLen = defaultMaxMsgLen
	}
	if h.timeout <= 0 {
		h.timeout = defaultCmdTimeout
	}
	if h.sessions == nil {
		h.sessions = session.NewManager()
	}
	if h.log == nil {
		h.log = slog.Default()
	}
	return h
}

func (h *Handler) commandTimeout() time.Duration {
	if h.timeout > 0 {
		return h.timeout
	}
	return defaultCmdTimeout
}

func (h *Handler) Callback() tg.HandlerFunc {
	return func(ctx context.Context, b *tg.Bot, update *models.Update) {
		if update.Message == nil || update.Message.From == nil {
			return
		}
		msg := update.Message
		if !h.authorizer.IsAllowed(msg.From.ID) {
			h.log.Warn("rejected unauthorized message", "user_id", msg.From.ID, "chat_id", msg.Chat.ID)
			return
		}
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			return
		}
		h.handle(ctx, b, msg.Chat.ID, msg.From.ID, text)
	}
}

func (h *Handler) handle(ctx context.Context, b *tg.Bot, chatID int64, userID int64, text string) {
	st := h.sessions.Ensure(strconv.FormatInt(chatID, 10))

	if strings.HasPrefix(text, "/") {
		h.handleCommand(ctx, b, chatID, userID, st, text)
		return
	}
	if h.pendingApprovalFor(chatID) != nil {
		h.resolveApproval(ctx, b, chatID, userID, st, text)
		return
	}
	h.maybeApproveAndRun(ctx, b, chatID, userID, st, text)
}

func (h *Handler) maybeApproveAndRun(ctx context.Context, b *tg.Bot, chatID int64, userID int64, st *session.State, text string) {
	if h.policy != nil && h.policy.NeedsApproval(h.authorizer.IsOwner(userID), text) {
		h.pendingMu.Lock()
		h.pending[chatID] = pendingApproval{userID: userID, raw: text, expires: time.Now().Add(approvalTTL)}
		h.pendingMu.Unlock()
		h.send(ctx, b, chatID, formatApprovalPrompt(text, approvalTTL))
		return
	}
	h.runCommand(ctx, b, chatID, userID, st, text)
}

func (h *Handler) pendingApprovalFor(chatID int64) *pendingApproval {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	req, ok := h.pending[chatID]
	if !ok {
		return nil
	}
	if time.Now().After(req.expires) {
		delete(h.pending, chatID)
		return nil
	}
	return &req
}

func (h *Handler) resolveApproval(ctx context.Context, b *tg.Bot, chatID int64, userID int64, st *session.State, text string) {
	h.pendingMu.Lock()
	req, ok := h.pending[chatID]
	if ok {
		delete(h.pending, chatID)
	}
	h.pendingMu.Unlock()
	if !ok || time.Now().After(req.expires) {
		h.send(ctx, b, chatID, formatApprovalTimeout())
		return
	}
	if !h.authorizer.IsOwner(userID) {
		h.pendingMu.Lock()
		h.pending[chatID] = req
		h.pendingMu.Unlock()
		h.send(ctx, b, chatID, "🔒 Only the owner can approve or reject a dangerous command.")
		return
	}
	switch approvalVerdict(text) {
	case answerApprove:
		h.runCommand(ctx, b, chatID, req.userID, st, req.raw)
	case answerDeny:
		h.sendPlain(ctx, b, chatID, "❌ Rejected, the command was not executed.")
	default:
		h.pendingMu.Lock()
		h.pending[chatID] = req
		h.pendingMu.Unlock()
		h.send(ctx, b, chatID, formatApprovalStillPending(req.raw))
	}
}

type approvalAnswer int

const (
	answerUnknown approvalAnswer = iota
	answerApprove
	answerDeny
)

func approvalVerdict(text string) approvalAnswer {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "evet", "yes", "ok", "onay", "onayla":
		return answerApprove
	case "hayir", "hayır", "no", "cancel", "iptal", "reddet":
		return answerDeny
	}
	return answerUnknown
}

func (h *Handler) handleCommand(ctx context.Context, b *tg.Bot, chatID int64, userID int64, st *session.State, text string) {
	fields := strings.Fields(text)
	name := strings.TrimPrefix(fields[0], "/")
	if i := strings.Index(name, "@"); i >= 0 {
		name = name[:i]
	}
	args := fields[1:]

	switch name {
	case "start":
		h.send(ctx, b, chatID, welcomeMsg)
	case "help":
		h.send(ctx, b, chatID, helpMsg)
	case "ping":
		h.sendPlain(ctx, b, chatID, pingOK)
	case "projects":
		h.send(ctx, b, chatID, formatProjects(h.projectNames()))
	case "project":
		h.handleProject(ctx, b, chatID, userID, st, args)
	case "status":
		h.send(ctx, b, chatID, h.formatSessionStatus(st))
	case "sessions":
		h.send(ctx, b, chatID, formatSessions(h.sessionRows()))
	case "input":
		h.handleInput(ctx, b, chatID, st, args)
	case "stop":
		h.handleStop(ctx, b, chatID, st)
	case "exit":
		h.handleExit(ctx, b, chatID, st)
	default:
		h.send(ctx, b, chatID, unknown)
	}
}

func (h *Handler) handleProject(ctx context.Context, b *tg.Bot, chatID int64, userID int64, st *session.State, args []string) {
	if len(args) == 0 {
		h.send(ctx, b, chatID, formatProjects(h.projectNames()))
		return
	}
	name := args[0]
	p, ok := h.projects[name]
	if !ok {
		h.send(ctx, b, chatID, formatErr("unknown project: "+name))
		return
	}
	if _, err := os.Stat(p.Path); err != nil {
		h.send(ctx, b, chatID, formatErr("project path not reachable: "+p.Path))
		return
	}
	wks := h.policy != nil && !h.policy.WorkspaceEmpty()
	if wks && !h.authorizer.IsOwner(userID) && !h.policy.InWorkspace(p.Path) {
		h.send(ctx, b, chatID, formatErr("project path is outside the allowed workspace: "+p.Path))
		return
	}
	h.closeShellFor(st.ID)
	st.Cwd = p.Path
	st.Project = name
	h.sessions.Save()

	cmdNames := make([]string, 0, len(p.Commands))
	for k := range p.Commands {
		cmdNames = append(cmdNames, k)
	}
	h.send(ctx, b, chatID, formatProjectHome(name, p.Path, cmdNames))
}

func (h *Handler) handleInput(ctx context.Context, b *tg.Bot, chatID int64, st *session.State, args []string) {
	shell := h.shellFor(st.ID)
	if shell == nil || !shell.IsAlive() {
		h.send(ctx, b, chatID, formatErr("no active shell; send a command first."))
		return
	}
	text := strings.Join(args, " ")
	trimmed := strings.TrimSpace(text)
	var data string
	switch strings.ToLower(trimmed) {
	case "ctrl-c", "^c", "int", "sigint":
		data = "\x03"
	case "ctrl-d", "^d", "eof":
		data = "\x04"
	case "enter":
		data = "\n"
	default:
		if strings.HasPrefix(trimmed, "raw:") {
			data = strings.TrimPrefix(text, "raw:")
		} else {
			data = text + "\n"
		}
	}
	if err := shell.Write([]byte(data)); err != nil {
		h.send(ctx, b, chatID, formatErr("failed to write to shell: "+err.Error()))
		return
	}
	h.sendPlain(ctx, b, chatID, "📥 Input sent.")
}

func (h *Handler) handleStop(ctx context.Context, b *tg.Bot, chatID int64, st *session.State) {
	shell := h.shellFor(st.ID)
	if !st.Active || shell == nil || !shell.IsAlive() {
		h.sendPlain(ctx, b, chatID, "⏹ No command is running.")
		return
	}
	_ = shell.Stop()
}

func (h *Handler) handleExit(ctx context.Context, b *tg.Bot, chatID int64, st *session.State) {
	h.closeShellFor(st.ID)
	st.Active = false
	h.sessions.Save()
	h.sendPlain(ctx, b, chatID, "🗑 Shell closed.")
}

func (h *Handler) formatSessionStatus(st *session.State) string {
	var extra strings.Builder
	if shell := h.shellFor(st.ID); shell != nil && shell.IsAlive() {
		fmt.Fprintf(&extra, "\n\nShell:\n`pid %d, up %s`", shell.PID(), shell.Age().Round(time.Second))
		tail := shell.Tail(2048)
		cleaned := terminal.CleanShellOutput([]byte(tail))
		cleaned = terminal.TruncateOutput(cleaned, 1200)
		if strings.TrimSpace(string(cleaned)) != "" {
			fmt.Fprintf(&extra, "\n\nLast output tail:\n```\n%s\n```", sanitizeCodeBlock(string(cleaned)))
		}
	} else {
		extra.WriteString("\n\nShell: _(closed)_. Send a command to start one.")
	}
	return formatStatus(st.Project, h.effectiveCwd(st), st.LastCmd) + extra.String()
}

func (h *Handler) runCommand(ctx context.Context, b *tg.Bot, chatID int64, userID int64, st *session.State, raw string) {
	if st.Active {
		h.send(ctx, b, chatID, formatErr("a command is already running in this session; use /stop to interrupt it."))
		return
	}
	if reason, ok := h.authorizeCommand(st, h.authorizer.IsOwner(userID), raw); !ok {
		h.send(ctx, b, chatID, formatErr(reason))
		return
	}

	command, isCd := h.resolve(st, raw)
	shell, err := h.getShell(st)
	if err != nil {
		h.send(ctx, b, chatID, formatErr("failed to open shell: "+err.Error()))
		return
	}

	runCtx, cancel := context.WithTimeout(ctx, h.commandTimeout())
	defer cancel()

	st.Active = true
	st.LastCmd = raw
	h.sessions.Save()
	defer func() {
		st.Active = false
		h.sessions.Save()
	}()

	res, err := shell.ExecCommand(runCtx, command)

	if pwd := terminal.ParsePWD(res); pwd != "" {
		st.Cwd = pwd
		st.Project = h.projectNameByPath(pwd)
		h.sessions.Save()
	}

	display := terminal.TruncateOutput(terminal.CleanShellOutput(res), h.maxMsgLen)

	switch {
	case err == nil:
	case errors.Is(err, context.DeadlineExceeded):
		h.send(ctx, b, chatID, formatTimeout(command, string(display), h.commandTimeout()))
		return
	case errors.Is(err, terminal.ErrInterrupted):
		h.send(ctx, b, chatID, formatInterrupted(command, string(display)))
		return
	default:
		h.log.Warn("shell command failed", "chat_id", chatID, "command", raw, "err", err)
		h.send(ctx, b, chatID, formatErr("command failed: "+err.Error()+"\n"+string(display)))
		return
	}

	if isCd {
		wd := h.effectiveCwd(st)
		h.send(ctx, b, chatID, "📁 Working directory:\n`"+escapeCode(wd)+"`")
		return
	}

	_, _ = b.SendChatAction(ctx, &tg.SendChatActionParams{ChatID: chatID, Action: models.ChatActionTyping})
	h.send(ctx, b, chatID, formatRun(raw, terminal.Result{
		Output:   display,
		ExitCode: 0,
	}))
}

func (h *Handler) authorizeCommand(st *session.State, isOwner bool, raw string) (string, bool) {
	if isOwner {
		return "", true
	}
	if isCdCommand(raw) {
		return "only the owner can change directories; use /project <name> to switch projects.", false
	}
	if st.Project == "" {
		return "select a project first with /project <name>.", false
	}
	if reason, blocked := h.workspaceVet(raw); blocked {
		return reason, false
	}
	return "", true
}

// workspaceVet is a policy-level (not an OS sandbox) guard for workers: when
// workspace.allowed roots are configured, obvious out-of-scope absolute / ~ /
// $HOME paths are rejected. Relative paths and shell expansion can still
// escape; this is best-effort and documented as such.
func (h *Handler) workspaceVet(raw string) (string, bool) {
	p := h.policy
	if p == nil || p.WorkspaceEmpty() {
		return "", false
	}
	for _, tok := range strings.Fields(raw) {
		tok = strings.Trim(tok, `"'`)
		tok = strings.TrimRight(tok, ",;()|&<>")
		if tok == ".." || strings.HasPrefix(tok, "../") {
			return "⛔ workspace: `..` escapes are not allowed for workers.", true
		}
		path, ok := expandPathCandidate(tok)
		if !ok {
			continue
		}
		if p.InWorkspace(path) {
			continue
		}
		return "⛔ workspace: `" + escapeCode(tok) + "` is outside the allowed workspace.", true
	}
	return "", false
}

func expandPathCandidate(tok string) (string, bool) {
	switch {
	case strings.HasPrefix(tok, "~"):
		home, err := os.UserHomeDir()
		if err != nil {
			home = "$HOME"
		}
		return filepath.Clean(home + strings.TrimPrefix(tok, "~")), true
	case strings.HasPrefix(tok, "$HOME/"):
		home, err := os.UserHomeDir()
		if err != nil {
			home = "$HOME"
		}
		return filepath.Clean(home + strings.TrimPrefix(tok, "$HOME")), true
	case strings.HasPrefix(tok, "/"):
		return filepath.Clean(tok), true
	}
	return "", false
}

func isCdCommand(raw string) bool {
	return raw == "cd" || strings.HasPrefix(raw, "cd ")
}

func (h *Handler) effectiveCwd(st *session.State) string {
	if st.Cwd != "" {
		return st.Cwd
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

func (h *Handler) getShell(st *session.State) (*terminal.Shell, error) {
	h.shellMu.Lock()
	defer h.shellMu.Unlock()
	if s, ok := h.shells[st.ID]; ok {
		if s.IsAlive() {
			return s, nil
		}
		delete(h.shells, st.ID)
	}
	var env []string
	if st.Project != "" {
		env = append(env, "TERMILINK_PROJECT="+st.Project)
	}
	if h.runner == nil {
		return nil, errors.New("no terminal runner configured")
	}
	s, err := h.runner.OpenShell(st.Cwd, env)
	if err != nil {
		return nil, err
	}
	h.shells[st.ID] = s
	return s, nil
}

func (h *Handler) shellFor(id string) *terminal.Shell {
	h.shellMu.Lock()
	defer h.shellMu.Unlock()
	return h.shells[id]
}

func (h *Handler) closeShellFor(id string) {
	h.shellMu.Lock()
	s := h.shells[id]
	delete(h.shells, id)
	h.shellMu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

func (h *Handler) Close() {
	h.shellMu.Lock()
	shells := h.shells
	h.shells = map[string]*terminal.Shell{}
	h.shellMu.Unlock()
	for _, s := range shells {
		_ = s.Close()
	}
}

func (h *Handler) resolve(st *session.State, raw string) (string, bool) {
	if isCdCommand(raw) {
		target := strings.TrimSpace(strings.TrimPrefix(raw, "cd"))
		if target == "" {
			target = "$HOME"
		}
		return "cd " + target + " && pwd", true
	}
	if proj := h.projectByPath(st.Cwd); proj != nil {
		if cmd, ok := proj.Commands[raw]; ok {
			return cmd, false
		}
	}
	return raw, false
}

func (h *Handler) projectByPath(cwd string) *config.ProjectConfig {
	if cwd == "" {
		return nil
	}
	for _, p := range h.projects {
		if filepath.Clean(p.Path) == filepath.Clean(cwd) {
			cp := p
			return &cp
		}
	}
	return nil
}

func (h *Handler) projectNameByPath(cwd string) string {
	if cwd == "" {
		return ""
	}
	for name, p := range h.projects {
		if filepath.Clean(p.Path) == filepath.Clean(cwd) {
			return name
		}
	}
	return ""
}

func (h *Handler) projectNames() []string {
	names := make([]string, 0, len(h.projects))
	for n := range h.projects {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (h *Handler) sessionRows() []string {
	rows := []string{}
	for _, s := range h.sessions.List() {
		cwd := s.Cwd
		if cwd == "" {
			cwd = noCwd
		}
		last := s.LastCmd
		if last == "" {
			last = noCmd
		}
		rows = append(rows, fmt.Sprintf("• %s\n  cwd: `%s`\n  last: `%s`", s.ID, escapeCode(cwd), escapeCode(last)))
	}
	return rows
}

func (h *Handler) send(ctx context.Context, b *tg.Bot, chatID int64, text string) {
	if len(text) > msgMaxLength {
		text = string(terminal.TruncateOutput([]byte(text), msgMaxLength))
	}
	_, err := b.SendMessage(ctx, &tg.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	})
	if err == nil {
		return
	}
	_, _ = b.SendMessage(ctx, &tg.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	})
}

func (h *Handler) sendPlain(ctx context.Context, b *tg.Bot, chatID int64, text string) {
	_, _ = b.SendMessage(ctx, &tg.SendMessageParams{ChatID: chatID, Text: text})
}
