# TermiLink

> Remote terminal access and development automation for your own computer.

TermiLink is a self-hosted remote terminal and development agent that lets you
control your own computer through Telegram. Run terminal commands, manage
persistent shell sessions, switch between projects, monitor processes, transfer
files and receive build artifacts — all remotely.

## Status

🚧 Early development — Phase 1 (Core Terminal) and Phase 2 (Persistent PTY
Sessions) are implemented and live-tested. Security controls (role-based access,
dangerous-command approval, workspace policy, single-instance guard) are active.

## Features

- **Telegram gateway** — a chat interface to your own machine; only whitelisted
  user IDs are allowed.
- **Persistent PTY shell** — one interactive shell per chat. Working directory
  and environment survive across commands; use `/input` for interactive input
  and `/stop` to interrupt a running command.
- **Project switching** — `/project <name>` pins the working directory and
  enables per-project command shortcuts (`commands:` in config).
- **Dangerous-command approval** — destructive commands are not executed until
  an owner approves them in chat.
- **Workspace policy** — optionally restrict workers to a set of allowed roots.
- **Files & artifacts** — `get` delivers build artifacts or single files
  (zip fallback, plus owner-approved anonymous-link delivery for files beyond
  Telegram's 50 MB); sent documents are auto-saved.
- **Audit logging** — every command, approval, denial and file transfer is
  written to an append-only JSONL log.
- **Single instance** — a PID lock prevents running two gateways at once.
- **Single-source config** — settings in YAML, secrets in `.env` (auto-loaded).

## Requirements

- Go 1.24+
- A Telegram bot token (from [@BotFather](https://t.me/BotFather))

## Getting Started

```bash
git clone https://github.com/saliherden/termilink.git
cd termilink
cp config.example.yaml config.yaml     # structure/settings
cp .env.example .env                   # secrets (auto-loaded at startup)
# edit both, then:
go build -o termilink ./cmd/termilink
./termilink start
```

Only one gateway instance may run at a time; a second `start` is rejected while
the first is running (PID lock at `~/.termilink/termilink.pid`).

## Telegram Commands

Everything that is **not** a command is executed in the persistent shell.

| Command | Purpose |
| --- | --- |
| `/start`, `/help` | Show the welcome / help message |
| `/project <name>` | Switch working directory to a configured project |
| `/status` | Show project, working directory and recent shell output |
| `/input <text>` | Write input to the running command (`/input ctrl-c` = SIGINT) |
| `/stop` | Interrupt the running command |
| `/exit` | Close the persistent shell |
| `/sessions` | List active sessions |
| `/projects` | List configured projects |
| `get` | Send files/artifacts (also `/get`) |
| `/ping` | Health check |

A configured project command name (e.g. `build`) runs its shortcut command.

## Files & Artifacts

`get` copies files from the machine into the chat. It has **two modes**; the
bot decides which one to use from what you type:

| You type… | Mode | What happens |
| --- | --- | --- |
| `get` | Artifact | Sends **all** build artifacts of the selected project (newest first) |
| `get debug` | Artifact | Sends only artifacts whose name/relative path contains `debug` (case-insensitive) |
| `get ./release/app.apk` | File | Sends **that one file** — also `~/…`, `$HOME/…`, absolute or relative-to-cwd paths |

**Which mode is used?** If the argument looks like a path to an *existing*
file (`./x`, `~/x`, `…/x`, absolute), it's a File. Anything else (`debug`,
`release/14`, `.md`) is a search keyword against artifact names. So `get
release/14` finds `release/14.apk` even though it contains a slash.

`get` also works as `/get` — both spellings are accepted. Note: if a project
defines a command shortcut named `get`, file fetching wins. Files that still
exceed the sending limit are offered as a temporary link — see **Size limits**.

### Artifacts (build outputs)

Define which files count as artifacts per project; they are searched
recursively and newest-first:

```yaml
projects:
  app:
    path: ~/code/app
    artifacts: [ "*.apk", "dist/*.zip" ]   # glob vs. file name, or vs. relative path
```

Globs match against the file name (`*.apk`) or, when they contain a `/`,
against the path relative to the project (`dist/*.zip`). Artifact mode sends
at most 15 files per request.

### Uploading (chat → machine)

Just **send a document** in the chat — it is saved into the session's working
directory automatically. Files are never overwritten: a duplicate gets a `-1`
suffix (`notes.md` → `notes-1.md`). Workers must select a project first.

### Size limits

Telegram lets bots send single documents up to **50 MB**. TermiLink handles
bigger files like this:

```yaml
telegram:
  max_file_bytes: 52428800      # zip decision / upload cap, default 50 MB
  big_file_link_host: uguu.se   # "" (off) | uguu.se | catbox.moe
```

- **≤ 50 MB** → sent directly as a document.
- **> 50 MB** → zipped on the fly first; if the archive fits, it is sent as
  one document (nice for logs, dumps, text).
- **Still > 50 MB** → needs `big_file_link_host` to be set. The file is then
  queued behind an **owner approval** in chat — the bot asks *"File exceeds
  Telegram's 50MB sending limit. Send it via a temporary link on `<host>`?"*
  Confirming with `yes` / `evet` / `ok` uploads a **`.tar` archive** of the
  file (so restricted types like `.apk` pass, and retention still applies)
  and posts the download link as a message (tap and save on your phone).
- **`big_file_link_host` blank** → the clear size error is kept, nothing
  leaves the machine.

Link retention: `uguu.se` auto-deletes after ~3 hours (accepts up to ~128 MB
per file); `catbox.moe` persists (up to 200 MB, may be purged after long
inactivity). `.apk` and other
restricted types are rejected by uguu — the `.tar` envelope handles that.
⚠️ While active, the link is **public** (unguessable URL) — never send
confidential files this way. Every step is audited (`approval_*`,
`file_link`).

- **Uploading:** a document over the limit is rejected with a clear message.

### Long command output

If a command's output exceeds the Telegram message limit, TermiLink does not
truncate silently — a **preview** is shown and the **full text arrives as an
`output.txt` document**.

### Access control

- **Owner** — unrestricted file access and uploads.
- **Workers** — must select a project first; every file delivery is checked
  against the workspace policy (`deliver <path>` vet).

## Security

### Roles

- **Owner** — full access: free `cd`, all commands, and the only user that can
  approve dangerous commands.
- **Workers** — restricted: cannot `cd`, must select a project first, and are
  bound to the workspace policy.

### Dangerous command approval

```yaml
security:
  approve_dangerous: all   # all (default) | worker | off
  dangerous_patterns: []   # optional extra regexes that also require approval
```

When a message matches a dangerous pattern it is **queued, not executed**:

1. Bot replies: `⚠️ Dangerous command detected: … — reply yes / evet / ok to
   approve or no / hayır to reject (2m0s). The command will not run until
   approved.`
2. **Owner** replies `yes` (or `evet` / `ok` / `onay`) → the command runs.
   `no` (or `hayır` / `cancel` / `iptal`) → rejected, **never executed**.
3. Any other reply keeps the request pending. If no decision arrives within 2
   minutes the request expires — the command is **not executed**.

Built-in patterns that require approval (deliberately narrow, so normal
development commands are never gated):

- `rm -rf …` targeting `/`, `~`, or `$HOME` (relative `rm -rf dist` is fine)
- `dd … of=/dev/…`, writes to `/dev/…`
- `mkfs` / `fdisk` / `gdisk` / `parted`
- `shutdown` / `reboot` / `poweroff` / `halt` / `sync`
- `sudo …`
- the classic fork bomb, `chown … /`, `chmod … /`

`approve_dangerous` modes:
- `all` — apply to owner and workers (default)
- `worker` — prompt only for workers; owner runs dangerous commands directly
- `off` — disable the gate

Add your own triggers with `dangerous_patterns` (Go `regexp` syntax), e.g.
`- "git\\s+push\\s+(-f|--force)"`.

### Workspace policy (workers)

```yaml
workspace:
  allowed:
    - /Users/you/projects
```

When `workspace.allowed` is non-empty, workers may only access paths under one
of the allowed roots: out-of-scope absolute paths, `~`/`$HOME` paths and `..`
escapes in their commands are rejected, and `/project` refuses projects outside
the allowed roots. The owner is never bound by this policy.

Note: this is a **policy-level guard, not an OS sandbox** — relative paths and
shell expansions are trusted to the user, and the owner is fully trusted.

### Audit logging

```yaml
security:
  audit_log: ""   # empty = ~/.termilink/audit.log, "off" disables, or a path
```

Every security-sensitive event is appended to the audit file as one JSON line
per event: commands (with result, duration and exit state), the danger-approval
flow (requested / approved / rejected / blocked / timeout), whitelist
rejections, workspace vet blocks, project switches, `/input`, `/stop` and
`/exit`. Obvious secret values in commands (`password=…`, `token=…`,
`Authorization: Bearer …`, …) are written as `***redacted***`; the executed
command is never modified.

Read recent entries with:

```bash
termilink audit          # last 20 entries, human-readable
termilink audit -n 100   # last 100 entries
termilink audit --json   # raw JSON lines, e.g. for machine processing
```

Auditing never takes the agent down — write errors are reported once to stderr
and ignored. `security.audit_log: off` disables it entirely.

## CLI

```bash
termilink start        # start the agent (Telegram gateway)
termilink status       # show runtime status
termilink config       # show resolved configuration
termilink projects     # list configured projects
termilink sessions     # list active sessions
termilink audit        # show recent audit log entries
termilink init         # scaffold a config file
```

## Development

```bash
make build
make run
make test
```

## License

MIT. See [LICENSE](LICENSE).