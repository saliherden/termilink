package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/saliherden/termilink/internal/config"
	"github.com/saliherden/termilink/internal/security"
	"github.com/saliherden/termilink/internal/session"
	"github.com/saliherden/termilink/internal/terminal"
)

const (
	defaultMaxMsgLen = 3500
	msgMaxLength     = 4096
)

type Handler struct {
	authorizer *security.Authorizer
	runner     *terminal.Runner
	sessions   *session.Manager
	projects   map[string]config.ProjectConfig
	maxMsgLen  int
	log        *slog.Logger
}

type Options struct {
	Authorizer *security.Authorizer
	Runner     *terminal.Runner
	Sessions   *session.Manager
	Projects   map[string]config.ProjectConfig
	MaxMsgLen  int
	Logger     *slog.Logger
}

func NewHandler(opts Options) *Handler {
	h := &Handler{
		authorizer: opts.Authorizer,
		runner:     opts.Runner,
		sessions:   opts.Sessions,
		projects:   opts.Projects,
		maxMsgLen:  opts.MaxMsgLen,
		log:        opts.Logger,
	}
	if h.maxMsgLen <= 0 || h.maxMsgLen > msgMaxLength {
		h.maxMsgLen = defaultMaxMsgLen
	}
	if h.sessions == nil {
		h.sessions = session.NewManager()
	}
	if h.log == nil {
		h.log = slog.Default()
	}
	return h
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
		h.handleCommand(ctx, b, chatID, st, text)
		return
	}
	h.runCommand(ctx, b, chatID, userID, st, text)
}

func (h *Handler) handleCommand(ctx context.Context, b *tg.Bot, chatID int64, st *session.State, text string) {
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
		h.handleProject(ctx, b, chatID, st, args)
	case "status":
		h.send(ctx, b, chatID, formatStatus(st.Project, h.effectiveCwd(st), st.LastCmd))
	case "sessions":
		h.send(ctx, b, chatID, formatSessions(h.sessionRows()))
	default:
		h.send(ctx, b, chatID, unknown)
	}
}

func (h *Handler) handleProject(ctx context.Context, b *tg.Bot, chatID int64, st *session.State, args []string) {
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
	st.Cwd = p.Path
	st.Project = name
	h.sessions.Save()

	cmdNames := make([]string, 0, len(p.Commands))
	for k := range p.Commands {
		cmdNames = append(cmdNames, k)
	}
	h.send(ctx, b, chatID, formatProjectHome(name, p.Path, cmdNames))
}

func (h *Handler) runCommand(ctx context.Context, b *tg.Bot, chatID int64, userID int64, st *session.State, raw string) {
	if st.Active {
		h.send(ctx, b, chatID, formatErr("a command is already running in this session; send /status to inspect it."))
		return
	}
	if reason, ok := h.authorizeCommand(st, h.authorizer.IsOwner(userID), raw); !ok {
		h.send(ctx, b, chatID, formatErr(reason))
		return
	}

	command, isCd := h.resolve(st, raw)

	if isCd {
		res, err := h.runner.Execute(ctx, st.Cwd, command, nil)
		if err != nil {
			h.send(ctx, b, chatID, formatErr("failed to change directory: "+err.Error()))
			return
		}
		if res.ExitCode != 0 {
			h.send(ctx, b, chatID, formatRun(command, res))
			return
		}
		st.Cwd = firstLine(string(res.Output))
		st.Project = h.projectNameByPath(st.Cwd)
		st.LastCmd = raw
		h.sessions.Save()
		h.send(ctx, b, chatID, "📁 Working directory:\n`"+escapeCode(st.Cwd)+"`")
		return
	}

	st.Active = true
	st.LastCmd = raw
	defer func() {
		st.Active = false
		h.sessions.Save()
	}()

	_, _ = b.SendChatAction(ctx, &tg.SendChatActionParams{ChatID: chatID, Action: models.ChatActionTyping})

	res, err := h.runner.Execute(ctx, st.Cwd, command, nil)
	if err != nil {
		h.send(ctx, b, chatID, formatErr(err.Error()))
		return
	}
	if res.Capped {
		h.log.Warn("command output hit max_output_bytes", "chat_id", chatID, "command", raw)
	}
	res.Output = terminal.TruncateOutput(res.Output, h.maxMsgLen)
	h.send(ctx, b, chatID, formatRun(raw, res))
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
	return "", true
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

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}