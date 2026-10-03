# Security

[← Back to README](../README.md)

## Roles

Owner — full access: free `cd`, all commands, and the only user that can
approve dangerous commands or control an agent session. Workers — restricted:
cannot `cd`, must select a project first, and are bound to the workspace policy.
See [Known limitations](#known-limitations) before adding a second user.

## Dangerous command approval

Configured as `security.approve_dangerous` and `security.dangerous_patterns`
(see [Configuration](configuration.md#security)). When a message matches a
dangerous pattern it is queued, not executed:

1. Bot replies: `⚠️ Dangerous command detected: … — reply yes / evet / ok to
   approve or no / hayır to reject (2m0s). The command will not run until
   approved.`
2. Owner replies `yes` (or `evet` / `ok` / `onay`) → the command runs.
   `no` (or `hayır` / `cancel` / `iptal`) → rejected, never executed.
3. Any other reply keeps the request pending. If no decision arrives within 2
   minutes the request expires — the command is not executed.

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

## Workspace policy (workers)

Configured as `workspace.allowed` (see
[Configuration](configuration.md#workspace)). When it is non-empty, workers may
only access paths under one of the allowed roots: out-of-scope absolute paths,
`~`/`$HOME` paths and `..` escapes in their commands are rejected, and `/project`
refuses projects outside the allowed roots. The owner is never bound by this
policy. Empty — the default — means no restriction at all, so read
[Known limitations](#known-limitations) before adding a second user.

## Audit logging

Configured as `security.audit_log` and `security.audit_max_bytes` (see
[Configuration](configuration.md#security)). Every security-sensitive event is
appended as one JSON line per event: commands (with result, duration and exit
state), the danger-approval flow, whitelist rejections, workspace vet blocks,
project switches, `/input`, `/stop` and `/exit`.

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

## Known limitations

This runs arbitrary commands as your user account, so the boundary of what it
actually enforces matters more than the feature list. Each item below is a
deliberate, current limitation, not a roadmap promise.

The workspace policy is not a sandbox, and is not scoped by default. With
`workspace.allowed` empty — the default — there is no filesystem restriction at
all: any allowed user can read and write anything the process can. Even when you
do configure it, the guard is a text-level check over the command's tokens, so
relative paths, command substitution (`$(cd /etc && …)`) and symlinks pass
straight through. Set `workspace.allowed` before adding a second user, and treat
workers as trusted.

The dangerous-command gate is a regex, not a policy engine. It matches eight
built-in patterns against the raw text, so a command that reaches the same effect
without matching one — `find … -delete`, `git clean -fdx`, a script that does the
work — is not gated. With `approve_dangerous: worker` the owner is exempt
entirely. Approval means "the owner said yes in chat"; once given, the command
runs with the full privileges of the user who started the gateway.

The owner is unrestricted by design. The owner may `cd` anywhere, is not
bound by `workspace.allowed`, and is the only role that can approve a dangerous
command or control an agent session. There is no second tier of privilege above
or below that.

There is no chat-type or chat-allowlist check. Authorization is keyed on the
Telegram user id alone (`security.allowed_users`); nothing inspects whether the
message arrived in a private chat, a group or a channel. If the bot is added to a
group where an allowed user is a member, commands execute there and the output —
including file contents — is posted to the group. Session state is keyed by chat
id, so one group is one persistent shell. Keep the bot in a private chat.

The bot token is the whole perimeter. A leaked token means anyone who can
reach the bot's username can act as an allowed user, and the allowlist is the
only thing between them and your shell. Rotate with `@BotFather /revoke` and
update `.env`. Note that the audit redaction is regex-based: a secret passed
without one of those labels is written to the audit log verbatim.

External file links are public and, on `catbox.moe`, permanent — see
[External links](files.md#external-links). This is the only path where file
content leaves the machine to a third party.

A service install captures the environment instead of tracking it. The PATH
written into the definition is the one resolved when you installed, so a toolchain
installed or moved afterwards is not on the job's PATH until you re-run
`termilink service install`. This is deliberate — an init system gives a job no
environment of its own, and the alternative is a job that cannot find any tool —
but it means a machine that changes often wants reinstalling when it does.
`termilink service path` shows the current answer.
