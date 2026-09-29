package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/saliherden/termilink/internal/audit"
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
	authorizer   *security.Authorizer
	policy       *security.Policy
	runner       *terminal.Runner
	sessions     *session.Manager
	projects     map[string]config.ProjectConfig
	maxMsgLen    int
	timeout      time.Duration
	maxFileBytes int64
	bigFileLink  string
	agentCfg     config.AgentConfig
	log          *slog.Logger
	audit        *audit.Logger

	shellMu sync.Mutex
	shells  map[string]*terminal.Shell

	pendingMu sync.Mutex
	pending   map[int64]pendingApproval

	filePendingMu sync.Mutex
	filesPending  map[int64]pendingFile

	agentMu sync.Mutex
	agents  map[int64]*agentRun
}

type Options struct {
	Authorizer   *security.Authorizer
	Policy       *security.Policy
	Runner       *terminal.Runner
	Sessions     *session.Manager
	Projects     map[string]config.ProjectConfig
	MaxMsgLen    int
	Timeout      time.Duration
	MaxFileBytes int64
	BigFileLink  string
	Agent        config.AgentConfig
	Logger       *slog.Logger
	Audit        *audit.Logger
}

// pendingFile is a chat-scoped approval request to deliver a file that exceeds
// Telegram's limit through a temporary anonymous link. path holds the temporary
// .tar archive that will be uploaded; display is the original file name.
type pendingFile struct {
	userID  int64
	path    string
	display string
	size    int64
	chatID  int64
	host    string
	expires time.Time
}

