# TermiLink

![CI](https://github.com/saliherden/termilink/actions/workflows/ci.yml/badge.svg)

> Remote terminal access and development automation for your own computer.

TermiLink is a self-hosted remote terminal and development agent that lets you
control your own computer through Telegram. Run terminal commands, manage
persistent shell sessions, switch between projects, monitor processes, transfer
files and receive build artifacts — all remotely.

## Status

🚧 Early development, in active daily use. Implemented and live-tested: the core
terminal, persistent PTY sessions, the interactive coding-agent bridge (live
screen as a photo, the final screen and the session id as text), its scrollback
reader, files & artifacts, and the security controls (role-based access,
dangerous-command approval, workspace policy, single-instance guard, audit
logging with rotation).

## Features

- **Telegram gateway** — a chat interface to your own machine; only whitelisted
  user IDs are allowed.
- **Persistent PTY shell** — one interactive shell per chat. Working directory
  and environment survive across commands; use `/input` for interactive input
  and `/stop` to interrupt a running command.
- **Agent TUI bridge** — `agent <prompt>` runs opencode / claude / codex / gemini
  in the selected project and relays its real screen, colors included, as an
  image that is updated in place. Every message you send is typed into it. When
  the session ends the screen comes back as plain text, along with the session id
  you can resume from.
- **Scrollback reader** — `/agent history` re-reads everything the agent has
  drawn, in its own colors, by scrolling one message with `/agent up` /
  `/agent down` instead of paging through dozens of messages.
- **Project switching** — `/project <name>` pins the working directory and
  enables per-project command shortcuts (`commands:` in config).
- **Dangerous-command approval** — destructive commands are not executed until
  an owner approves them in chat.
- **Workspace policy** — optionally restrict workers to a set of allowed roots.
- **Files & artifacts** — `get` delivers build artifacts or single files
  (zip fallback, plus owner-approved anonymous-link delivery for files beyond
  Telegram's 50 MB); sent documents are auto-saved.
- **Audit logging** — every command, approval, denial, file transfer, agent
  event and recovered panic is written to an append-only JSONL log that can be
  rotated by size.
- **Never dies quietly** — a panic in any command is contained, logged with its
  stack and reported to the owner instead of taking the whole gateway down.
- **Single instance** — a PID lock prevents running two gateways at once.
- **Single-source config** — settings in YAML, secrets in `.env` (auto-loaded).

## Requirements

- Go 1.27.1 or newer (CI runs on `1.27.x`)
- zsh, bash or sh — TermiLink drives an interactive shell session; see
  [Which shell is used](#which-shell-is-used) to pick one
- A Telegram bot token (from [@BotFather](https://t.me/BotFather))

### Which shell is used

`terminal.shell` in `config.yaml` decides. Left unset, TermiLink picks the first
of `/bin/zsh`, `/bin/bash`, `/bin/sh` that exists on the machine.

⚠️ **`config.example.yaml` sets `/bin/zsh` explicitly**, so copying it as-is
(`cp config.example.yaml config.yaml`) overrides that auto-detection. On a
machine without zsh, every shell command then fails — the path is not checked
at load time, so the error only surfaces once you send the first command.
Install zsh (`apt install zsh`) or point that one line at your own shell.

## Platform support

| platform | status | notes |
| --- | --- | --- |
| macOS | supported | CI-tested; `termilink service install` generates and loads a launchd agent |
| Linux | supported | CI-tested on `ubuntu-24.04`; `termilink service install` generates and loads a systemd user unit |
| Windows | **not supported** | `creack/pty` compiles there, but `StartWithSize` returns `ErrUnsupported` at runtime, so every PTY operation fails |

The PTY library is what restricts the core to Unix, which is also why Windows is
absent from CI rather than failing in it. A few files do carry `//go:build`
constraints — the service installers (`service_darwin.go`, `service_linux.go`,
`service_other.go`) and the process-liveness check (`process_unix.go`,
`process_windows.go`) — but everything they share is platform-neutral.

In practice: build from source. **No binaries or releases are published yet.**
Long-polling is the transport, so the gateway needs no inbound port and works
from behind NAT, but it does run with the full privileges of the user who
started it — see [Known limitations](#known-limitations).

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

## Configuration

`config.yaml` in the working directory, plus `.env` (auto-loaded at startup).
Every key, its type and its effective default:

### `telegram`

| key | type | default | notes |
| --- | --- | --- | --- |
| `bot_token` | string | — **required** | either inline or `${TELEGRAM_BOT_TOKEN}`; start fails if neither resolves |
| `max_file_bytes` | int | `52428800` (50 MiB) | the send/zip decision threshold; must be > 0 |
| `big_file_link_host` | string | `""` (off) | `uguu.se` or `catbox.moe`; anything else is rejected at load |

### `security`

| key | type | default | notes |
| --- | --- | --- | --- |
| `owner` | int | first entry of `allowed_users` | automatically appended to the allowlist if missing |
| `allowed_users` | []int | — **required** | at least one Telegram user id; everyone else is rejected |
| `approve_dangerous` | string | `all` | `all` \| `worker` \| `off` |
| `dangerous_patterns` | []string | none | extra regexes, OR-ed with the built-in list |
| `audit_log` | string | `~/.termilink/audit.log` | absolute path, or `off` to disable auditing |
| `audit_max_bytes` | int | `0` (unlimited) | past this, the file rotates to `<path>.1` before the next entry |
| `audit_keep` | int | `1` | how many rotated archives to retain; below 1 is clamped to 1 |

### `workspace`

| key | type | default | notes |
| --- | --- | --- | --- |
| `allowed` | []string | `[]` | **empty means no filesystem restriction at all** — see [Known limitations](#known-limitations) |

### `terminal`

| key | type | default | notes |
| --- | --- | --- | --- |
| `shell` | string | first of `/bin/zsh`, `/bin/bash`, `/bin/sh` that exists, else `/bin/sh` | must not be empty |
| `command_timeout` | duration | `30m` | applied as a deadline around each command; a timeout is reported, not silently dropped |
| `max_output_bytes` | int | `1048576` (1 MiB) | raised to at least `65536`; over-long output keeps the **most recent** bytes, and the exit status is still exact |

### `agent`

| key | type | default | notes |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | gates the whole feature |
| `command` | string | `""` (auto-detect) | searches `opencode`, `claude`, `codex`, `gemini` on `PATH`; a bare name or absolute path pins one. `~` is **not** expanded — write the full path |
| `screen.mode` | string | `png` | `png` = live screen as a color photo, `text` = code block. The screen posted when a session *ends* is always text |

### `projects`

| key | type | default | notes |
| --- | --- | --- | --- |
| `<name>.path` | string | — | working directory for `/project <name>` |
| `<name>.commands` | map[string]string | — | named shortcuts |
| `<name>.artifacts` | []string | — | paths collected by `get` |
| `<name>.env` | map[string]string | — | exported into that project's shell; keys may not contain `=` or a newline, values no newline or NUL |

### `state_file` (top level)

| key | type | default | notes |
| --- | --- | --- | --- |
| `state_file` | string | `~/.termilink/state.json` | must be absolute; where persisted terminal sessions live |

Validation is strict where a wrong value would fail silently: `shell` must be
non-empty, `max_file_bytes` positive, `audit_max_bytes` and `audit_keep`
non-negative, `state_file` absolute, and the three enumerated fields accept
only the values listed above.

Both file paths are resolved once, in the same place, for the running agent and
for the `status` and `sessions` commands — so `termilink sessions` always reads
the file the agent actually writes. When no path can be resolved at all (no home
directory, nothing configured) the agent **refuses to start** rather than
degrading to in-memory sessions, which would look healthy and forget everything
on restart. `state_file` is what makes a service install work when the account
it runs under has no usable home directory.

## Telegram Commands

Everything that is **not** a command is executed in the persistent shell.

### Direct terminal

| Command | Purpose |
| --- | --- |
| `/start`, `/help` | Show the welcome / help message |
| `/project <name>` | Switch working directory to a configured project |
| `/projects` | List configured projects |
| `/status` | Show project, working directory, agent session (`none` when there is none) and recent shell output |
| `/input <text>` | Write input to the running command (`/input ctrl-c` = SIGINT) |
| `/stop` | Interrupt the running command (SIGINT) |
| `/exit` | Reset the session: close the agent (if running) and the shell, and clear the project and working directory |
| `/sessions` | List active sessions |
| `get`, `/get <filter>` | Send files/artifacts from the machine |
| `/ping` | Health check |
| `agent <prompt>` | Open the coding-agent TUI in the selected project (owner only) |

A configured project command name (e.g. `build`) runs its shortcut command.

### Agent session

| Command | Purpose |
| --- | --- |
| `/agent status` | Show the running agent session |
| `/agent stop`, `/agent exit` | Close the agent session, post its final screen as text, then report the session id (the shell keeps running) |
| `/agent history` | Open the scrollback reader |
| `/agent up`, `/agent down` | Scroll the reader one screen at a time |
| `/agent top`, `/agent bottom` | Jump to the oldest / newest line |
| `/agent history exit`, `/agent off` | Drop the hint line, leave the reader's image in the chat |

There is no `/agent close`; the session is closed by `/agent exit` or
`/agent stop`.

### Closing things — three scopes, three commands

The names are deliberately distinct so nothing closes something you did not mean
to close:

| Command | Closes | Leaves running |
| --- | --- | --- |
| `/agent history exit` | the reader's hint line | the agent session |
| `/agent exit` | the agent session | the shell |
| `/exit` | **everything** — the agent session *and* the shell | nothing, and the project binding is cleared too |

`/exit` is the "shut it all down and start over" command. It stops the agent
before closing the shell, so TermiLink can capture its final visible screen, then
clears the project binding, the working directory and the last command.
`/agent stop` and `/agent exit` capture that same final screen but leave the
shell and the project in place, so every path that ends an agent session — those
two, `/exit`, or the agent exiting on its own — leaves a readable record behind.

### The exit message carries the session id

Once the agent has closed, TermiLink asks its CLI which session that run was and
posts the id with the command that reopens it:

```
🆔 Session `ses_f318e258fffe5G8PbTz5Zh` — resume with `opencode -s ses_f318e258fffe5G8PbTz5Zh`
```

Only opencode-compatible CLIs answer (`session list --format json`); one that
does not understand the subcommand is detected once and the feature steps aside.
The id is reported **only if exactly one** session was created during the run —
never guessed, never invented.

### A slash is only a command when the bot knows it

A leading `/` is treated as a command only when it matches a known command.
Unknown slash-prefixed messages are passed to the running agent as text; without
an agent they return `⚠️ Unknown command`. Special TUI keys are typed without a
slash — `up`, `down`, `esc`, `enter` — so they can never collide with a real
command.

## Agent (TUI bridge)

`agent <prompt>` launches your coding-agent CLI inside a **pseudo-terminal rooted
at the selected project directory** (you need `/project <name>` first) and
relays its screen into the chat. It works with any TUI CLI that can run on the
machine — OpenCode, Claude Code, Codex or Gemini CLI:

```
🤖 Agent started.

Project: myapp
Working dir: /Users/me/code/myapp
PID: 31337
```

While a session is active, every message you send is **typed into the agent's
input box** (Enter appended), so the workload runs interactively inside the
agent. Each message starts a new turn. Set `agent.screen.mode: text` to drop the
live images and get copyable text for the whole session.

### What arrives in the chat, and as what

The medium follows what the thing is, not how wide the screen is:

| | sent as | why |
| --- | --- | --- |
| live screen (every frame) | **photo** | re-uploaded ~3×/second; a document per frame would drop a file card into the chat on every tick |
| final screen (`/agent stop`, `/agent exit`, `/exit`, or the agent exiting on its own) | **text**, a code block | it is written output, not a picture of one — copyable, searchable, pasteable |
| scrollback reader (`/agent history`) | **document** | a page of scrollback is as wide as the TUI, and Telegram does not scale documents, so it opens at native resolution |

The final screen is the window the TUI was showing at the moment the agent
stopped, not the whole conversation: your prompts scroll up and off the top of
the 40-row window, so it holds the tail of the session. For the whole thing, use
`/agent history`.

### Keys

| You type… | Meaning |
| --- | --- |
| plain text | typed into the agent input box, Enter pressed |
| `^p`, `^c`, … | Ctrl-key (`^p` = Ctrl+P → command palette) |
| `↑` `↓` `←` `→` | arrow keys |
| `up`, `down`, `left`, `right`, `esc`, `tab`, `enter`, `backspace`, `delete`, `home`, `end`, `pageup`, `pagedown`, `insert` | the named special key, sent as real key bytes |
| `^x l` | **key chord**: `Ctrl+X` then `l` (switch session) — opencode uses `ctrl+x` chord shortcuts like `ctrl+x n` (new session), `ctrl+x m` (switch model) |
| `^p enter` | chord that ends with Enter (select the highlighted palette entry) |

Navigation inside panels is **`↑`/`↓` + `enter` to select, `esc` to close**. Key
names go in without a slash — `up`, not `/up`, for the reason given in
[A slash is only a command when the bot knows it](#a-slash-is-only-a-command-when-the-bot-knows-it).

### The scrollback reader

`/agent history` opens it; `/agent up` and `/agent down` move one screenful at a
time and repaint the same message in place, `/agent top` and `/agent bottom` jump
to either end, and `/agent history exit` (short: `/agent off`) drops the hint
line and leaves the image in the chat to re-read later.

It is a single message showing up to 40 rows rather than a set of numbered
pages, and it renders in the agent's own colors. Consecutive screens overlap by
four lines, so walking to the top shows every line at least once; near the top
the window is allowed to become **shorter** than 40 rows and shows what is left
above rather than skipping it. The view spans the lines that scrolled off *plus*
the rows currently on screen, so scrolling up from the bottom never lands in a
gap; the scrollback itself is capped at the last 2000 distinct lines. Scrolling
is slash-only, so the bare words `up` and `down` stay reserved for the TUI's
arrow keys. This is how you re-read a long reply while the session keeps running.

**Only the owner** can start an agent or write into one — the running agent has
the same machine permissions as the bot user, so it is never exposed to workers.

## Files & artifacts

### Downloading

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
exceed the sending limit are offered as a temporary link — see
[Size limits](#size-limits).

Which files count as artifacts is per project; they are searched recursively and
newest-first, at most 15 per request:

```yaml
projects:
  app:
    path: ~/code/app
    artifacts: [ "*.apk", "dist/*.zip" ]   # glob vs. file name, or vs. relative path
```

Globs match against the file name (`*.apk`) or, when they contain a `/`, against
the path relative to the project (`dist/*.zip`).

With a project selected, `TERMILINK_PROJECT=<name>` is already exported into the
shell. Add more per-project variables with `env:` — they are exported when the
shell session is (re)created (first command, `/project` switch, or `/exit`):

```yaml
projects:
  app:
    path: ~/code/app
    env:
      APP_PORT: "8080"
      NODE_ENV: production
```

If a command's output exceeds the Telegram message limit, TermiLink does not
truncate silently — a **preview** is shown and the **full text arrives as an
`output.txt` document**.

### Uploading

Just **send a document** in the chat — it is saved into the session's working
directory automatically. Files are never overwritten: a duplicate gets a `-1`
suffix (`notes.md` → `notes-1.md`). A document over the sending limit is
rejected with a clear message.

### Size limits

Telegram lets bots send single documents up to **50 MB**. TermiLink handles
bigger files like this, using [`telegram.max_file_bytes` and
`telegram.big_file_link_host`](#telegram):

- **≤ 50 MB** → sent directly as a document.
- **> 50 MB** → zipped on the fly first; if the archive fits, it is sent as
  one document (nice for logs, dumps, text).
- **Still > 50 MB** → needs `big_file_link_host` to be set. The file is then
  queued behind an **owner approval** in chat — the bot asks *"File exceeds
  Telegram's 50MB sending limit. Send it via a temporary link on `<host>`?"*
  Confirming with `yes` / `evet` / `ok` uploads a **`.tar` archive** of the
  file (so restricted types like `.apk` pass) and posts the download link as a
  message (tap and save on your phone).
- **`big_file_link_host` blank** → the clear size error is kept, nothing
  leaves the machine.

### External links

⚠️ External links are anonymous public URLs hosted outside your machine.
**Do not use this path for secrets, credentials or customer data.**

| host | size cap | retention | deleteable by TermiLink? |
| --- | --- | --- | --- |
| `uguu.se` | ~128 MB | auto-deleted after ~3 hours | yes, by the host's own timer |
| `catbox.moe` | 200 MB | **persists** — removed only if the host purges it for inactivity | **no** |

The URL is unguessable, but the file is served to anyone who has it and the host
operator can read it for as long as it is up — with `catbox.moe` that includes a
`.tar` of what you sent, potentially indefinitely, on infrastructure you do not
control. The host is a single global setting in `config.yaml`, so every oversized
file goes through it; leave it empty and accept the size error unless you have a
reason to send something off the machine.

Owner approval gates the transfer and every step is audited (`approval_*`,
`file_link`), but approval is a prompt in the chat, not a confidentiality
control: approving it *is* the act of publishing the file.

### Access control

Owner: unrestricted. Workers: must select a project; every delivery is vetted
against the workspace policy. See [Security](#security).

## Security

### Roles

**Owner** — full access: free `cd`, all commands, and the only user that can
approve dangerous commands or control an agent session. **Workers** — restricted:
cannot `cd`, must select a project first, and are bound to the workspace policy.
See [Known limitations](#known-limitations) before adding a second user.

### Dangerous command approval

Configured as `security.approve_dangerous` and `security.dangerous_patterns`
(see [Configuration](#security)). When a message matches a dangerous pattern it
is **queued, not executed**:

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

Configured as `workspace.allowed` (see [Configuration](#workspace)). When it is
non-empty, workers may only access paths under one of the allowed roots:
out-of-scope absolute paths, `~`/`$HOME` paths and `..` escapes in their
commands are rejected, and `/project` refuses projects outside the allowed roots.
The owner is never bound by this policy. **Empty — the default — means no
restriction at all**, so read
[Known limitations](#known-limitations) before adding a second user.

### Audit logging

Configured as `security.audit_log` and `security.audit_max_bytes` (see
[Configuration](#security)). Every security-sensitive event is appended as one
JSON line per event: commands (with result, duration and exit state), the
danger-approval flow, whitelist rejections, workspace vet blocks, project
switches, `/input`, `/stop` and `/exit`.

| action | raised when |
| --- | --- |
| `command`, `command_result` | a command ran, and how it ended |
| `approval_requested`, `approval_approved`, `approval_rejected`, `approval_blocked`, `approval_timeout` | the dangerous-command flow |
| `access_denied` | a non-owner, or a non-whitelisted user, tried to do something |
| `project_switch`, `vet_blocked`, `file_get`, `file_upload`, `file_link` | project and file activity |
| `agent_start`, `agent_input`, `agent_stop`, `agent_history`, `agent_session`, `agent_error` | agent sessions |
| `panic` | a fault was contained; the message and stack also go to the log |

`panic` is the one to grep for first when the bot goes quiet: it means a command
died but the gateway survived. Obvious secret values in commands
(`password=…`, `token=…`, `Authorization: Bearer …`) are written as
`***redacted***`; the executed command is never modified. Auditing never takes
the agent down — write errors are reported once to stderr and ignored.
`security.audit_log: off` disables it entirely, and `audit_max_bytes` (default
`0` = unlimited) rotates to `audit.log.1` once the file exceeds it.

Rotation is generational, and the default retention is one archive. With
`audit_keep: 5` a rotation moves `audit.log` to `audit.log.1`, shifts the
existing archives up to `.5` and drops whatever was there — so the disk cost is
bounded by roughly `audit_max_bytes × (audit_keep + 1)`. The default of `1`
reproduces the older single-`.1` behaviour exactly, which means the log never
held more than one cap's worth of history; an investigation that reached back
further than that found the entry gone. Lowering the value clears the archives
past the new count on the next rotation, and `termilink status` shows the
resolved log path.

Read recent entries with:

```bash
termilink audit          # last 20 entries, human-readable
termilink audit -n 100   # last 100 entries
termilink audit --json   # raw JSON lines, e.g. for machine processing
```

### Known limitations

This runs arbitrary commands as your user account, so the boundary of what it
actually enforces matters more than the feature list. Each item below is a
deliberate, current limitation, not a roadmap promise.

**The workspace policy is not a sandbox, and is not scoped by default.** With
`workspace.allowed` empty — the default — there is no filesystem restriction at
all: any allowed user can read and write anything the process can. Even when you
do configure it, the guard is a text-level check over the command's tokens, so
relative paths, command substitution (`$(cd /etc && …)`) and symlinks pass
straight through. Set `workspace.allowed` before adding a second user, and treat
workers as trusted.

**The dangerous-command gate is a regex, not a policy engine.** It matches eight
built-in patterns against the raw text, so a command that reaches the same effect
without matching one — `find … -delete`, `git clean -fdx`, a script that does the
work — is not gated. With `approve_dangerous: worker` the owner is exempt
entirely. Approval means "the owner said yes in chat"; once given, the command
runs with the full privileges of the user who started the gateway.

**The owner is unrestricted by design.** The owner may `cd` anywhere, is not
bound by `workspace.allowed`, and is the only role that can approve a dangerous
command or control an agent session. There is no second tier of privilege above
or below that.

**There is no chat-type or chat-allowlist check.** Authorization is keyed on the
Telegram user id alone (`security.allowed_users`); nothing inspects whether the
message arrived in a private chat, a group or a channel. If the bot is added to a
group where an allowed user is a member, commands execute there and the output —
including file contents — is posted to the group. Session state is keyed by chat
id, so one group is one persistent shell. Keep the bot in a private chat.

**The bot token is the whole perimeter.** A leaked token means anyone who can
reach the bot's username can act as an allowed user, and the allowlist is the
only thing between them and your shell. Rotate with `@BotFather /revoke` and
update `.env`. Note that the audit redaction is regex-based: a secret passed
without one of those labels is written to the audit log verbatim.

**External file links are public and, on `catbox.moe`, permanent** — see
[External links](#external-links). This is the only path where file content
leaves the machine to a third party.

**A service install captures the environment instead of tracking it.** The PATH
written into the definition is the one resolved when you installed, so a toolchain
installed or moved afterwards is not on the job's PATH until you re-run
`termilink service install`. This is deliberate — an init system gives a job no
environment of its own, and the alternative is a job that cannot find any tool —
but it means a machine that changes often wants reinstalling when it does.
`termilink service path` shows the current answer.

## Run as a service

### macOS (launchd)

```bash
make service-install     # or: termilink service install
```

The definition is generated from your resolved configuration, so there is no
template to fill in. `install` prints the paths it will use and the plist it is
about to write, then asks before touching anything.

Control it with:

```bash
termilink service status     # paths, plus whether launchd has it loaded
termilink service uninstall  # stop and delete the plist (logs are kept)
```

`RunAtLoad` starts the agent at login and `KeepAlive` restarts it after a crash.
That means `KeepAlive` is satisfied by "it died badly" but not by "it was asked to
stop": `SIGTERM` makes the agent save its sessions and exit 0, so a stopped agent
stays stopped, while `kill -9` brings it back within `ThrottleInterval` seconds.

There is no `termilink stop`. To stop it without removing it, ask launchd:

```bash
launchctl bootout gui/$(id -u)/com.termilink.agent   # stops; still installed
```

It comes back at the next login, because `RunAtLoad` is still true. `termilink
service uninstall` is the one that deletes the plist, and it leaves
`~/Library/Logs/termilink/` alone — a service log is the record of what the agent
did, and removing the service should not be the thing that destroys it.

While it runs under launchd, **don't** also run `termilink start` by hand — the
single-instance PID guard refuses the second one. The reverse also holds: the
agent runs shell commands as you, so `pkill -f termilink` typed into Telegram
kills the agent's own process, and launchd starts it again.

#### The PATH is the part that matters

A launchd job starts with `PATH=/usr/bin:/bin:/usr/sbin:/sbin`. That is enough
to run the binary and nothing else: no Homebrew, no `gh`, no `node`, no `java`.
Because every shell the agent spawns inherits its environment, the job would
work in a way that looks correct until you asked it to do anything real.

So the PATH is captured at install time and written into the plist. Check what
the job would get, without installing anything:

```bash
termilink service path          # the resolved PATH, and what was dropped
termilink service render        # the full plist
make service-render | plutil -lint -   # validate it
```

`service path` prints where the PATH came from and lists every entry it
discarded. Entries that do not exist are dropped, which is how sandbox and
stale paths stop being carried into a long-lived job; a literal `~` is expanded
rather than passed along, since nothing downstream would expand it.

If you install from somewhere that has a stripped environment — an IDE, a cron
job, CI — the capture falls back to your login shell, and says so. Override
either way with `--path`:

```bash
termilink service install --path "/opt/homebrew/bin:/usr/bin:/bin:$(go env GOBIN)"
```

The login shell is read with `-lic` on purpose. A plain login shell does not
read `.zshrc`, so on a machine where Homebrew or `nvm` sets itself up there, the
result has none of them.

#### Bot token

The plist carries no token. A property list in `~/Library/LaunchAgents` is
world-readable, and the generated file is meant to be pasted into a review, so
putting a credential in it would defeat the reason the job runs as you.

What matters instead is *where* the token lives. A service job starts with no
environment of its own, so a token that only ever existed in your shell is a job
that comes up and cannot connect. Two of the three places work:

| token lives in | works under launchd |
| --- | --- |
| `config.yaml`, as a literal | yes |
| `.env` beside `config.yaml` | yes — the agent loads that file at startup |
| your interactive environment | **no** |

`service install` reports which one applies, and says so before you commit:

```console
$ termilink service install
note: the bot token is in .env next to config.yaml, which the service can read.
      Keep that file at 0600: it is the one thing that grants shell access.
```

A `.env` file is the usual answer because it keeps `config.yaml` safe to paste
into an issue and the token out of it. `chmod 600` it — it is a shell credential.
And note that `.env` is read from the **config file's** directory, not from
wherever the command was run, so `termilink --config ../other/config.yaml
service install` inspects the same file the installed agent will.

### Linux (systemd)

```bash
make service-install     # or: termilink service install
```

The same generated definition, written as a systemd **user** unit rather than a
launchd plist. A user unit is the right scope for the same reason the agent runs
as you on macOS: it is a per-user gateway, and a user unit is the only kind that
can be managed without `sudo`. The unit lands in
`~/.config/systemd/user/com.termilink.agent.service`, the logs in
`~/.local/state/termilink/`, and `install` runs `systemctl --user daemon-reload`,
`enable` and `restart` in that order. If the unit will not start, `install`
disables and deletes it rather than leaving a job that is enabled and broken.

Control it with:

```bash
termilink service status     # paths, plus systemctl's view of the unit
termilink service uninstall  # stop and delete the unit (logs are kept)
```

`Restart=on-failure` with `RestartSec=10` mirrors launchd's `KeepAlive`: a crash
is restarted after ten seconds, while `SIGTERM` — which the agent catches, saves
its sessions and exits 0 — leaves it stopped. There is no `termilink stop` here
either; stop it without removing it with:

```bash
systemctl --user stop com.termilink.agent   # stops; still installed
```

It comes back at the next login, because the unit is still enabled. The PATH
capture, the generated-definition review and the bot-token rules are identical
to macOS above — the unit carries no token for the same reason the plist does
not, and `termilink service path` and `termilink service render` behave exactly
as described there. What changes is the grammar: `Environment=` instead of
`EnvironmentVariables`, `StandardOutput=append:` instead of `StandardOutPath`.

If `systemctl --user` cannot reach a running user manager — some containers, WSL
without systemd enabled — `install` fails with systemctl's own message instead of
pretending to have installed something.

### Windows

`termilink service` is implemented on macOS (launchd) and Linux (systemd). On
Windows the commands exist and explain what is missing rather than failing
silently:

```console
$ termilink service install
termilink service install is not implemented on windows yet; the planned approach is a service registration (nssm or the SCM directly)
```

The service description, the PATH resolution and the command surface are already
platform-neutral — Linux needed a renderer, not a redesign — so Windows is a
renderer to write as well. It is tracked in `project.md`.

## CLI

```bash
termilink start        # start the agent (Telegram gateway)
termilink status       # show runtime status
termilink config       # show resolved configuration
termilink projects     # list configured projects
termilink sessions     # list active sessions
termilink audit        # show recent audit log entries
termilink init         # scaffold a config file
termilink service ...  # install as a service (macOS/Linux): render/install/status/uninstall/path
```

## Development

```bash
make build
make run
make test   # CI additionally runs the race detector
```

## Architecture

| package | what lives there |
| --- | --- |
| `cmd/termilink` | cobra CLI and the `start` entry point |
| `internal/config` | YAML + `.env` loading, secret redaction |
| `internal/instance` | single-instance PID lock |
| `internal/terminal` | the PTY, the command frame and its parsers |
| `internal/telegram` | update dispatch, command handlers, per-session locks |
| `internal/session` | persisted session state |
| `internal/agent` | TUI bridge, terminal emulator, renderer |
| `internal/security` | roles, dangerous-command vet, workspace policy |
| `internal/audit` | append-only JSONL audit log |
| `internal/servicedef` | platform-neutral service description and PATH resolution |
| `internal/servicedef/launchd` | macOS plist renderer |

Three decisions are worth knowing before reading the code. **The command frame:**
every command runs in the persistent shell wrapped in `crypto/rand` markers, and
the shell appends its own exit status and cwd inside them —

```
<startTok>  <the command's output>  TLM_RC:<status>  TLM_PWD:<cwd>  <stopTok>
```

Both parsers scan **backwards** and take the last match, so a command cannot
speak for the shell's own status or directory. **One command per session, at a
time:** the Telegram library dispatches each update on its own goroutine, so the
busy state is a per-session `TryLock` rather than a flag read and set twenty lines
apart — and `/stop` and `/status` never take it, because an interrupt that waits
behind the command it exists to interrupt is not an interrupt. **Session state is
passed by value.** The Manager hands out copies (`Snapshot`, `Ensure`) and takes
writes as a closure (`Mutate`) that applies the change and persists it inside one
critical section, so the goroutine running a command and the one answering
`/status` cannot be looking at the same struct — and `/status` cannot pair a
project with a directory that belongs to a different one.

**A service job's PATH is captured, not inherited.** An init system starts a job
with a minimal environment, so the generated definition carries the PATH that
was resolved at install time. `termilink service path` shows what it resolved
to and what it dropped; install with `--path` to override. See
[Run as a service](#run-as-a-service).

## Troubleshooting

**"Unknown command" for something that looks like a command.** A leading `/`
only makes it a command if the bot knows the word; with an agent running, an
unknown one is typed into the agent as text. See
[A slash is only a command when the bot knows it](#a-slash-is-only-a-command-when-the-bot-knows-it).

**Every shell command fails on a machine without zsh.** `config.example.yaml`
sets `/bin/zsh` explicitly, which overrides auto-detection, and the path is not
checked at load time — install zsh or point `terminal.shell` at your own shell.

**A second `termilink start` is refused.** The PID lock at
`~/.termilink/termilink.pid` is held by another instance — stop it, or delete the
file if you are sure nothing is running. Note that under launchd the instance you
want to stop *is* the service: `launchctl bootout gui/$(id -u)/com.termilink.agent`.

**Everything in `~/.termilink/` is private.** The directory is `0700` and its
files `0600`, on the reasoning that the audit log records every command you ran
and the state file holds the chat id of every conversation and the path of every
directory the agent has touched. That is enough to describe your work to another
account on a shared machine without revealing any of it. An older build wrote
some of it as `0644`; the permissions are corrected on the next write, or run
`chmod 700 ~/.termilink && chmod 600 ~/.termilink/*`.

**`agent` says the project is not set.** It runs inside the selected project
directory, so send `/project <name>` first.

**`get` sent a search instead of the file you meant.** An argument that looks
like a path to an existing file is a file; anything else is a keyword matched
against artifact names. `get ./release/app.apk` is that file, `get release/14` is
a search for `release/14.apk`. See [Downloading](#downloading).

**A file over 50 MB was not sent.** Without `big_file_link_host` set it is
refused and nothing leaves the machine; with a host set it needs an owner
approval and the link is **public**. See [Size limits](#size-limits).

**The bot went quiet.** `termilink audit | grep panic` first: a contained fault
is recorded there and the gateway is still running.

## Roadmap

Ordered by what is actually queued, not by wish list. Nothing here is promised
on a date.

**Next up** — these need you, not the code

- **Second-user verification** — the worker path has never been exercised with a
  real second Telegram account, which is also the only thing that would prove the
  branch ruleset gates anything.
- **Bot token rotation** — the token in use was shared in chat during
  development. Deferred until the deployment is settled, but a real exposure.
- **Coverage reporting in CI** — not wired. Overall coverage is 69%; `cmd/termilink`
  and `cmd/termilink/cli` have no tests at all (0%), and the thinnest covered
  packages are `internal/instance` (67%), `internal/agent` (70%) and
  `internal/telegram` (71%).

**Deliberately later**

- **Scheduled tasks** — the one thing the agent CLIs cannot do: a job that runs
  when no agent session is open. Everything else on this list is either already
  here or a few lines of template.
- **Service packaging for Linux and Windows** — `termilink service` currently
  implements macOS only, and says so on the other two. The service description,
  the PATH resolution and the command surface are already platform-neutral, so
  this is a renderer each: a systemd user unit for Linux, an SCM or nssm
  registration for Windows. `state_file` and `audit_keep` are in place for the
  case that matters most there, an account with no home directory.

**Not planned**

Each of these was queued once. The reason it came off is kept here so it does not
get re-proposed:

- **Multiple concurrent agent sessions.** The agent CLIs already persist their own
  sessions per working directory, so `/project` and `cd` are the switch you
  actually want. A second TermiLink-side session would duplicate what the agent
  already does, and two screen relays would interleave in one chat. Rate limits
  are not the reason — the live relay edits a single message rather than posting
  one per frame, so it costs one message per session no matter how long it runs.
- **Git worktree support.** It exists to keep concurrent sessions off each other's
  files. With one session per chat there is nothing to isolate.
- **Automated build/test loops, `git status` inspection, PR workflows.** The agent
  has a shell: it runs `npm test`, reads `git status` and calls `gh pr create` on
  its own. A Telegram command for each would be a worse version of a tool the
  agent already has. `get` already delivers build artifacts, so that half shipped.
- **Gateway mode and device management.** TermiLink stays on one machine, which is
  also the security argument: a leaked bot token reaches the one machine TermiLink
  runs on, but behind a gateway the same token reaches every machine you have
  connected. A bad trade for one chat instead of two.
- **Web dashboard.** One operator, one machine, and Telegram is the interface that
  already works. A dashboard is a second interface to build, secure and test, in
  order to show you data you can already read.
- **Any hosted or multi-tenant mode.** This is a single-operator tool: it runs as
  your user account, on your machine, with your bot token. If you need an OS
  sandbox around it, run it inside a VM or container — the process boundary is
  the real one, and the workspace policy in
  [Known limitations](#known-limitations) is not a substitute for it.

## Contributing

`master` is **protected**: outside contributors go through a pull request, and
the repository owner is deliberately exempt so a stale branch is never a reason to
skip the checks. CI runs `ubuntu-24.04` and `macos-latest` with `fail-fast:
false`, and each job runs `gofmt` → `go build` → `go vet` → `go test -race
-timeout 15m`, ordered cheapest first; `staticcheck` is pinned at `v0.8.1` and
runs once, on the Linux job. Green in both is the bar for a pull request, though
these are **not currently enforced as required status checks** and the ruleset has
never actually gated a merge.

The Linux label is pinned to `ubuntu-24.04` rather than `ubuntu-latest`: these
tests drive a real `zsh` over a PTY and read the terminal's line settings, so they
are sensitive to what the runner image ships — enough that they broke on an image
the local machine could not reproduce. `ubuntu-latest` moves from 24.04 to 26.04
between 19 Oct and 19 Nov 2026, so bumping it should be a deliberate edit and a
visible failure, not a surprise on a Tuesday. Windows is absent on purpose —
`creack/pty` compiles there but `StartWithSize` returns `ErrUnsupported` at
runtime, so every PTY test would fail on a runner that could never be made to
pass.

## License

MIT. See [LICENSE](LICENSE).