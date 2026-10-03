# Architecture

[← Back to README](../README.md)

| package | what lives there |
| --- | --- |
| `cmd/termilink` | cobra CLI and the `start` entry point |
| `internal/config` | YAML + `.env` loading, secret redaction |
| `internal/instance` | single-instance PID lock, process liveness |
| `internal/terminal` | the PTY, the command frame and its parsers |
| `internal/telegram` | update dispatch, command handlers, per-session locks |
| `internal/session` | persisted session state |
| `internal/agent` | TUI bridge, terminal emulator, renderer |
| `internal/security` | roles, dangerous-command vet, workspace policy |
| `internal/audit` | append-only JSONL audit log |
| `internal/scaffold` | embedded `config.example.yaml` / `env.example` for `termilink init` |
| `internal/servicedef` | platform-neutral service description and PATH resolution |
| `internal/servicedef/launchd` | macOS plist renderer |
| `internal/servicedef/systemd` | Linux systemd user unit renderer |
| `internal/version` | the build version, injected at release time |

Three decisions are worth knowing before reading the code. The command frame:
every command runs in the persistent shell wrapped in `crypto/rand` markers, and
the shell appends its own exit status and cwd inside them —

```
<startTok>  <the command's output>  TLM_RC:<status>  TLM_PWD:<cwd>  <stopTok>
```

Both parsers scan backwards and take the last match, so a command cannot
speak for the shell's own status or directory. One command per session, at a
time: the Telegram library dispatches each update on its own goroutine, so the
busy state is a per-session `TryLock` rather than a flag read and set twenty lines
apart — and `/stop` and `/status` never take it, because an interrupt that waits
behind the command it exists to interrupt is not an interrupt. Session state is
passed by value. The Manager hands out copies (`Snapshot`, `Ensure`) and takes
writes as a closure (`Mutate`) that applies the change and persists it inside one
critical section, so the goroutine running a command and the one answering
`/status` cannot be looking at the same struct — and `/status` cannot pair a
project with a directory that belongs to a different one.

A service job's PATH is captured, not inherited. An init system starts a job
with a minimal environment, so the generated definition carries the PATH that
was resolved at install time. `termilink service path` shows what it resolved
to and what it dropped; install with `--path` to override. See
[Run as a service](service.md).