func NewHandler(opts Options) *Handler {
	h := &Handler{
		authorizer:   opts.Authorizer,
		policy:       opts.Policy,
		runner:       opts.Runner,
		sessions:     opts.Sessions,
		projects:     opts.Projects,
		maxMsgLen:    opts.MaxMsgLen,
		timeout:      opts.Timeout,
		maxFileBytes: opts.MaxFileBytes,
		bigFileLink:  opts.BigFileLink,
		log:          opts.Logger,
		audit:        opts.Audit,
		agentCfg:     opts.Agent,
		shells:       map[string]*terminal.Shell{},
		pending:      map[int64]pendingApproval{},
		filesPending: map[int64]pendingFile{},
		agents:       map[int64]*agentRun{},
	}
	if h.maxMsgLen <= 0 || h.maxMsgLen > msgMaxLength {
		h.maxMsgLen = defaultMaxMsgLen
	}
	if h.timeout <= 0 {
		h.timeout = defaultCmdTimeout
	}
	if h.maxFileBytes <= 0 {
		h.maxFileBytes = defaultMaxFileBytes
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

// auditEvent is a nil-safe wrapper around the audit logger.
func (h *Handler) auditEvent(e audit.Entry) {
	if h.audit == nil {
		return
	}
	h.audit.Audit(e)
}

// entryFor builds a redacted audit entry from chat/user context.
func entryFor(chatID, userID int64, owner bool, action, raw string) audit.Entry {
	return audit.Entry{
		Time:   time.Now(),
		ChatID: chatID,
		UserID: userID,
		Owner:  owner,
		Action: action,
		Cmd:    audit.Redact(raw),
	}
}

// auditActionForDenial maps a denial reason to its audit action.
func auditActionForDenial(reason string) string {
	if strings.HasPrefix(reason, "⛔ workspace:") {
		return audit.ActionVetBlocked
	}
	return audit.ActionAccessDenied
}

// rejectUnauthorized records a whitelist rejection in the log and audit trail.
func (h *Handler) rejectUnauthorized(userID, chatID int64, text string) {
	h.log.Warn("rejected unauthorized message", "user_id", userID, "chat_id", chatID)
	h.auditEvent(entryFor(chatID, userID, false, audit.ActionAccessDenied, text))
}

func (h *Handler) Callback() tg.HandlerFunc {
	return func(ctx context.Context, b *tg.Bot, update *models.Update) {
		// The bot library dispatches each update on its own goroutine, so a panic
		// anywhere below takes the whole gateway down and every later command goes
		// unanswered. Contain it here — and register it before touching any field
		// of the update, so even a malformed one that panics while being read is
		// still contained.
		var chatID, userID int64
		defer h.recoverUpdate(ctx, b, &chatID, &userID)()

		// A message without a sender carries nothing to act on. models.Chat is a
		// value, not a pointer, so it is always safe to read.
		if update.Message == nil || update.Message.From == nil {
			return
		}
		chatID, userID = update.Message.Chat.ID, update.Message.From.ID
		msg := update.Message
		if !h.authorizer.IsAllowed(msg.From.ID) {
			h.rejectUnauthorized(msg.From.ID, msg.Chat.ID, msg.Text)
			return
		}
		if msg.Document != nil {
			h.handleDocument(ctx, b, msg.Chat.ID, msg.From.ID, msg.Document)
			return
		}
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			return
		}
		h.handle(ctx, b, msg.Chat.ID, msg.From.ID, text)
	}
}

// recoverUpdate returns a deferred function that turns a panic in update
// handling into a recorded, non-fatal event. Without it a single fault stops the
// whole gateway: the Telegram library runs each update on its own goroutine and
// nothing else would recover, so the bot would simply stop answering while
// looking alive. chatID and userID are pointers because they are filled in as
// soon as the update is known to be well formed, and the panic may happen later.
func (h *Handler) recoverUpdate(ctx context.Context, b *tg.Bot, chatID, userID *int64) func() {
	return func() {
		r := recover()
		if r == nil {
			return
		}
		h.log.Error("panic handling update", "panic", r, "stack", string(debug.Stack()))
		h.auditEvent(entryFor(*chatID, *userID, false, audit.ActionPanic, fmt.Sprint(r)))
		if *chatID != 0 {
			h.send(ctx, b, *chatID, "⚠️ That command hit an error. The bot is still running — the details are in the log.")
		}
	}
}

func (h *Handler) handle(ctx context.Context, b *tg.Bot, chatID int64, userID int64, text string) {
	st := h.sessions.Ensure(strconv.FormatInt(chatID, 10))

	if strings.HasPrefix(text, "/") {
		h.handleCommand(ctx, b, chatID, userID, st, text)
		return
	}
	if fields := strings.Fields(text); len(fields) > 0 && fields[0] == "get" {
		h.handleGet(ctx, b, chatID, userID, st, fields[1:])
		return
	}
	if run := h.agentSessionFor(chatID); run != nil {
		intake := strings.TrimSpace(strings.TrimPrefix(text, "agent"))
		h.handleAgentMessage(ctx, b, chatID, userID, run, intake)
		return
	}
	if fields := strings.Fields(text); len(fields) > 0 && fields[0] == "agent" {
		prompt := strings.TrimSpace(strings.TrimPrefix(text, "agent"))
		h.handleAgentStart(ctx, b, chatID, userID, st, prompt)
		return
	}
	if h.pendingApprovalFor(chatID) != nil {
		h.resolveApproval(ctx, b, chatID, userID, st, text)
		return
	}
	if h.filesPendingFor(chatID) != nil {
		h.resolveFileApproval(ctx, b, chatID, userID, text)
		return
	}
	h.maybeApproveAndRun(ctx, b, chatID, userID, st, text)
}

func (h *Handler) maybeApproveAndRun(ctx context.Context, b *tg.Bot, chatID int64, userID int64, st *session.State, text string) {
	if h.policy != nil && h.policy.NeedsApproval(h.authorizer.IsOwner(userID), text) {
		h.pendingMu.Lock()
		h.pending[chatID] = pendingApproval{userID: userID, raw: text, expires: time.Now().Add(approvalTTL)}
		h.pendingMu.Unlock()
		h.auditEvent(entryFor(chatID, userID, h.authorizer.IsOwner(userID), audit.ActionApprovalReq, text))
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
		h.auditEvent(entryFor(chatID, userID, h.authorizer.IsOwner(userID), audit.ActionApprovalTimed, req.raw))
		h.send(ctx, b, chatID, formatApprovalTimeout())
		return
	}
	if !h.authorizer.IsOwner(userID) {
		h.pendingMu.Lock()
		h.pending[chatID] = req
		h.pendingMu.Unlock()
		h.auditEvent(entryFor(chatID, userID, false, audit.ActionApprovalBlock, req.raw))
		h.send(ctx, b, chatID, "🔒 Only the owner can approve or reject a dangerous command.")
		return
	}
	switch approvalVerdict(text) {
	case answerApprove:
		h.auditEvent(entryFor(chatID, userID, true, audit.ActionApprovalOK, req.raw))
		h.runCommand(ctx, b, chatID, req.userID, st, req.raw)
	case answerDeny:
		h.auditEvent(entryFor(chatID, userID, true, audit.ActionApprovalNo, req.raw))
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
	case "get":
		h.handleGet(ctx, b, chatID, userID, st, args)
	case "status":
		h.send(ctx, b, chatID, h.formatSessionStatus(st))
	case "sessions":
		h.send(ctx, b, chatID, formatSessions(h.sessionRows()))
	case "input":
		h.handleInput(ctx, b, chatID, userID, st, args)
	case "stop":
		h.handleStop(ctx, b, chatID, userID, st)
	case "exit":
		h.handleExit(ctx, b, chatID, userID, st)
	case "agent":
		h.handleAgentCommand(ctx, b, chatID, userID, st, args)
	default:
		// A leading slash alone does not make something a command. Known
		// commands above always win, but anything the bot does not recognize
		// is far more likely text the user wants typed into the agent — a path
		// like /update.sh, a flag, a typo — and answering "Unknown command"
		// both swallows what they wrote and hides it. With an agent running we
		// pass the message through untouched; MapInput still turns /up, /esc
		// and friends into real key bytes. Without one, nothing changes.
		if run := h.agentSessionFor(chatID); run != nil {
			h.handleAgentMessage(ctx, b, chatID, userID, run, text)
			return
		}
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

	e := entryFor(chatID, userID, h.authorizer.IsOwner(userID), audit.ActionProjectSwitch, name)
	e.Detail = p.Path
	h.auditEvent(e)

	cmdNames := make([]string, 0, len(p.Commands))
	for k := range p.Commands {
		cmdNames = append(cmdNames, k)
	}
	h.send(ctx, b, chatID, formatProjectHome(name, p.Path, cmdNames))
}

func (h *Handler) handleInput(ctx context.Context, b *tg.Bot, chatID int64, userID int64, st *session.State, args []string) {
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
	h.auditEvent(entryFor(chatID, userID, false, audit.ActionInput, text))
	h.sendPlain(ctx, b, chatID, "📥 Input sent.")
}

func (h *Handler) handleStop(ctx context.Context, b *tg.Bot, chatID int64, userID int64, st *session.State) {
	shell := h.shellFor(st.ID)
	if !st.Active || shell == nil || !shell.IsAlive() {
		h.sendPlain(ctx, b, chatID, "⏹ No command is running.")
		return
	}
	h.auditEvent(entryFor(chatID, userID, h.authorizer.IsOwner(userID), audit.ActionStop, ""))
	_ = shell.Stop()
}

// handleExit is the "close everything" command. With an agent running it must
// also tear the agent down, because the agent holds its own PTY that /exit
// cannot reach on its own.
func (h *Handler) handleExit(ctx context.Context, b *tg.Bot, chatID int64, userID int64, st *session.State) {
	// Agent first. Its session holds work the owner would lose silently, and its
	// final screen has to be captured while the TUI is still alive; closing the
	// shell first interrupts the machine underneath it and the last screen comes
	// out blank.
	if run := h.agentSessionFor(chatID); run != nil {
		isOwner := h.authorizer.IsOwner(userID)
		h.auditEvent(entryFor(chatID, userID, isOwner, audit.ActionAgentStop, "exit via /exit"))
		// Claim before announcing: whoever claims owns the single announcement,
		// so an agent that dies at the same moment is still reported exactly once.
		if h.agentCleanup(chatID, run) {
			// Screen before close, same reason as /agent stop: Close interrupts the
			// TUI, which redraws and then clears, so a screen taken after it shows
			// the shutdown instead of the session.
			screen := captureAgentExitScreen(run)
			_ = run.sess.Close()
			h.announceAgentExit(run, screen)
		}
	}
	h.closeShellFor(st.ID)
	st.Active = false
	// /exit is a context reset, not just a process kill. closeShellFor only drops
	// the PTY from h.shells; the *State survives it, so without this block the
	// very next plain message calls getShell and reopens a shell in the same
	// directory under the same project — the command would look like it had done
	// nothing. Killing processes is already covered by /stop (a running command)
	// and /agent stop (the agent), which is what leaves "reset my context" as the
	// one thing only /exit can mean.
	//
	// Cwd is set explicitly rather than cleared: effectiveCwd falls back to the
	// gateway process's own working directory, which is wherever the bot happened
	// to be started, not a predictable "home".
	if home, err := os.UserHomeDir(); err == nil {
		st.Cwd = home
	} else {
		st.Cwd = "$HOME"
	}
	st.Project = ""
	st.LastCmd = ""
	h.sessions.Save()
	h.auditEvent(entryFor(chatID, userID, h.authorizer.IsOwner(userID), audit.ActionExit, "/exit"))
	h.sendPlain(ctx, b, chatID, "🗑 Shell closed. Project and working directory cleared — the next command starts fresh in your home directory.")
}

// handleGet delivers a single file (`get <path>`) or fetches project artifacts:
// `get` sends every artifact, `get <filter>` only the ones whose name/relative
// path contains filter (e.g. "debug").
func (h *Handler) handleGet(ctx context.Context, b *tg.Bot, chatID int64, userID int64, st *session.State, args []string) {
	isOwner := h.authorizer.IsOwner(userID)
	target := strings.Join(args, " ")

	if h.getPathOrFilter(st, target) {
		path, err := h.resolveTargetPath(st, target)
		if err != nil {
			h.send(ctx, b, chatID, formatErr(err.Error()))
			return
		}
		if reason, ok := h.deliveryAuth(st, isOwner, path); !ok {
			e := entryFor(chatID, userID, isOwner, auditActionForDenial(reason), target)
			e.Detail = reason
			h.auditEvent(e)
			h.send(ctx, b, chatID, formatErr(reason))
			return
		}
		h.auditEvent(entryFor(chatID, userID, isOwner, audit.ActionFileGet, target))
		note, err := h.deliverFile(ctx, b, chatID, userID, path, "⬆️ "+filepath.Base(path))
		if err != nil {
			h.send(ctx, b, chatID, formatErr(err.Error()))
			return
		}
		if note != "" {
			h.sendPlain(ctx, b, chatID, note)
		}
		return
	}

	p, ok := h.projects[st.Project]
	if st.Project == "" || !ok {
		h.send(ctx, b, chatID, formatErr("select a project first with /project <name>, or use `get <path>` for a direct file."))
		return
	}
	if len(p.Artifacts) == 0 {
		h.send(ctx, b, chatID, formatErr("no artifacts configured for "+st.Project+". Add `artifacts:` globs in config.yaml, or use `get <path>`."))
		return
	}
	files, err := findArtifacts(p.Path, p.Artifacts)
	if err != nil {
		h.send(ctx, b, chatID, formatErr("artifact scan failed: "+err.Error()))
		return
	}
	files = filterArtifacts(files, target)
	if len(files) == 0 {
		h.send(ctx, b, chatID, formatErr("no artifacts found for "+displayArtifactQuery(target, p.Artifacts)))
		return
	}
	h.auditEvent(entryFor(chatID, userID, isOwner, audit.ActionFileGet, "artifacts "+st.Project+" "+target))

	total := len(files)
	if total > maxArtifactsShare {
		files = files[:maxArtifactsShare]
	}
	for i, f := range files {
		caption := fmt.Sprintf("%d/%d · 📦 %s (%s)", i+1, total, f.Name, humanSize(f.Size))
		note, err := h.deliverFile(ctx, b, chatID, userID, f.Path, caption)
		if err != nil {
			h.sendPlain(ctx, b, chatID, "⛔ "+f.Name+": "+err.Error())
			continue
		}
		if note != "" {
			h.sendPlain(ctx, b, chatID, note)
		}
	}
	if total > maxArtifactsShare {
		h.sendPlain(ctx, b, chatID, fmt.Sprintf("%d found, first %d sent", total, maxArtifactsShare))
	}
}

func displayArtifactQuery(filter string, patterns []string) string {
	if filter != "" {
		return fmt.Sprintf("«%s» (desenler: %s)", filter, strings.Join(patterns, ", "))
	}
	return "desenler: " + strings.Join(patterns, ", ")
}

// deliveryAuth gates file delivery for workers: project selection plus the
// workspace vet applied to the resolved path. The owner bypasses everything.
func (h *Handler) deliveryAuth(st *session.State, isOwner bool, path string) (string, bool) {
	if isOwner {
		return "", true
	}
	if st.Project == "" {
		return "select a project first with /project <name>.", false
	}
	if reason, blocked := h.workspaceVet("deliver " + path); blocked {
		return reason, false
	}
	return "", true
}

// handleDocument auto-saves any sent document into the session working
// directory (never overwriting), subject to the upload size cap.
func (h *Handler) handleDocument(ctx context.Context, b *tg.Bot, chatID int64, userID int64, doc *models.Document) {
	st := h.sessions.Ensure(strconv.FormatInt(chatID, 10))
	isOwner := h.authorizer.IsOwner(userID)

	if doc.FileSize > h.uploadLimit() {
		h.send(ctx, b, chatID, formatErr("file too large: "+humanSize(doc.FileSize)+" > "+humanSize(h.uploadLimit())))
		return
	}
	name := sanitizeUploadFilename(doc.FileName)
	if name == "" {
		h.send(ctx, b, chatID, formatErr("invalid file name"))
		return
	}
	if !isOwner && st.Project == "" {
		h.send(ctx, b, chatID, formatErr("select a project first with /project <name> before uploading."))
		return
	}

	downCtx, cancel := context.WithTimeout(ctx, fileDownloadTimeout)
	defer cancel()

	file, err := b.GetFile(downCtx, &tg.GetFileParams{FileID: doc.FileID})
	if err != nil {
		h.send(ctx, b, chatID, formatErr("file metadata fetch failed: "+err.Error()))
		return
	}
	body, err := downloadTGFile(downCtx, b.FileDownloadLink(file), h.uploadLimit())
	if err != nil {
		h.send(ctx, b, chatID, formatErr(err.Error()))
		return
	}

	dir := h.effectiveCwd(st)
	path := savedPathNoClobber(dir, name)
	if path == "" {
		h.send(ctx, b, chatID, formatErr("could not allocate a unique file name in "+dir))
		return
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		h.send(ctx, b, chatID, formatErr("failed to save file: "+err.Error()))
		return
	}
	h.auditEvent(entryFor(chatID, userID, isOwner, audit.ActionFileUpload, name))
	h.sendPlain(ctx, b, chatID, "💾 saved: `"+escapeCode(path)+"` ("+humanSize(int64(len(body)))+")")
}

// sendFullOutput delivers the full command output as a document when it is too
// long for a single message, preceded by a short preview message. exitCode is
// the real exit status of the command: the preview is the only place the owner
// gets to see it, since the document itself is the raw text.
func (h *Handler) sendFullOutput(ctx context.Context, b *tg.Bot, chatID int64, out []byte, maxMsgLen int, exitCode int) {
	lim := h.uploadLimit()
	data := out
	if int64(len(data)) > lim {
		data = data[:lim]
	}
	preview := terminal.TruncateOutput(out, maxMsgLen)
	h.sendPlain(ctx, b, chatID, "🔎 Output is long ("+humanSize(int64(len(out)))+"). Sending a preview + the full text as an `output.txt` document:")
	h.send(ctx, b, chatID, formatRun("output.txt (full output)", terminal.Result{Output: preview, ExitCode: exitCode}))
	_, err := b.SendDocument(ctx, &tg.SendDocumentParams{
		ChatID:   chatID,
		Document: &models.InputFileUpload{Filename: "output.txt", Data: bytes.NewReader(data)},
	})
	if err != nil {
		h.sendPlain(ctx, b, chatID, "⚠️ failed to send output.txt: "+err.Error())
	}
}

func (h *Handler) formatSessionStatus(st *session.State) string {
	var extra strings.Builder
	if id, err := strconv.ParseInt(st.ID, 10, 64); err == nil {
		if run := h.agentSessionFor(id); run != nil && run.sess.IsAlive() {
			fmt.Fprintf(&extra, "\n\nAgent:\n`pid %d, %s, up %s`", run.sess.PID(),
				escapeCode(run.project), time.Since(run.sess.StartTime()).Round(time.Second))
		} else if h.agentCfg.Enabled {
			// Stated explicitly, the way the Shell block below does it. Printing
			// nothing left the reader unable to tell "no agent running" apart from
			// "this build has no agent support" — and the audit log had just
			// recorded /exit closing one, which is a natural moment to go looking.
			// Gated on Enabled so a gateway with the feature off never suggests a
			// command that cannot work.
			extra.WriteString("\n\nAgent: _(none)_. Send `agent <prompt>` to start one.")
		}
	}
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
	isOwner := h.authorizer.IsOwner(userID)
	if reason, ok := h.authorizeCommand(st, isOwner, raw); !ok {
		e := entryFor(chatID, userID, isOwner, auditActionForDenial(reason), raw)
		e.Detail = reason
		h.auditEvent(e)
		h.send(ctx, b, chatID, formatErr(reason))
		return
	}

	h.auditEvent(entryFor(chatID, userID, isOwner, audit.ActionCommand, raw))

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

	started := time.Now()
	out, err := shell.ExecCommand(runCtx, command)
	durMS := time.Since(started).Milliseconds()
	// The PTY path has no Go error for a command that ran and exited non-zero —
	// the shell reports the status, not the transport — so the frame's TLM_RC
	// line is the only signal, and err would call every failure a success in
	// both the audit trail and the message the owner reads.
	exitCode := terminal.ParseExitCode(out)

	resEntry := entryFor(chatID, userID, isOwner, audit.ActionCommandResult, raw)
	resEntry.DurMS = durMS
	resEntry.OK = audit.Bool(exitCode == 0)
	switch {
	case err == nil:
	case errors.Is(err, context.DeadlineExceeded):
		resEntry.Err = "timeout"
	case errors.Is(err, terminal.ErrInterrupted):
		resEntry.Err = "interrupted"
	default:
		resEntry.Err = err.Error()
	}
	h.auditEvent(resEntry)

	if pwd := terminal.ParsePWD(out); pwd != "" {
		st.Cwd = pwd
		st.Project = h.projectNameByPath(pwd)
		h.sessions.Save()
	}

	cleanAll := terminal.CleanShellOutput(out)
	display := terminal.TruncateOutput(cleanAll, h.maxMsgLen)

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

	if len(cleanAll) > h.maxMsgLen {
		h.sendFullOutput(ctx, b, chatID, cleanAll, h.maxMsgLen, exitCode)
		return
	}

	_, _ = b.SendChatAction(ctx, &tg.SendChatActionParams{ChatID: chatID, Action: models.ChatActionTyping})
	h.send(ctx, b, chatID, formatRun(raw, terminal.Result{
		Output:   display,
		ExitCode: exitCode,
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
	env := h.projectEnv(st)
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

// projectEnv builds the per-session environment additions for a project session:
// TERMILINK_PROJECT plus the project's env block. The base environment is not
// repeated here — the terminal runner prepends os.Environ() to whatever this
// returns.
func (h *Handler) projectEnv(st *session.State) []string {
	env := []string{}
	if st.Project != "" {
		env = append(env, "TERMILINK_PROJECT="+st.Project)
		if p, ok := h.projects[st.Project]; ok {
			for k, v := range p.Env {
				env = append(env, k+"="+v)
			}
		}
	}
	return env
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

	h.agentMu.Lock()
	agents := h.agents
	h.agents = map[int64]*agentRun{}
	h.agentMu.Unlock()
	for _, r := range agents {
		_ = r.sess.Close()
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
	if b == nil {
		return
	}
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
	if b == nil {
		return
	}
	_, _ = b.SendMessage(ctx, &tg.SendMessageParams{ChatID: chatID, Text: text})
}
