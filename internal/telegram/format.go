package telegram

import (
	"fmt"
	"strings"
	"time"

	"github.com/saliherden/termilink/internal/terminal"
	"github.com/saliherden/termilink/internal/version"
)

var welcomeMsg = "*TermiLink — Remote Terminal*\n\n" +
	"Controlled access to your own computer via Telegram.\n\n" +
	"*Commands*\n" +
	"/start — show this message\n" +
	"/help — show command help\n" +
	"/project <name> — switch working directory to a configured project\n" +
	"/status — show current session context and shell tail\n" +
	"/input <text> — send input to the running command (e.g. `/input ctrl-c`)\n" +
	"/stop — interrupt the running command (SIGINT)\n" +
	"/exit — close the persistent shell\n" +
	"/sessions — list active terminal sessions\n" +
	"/ping — health check\n\n" +
	"*Usage*\n" +
	"Everything that is not a command is executed in the persistent shell:\n\n" +
	"• `cd ~/projects/my-app` — change the persistent working directory\n" +
	"• `npm run dev` — run any command\n" +
	"• Type a configured project command name (e.g. `build`) to run it.\n\n" +
	"Version: " + version.Version

const helpMsg = "*TermiLink Help*\n\n" +
	"*Direct Terminal*\n" +
	"Send any shell command as a plain message. Commands run inside a\n" +
	"persistent interactive shell, so the working directory and environment\n" +
	"are preserved; only whitelisted users can reach the agent.\n\n" +
	"*Project switching*\n" +
	"/project <name>\n" +
	"Lists projects with /projects and /help.\n\n" +
	"*Interactive control*\n" +
	"/input <text> — write to the running command (newline appended)\n" +
	"/input ctrl-c — send SIGINT to interrupt\n" +
	"/stop — interrupt the running command\n" +
	"/exit — close the shell\n\n" +
	"*Session state*\n" +
	"/status shows the project, working directory and shell tail for this chat."

const (
	pingOK  = "🏓 pong"
	noCmd   = "_(no command yet)_"
	noCwd   = "_(home directory)_"
	unknown = "⚠️ Unknown command. Try /help."
)

func formatProjectHome(project string, cwd string, cmds []string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("✅ Project *%s* selected.\n\nWorking directory:\n`%s`\n", project, cwd))
	if len(cmds) > 0 {
		b.WriteString("\nAvailable commands:\n")
		for _, c := range cmds {
			b.WriteString("• `" + escapeCode(c) + "`\n")
		}
	}
	return b.String()
}

func formatProjects(list []string) string {
	if len(list) == 0 {
		return "_No projects configured._"
	}
	var b strings.Builder
	b.WriteString("*Configured projects*\n")
	for _, name := range list {
		b.WriteString("• `" + escapeCode(name) + "`\n")
	}
	b.WriteString("\nSwitch with /project <name>")
	return b.String()
}

func formatStatus(project, cwd, lastCmd string) string {
	var b strings.Builder
	b.WriteString("*Current context*\n\n")
	if project != "" {
		b.WriteString("Project: `" + escapeCode(project) + "`\n\n")
	}
	if cwd == "" {
		cwd = noCwd
	}
	b.WriteString("Working directory:\n`" + escapeCode(cwd) + "`\n\n")
	if lastCmd == "" {
		lastCmd = noCmd
	}
	b.WriteString("Last command:\n`" + escapeCode(lastCmd) + "`")
	return b.String()
}

func formatSessions(rows []string) string {
	if len(rows) == 0 {
		return "_No sessions._"
	}
	var b strings.Builder
	b.WriteString("*Sessions*\n")
	for _, r := range rows {
		b.WriteString(r + "\n")
	}
	return b.String()
}

func formatRun(cmd string, res terminal.Result) string {
	payload := strings.TrimSpace(string(res.Output))
	if payload == "" {
		payload = "_(no output)_"
	}

	var b strings.Builder
	b.WriteString("`$ " + escapeCode(cmd) + "`\n\n")
	b.WriteString(sanitizeCode(payload) + "\n")

	status := "❌ Process exited with code " + fmt.Sprint(res.ExitCode)
	switch {
	case res.TimedOut:
		status = "⏱ Command timed out"
	case res.ExitCode == 0:
		status = "✅ Process exited with code 0"
	}
	b.WriteString("\n" + status)
	return b.String()
}

func formatErr(msg string) string {
	return "⚠️ " + msg
}

func formatTimeout(cmd, out string, timeout time.Duration) string {
	var b strings.Builder
	b.WriteString("`$ " + escapeCode(cmd) + "`\n\n")
	if strings.TrimSpace(out) == "" {
		out = "_(no output)_"
	}
	b.WriteString(sanitizeCodeBlock(out) + "\n\n")
	fmt.Fprintf(&b, "⏱ Command did not finish within %s.", timeout.Round(time.Second))
	return b.String()
}

func formatInterrupted(cmd, out string) string {
	var b strings.Builder
	b.WriteString("`$ " + escapeCode(cmd) + "`\n\n")
	if strings.TrimSpace(out) == "" {
		out = "_(no output)_"
	}
	b.WriteString(sanitizeCodeBlock(out) + "\n\n")
	b.WriteString("⏹ Command interrupted.")
	return b.String()
}

func formatApprovalPrompt(raw string, ttl time.Duration) string {
	return fmt.Sprintf(
		"⚠️ Dangerous command detected:\n`%s`\n\nReply with *evet* to approve or *hayır* to reject (%s).\nThe command will not run until approved.",
		escapeCode(raw), ttl.Round(time.Second))
}

func formatApprovalStillPending(raw string) string {
	return fmt.Sprintf("⏳ Approval pending for:\n`%s`\n\nReply with *evet* or *hayır*.", escapeCode(raw))
}

func formatApprovalTimeout() string {
	return "⏰ Approval request expired — the dangerous command was *not* executed. Send it again if you still want to run it."
}

func sanitizeCodeBlock(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if len(s) > 3800 {
		s = s[:3800] + "\n… (truncated) …"
	}
	return strings.ReplaceAll(s, "```", "'''")
}

func sanitizeCode(s string) string {
	return "```\n" + strings.ReplaceAll(s, "```", "'''") + "\n```"
}

func escapeCode(s string) string {
	return strings.ReplaceAll(s, "`", "'")
}
