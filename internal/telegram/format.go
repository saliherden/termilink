package telegram

import (
	"fmt"
	"strings"

	"github.com/saliherden/termilink/internal/terminal"
	"github.com/saliherden/termilink/internal/version"
)

var welcomeMsg = "*TermiLink — Remote Terminal*\n\n" +
	"Controlled access to your own computer via Telegram.\n\n" +
	"*Commands*\n" +
	"/start — show this message\n" +
	"/help — show command help\n" +
	"/project <name> — switch working directory to a configured project\n" +
	"/status — show current session context\n" +
	"/sessions — list active terminal sessions\n" +
	"/ping — health check\n\n" +
	"*Usage*\n" +
	"Everything that is not a command is executed directly in the shell:\n\n" +
	"• `cd ~/projects/my-app` — change the persistent working directory\n" +
	"• `npm run dev` — run any command\n" +
	"• Type a configured project command name (e.g. `build`) to run it.\n\n" +
	"Version: " + version.Version

const helpMsg = "*TermiLink Help*\n\n" +
	"*Direct Terminal*\n" +
	"Send any shell command as a plain message. The command runs on your\n" +
	"computer and the output streams back here.\n\n" +
	"Working directory persists per chat with `cd`.\n\n" +
	"*Project switching*\n" +
	"/project <name>\n" +
	"Lists projects with /projects and /help.\n\n" +
	"*Session state*\n" +
	"/status shows the current working directory and last command for this chat.\n\n" +
	"_Everything you send is executed as shell code. Only whitelisted users can\n" +
	"reach this agent._"

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

func sanitizeCode(s string) string {
	return "```\n" + strings.ReplaceAll(s, "```", "'''") + "\n```"
}

func escapeCode(s string) string {
	return strings.ReplaceAll(s, "`", "'")
}