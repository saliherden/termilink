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
| `/ping` | Health check |

A configured project command name (e.g. `build`) runs its shortcut command.

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

1. Bot replies: `⚠️ Dangerous command detected: … — reply evet to approve or
   hayır to reject (2m0s). The command will not run until approved.`
2. **Owner** replies `evet` (or `yes` / `ok` / `onay`) → the command runs.
   `hayır` (or `no` / `cancel` / `iptal`) → rejected, **never executed**.
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

## CLI

```bash
termilink start        # start the agent (Telegram gateway)
termilink status       # show runtime status
termilink config       # show resolved configuration
termilink projects     # list configured projects
termilink sessions     # list active sessions
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