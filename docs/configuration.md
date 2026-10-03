# Configuration

[← Back to README](../README.md)

`config.yaml` at `~/.termilink/config.yaml` by default, plus `.env` beside it
(auto-loaded at startup). Pass `--config <path>` to read another file; the `.env`
is always taken from that file's directory.
Every key, its type and its effective default:

## Which shell is used

`terminal.shell` in `config.yaml` decides. Left unset, TermiLink picks the first
of `/bin/zsh`, `/bin/bash`, `/bin/sh` that exists on the machine.

⚠️ The example config sets `/bin/zsh` explicitly — `termilink init` writes it
that way — so it overrides that auto-detection. On a machine without zsh, every
shell command then fails: the path is not checked at load time, so the error only
surfaces once you send the first command. Install zsh (`apt install zsh`) or
point that one line at your own shell.

## `telegram`

| key | type | default | notes |
| --- | --- | --- | --- |
| `bot_token` | string | — required | either inline or `${TELEGRAM_BOT_TOKEN}`; start fails if neither resolves |
| `max_file_bytes` | int | `52428800` (50 MiB) | the send/zip decision threshold; must be > 0 |
| `big_file_link_host` | string | `""` (off) | `uguu.se` or `catbox.moe`; anything else is rejected at load |

Secrets normally live in `.env` rather than `config.yaml` so the config stays safe
to paste into an issue. `termilink init` writes a `.env.example` next to it.

## `security`

| key | type | default | notes |
| --- | --- | --- | --- |
| `owner` | int | first entry of `allowed_users` | automatically appended to the allowlist if missing |
| `allowed_users` | []int | — required | at least one Telegram user id; everyone else is rejected |
| `approve_dangerous` | string | `all` | `all` \| `worker` \| `off` |
| `dangerous_patterns` | []string | none | extra regexes, OR-ed with the built-in list |
| `audit_log` | string | `~/.termilink/audit.log` | absolute path, or `off` to disable auditing |
| `audit_max_bytes` | int | `0` (unlimited) | past this, the file rotates to `<path>.1` before the next entry |
| `audit_keep` | int | `1` | how many rotated archives to retain; below 1 is clamped to 1 |

See [Security](security.md) for what these actually enforce.

## `workspace`

| key | type | default | notes |
| --- | --- | --- | --- |
| `allowed` | []string | `[]` | empty means no filesystem restriction at all — see [Known limitations](security.md#known-limitations) |

## `terminal`

| key | type | default | notes |
| --- | --- | --- | --- |
| `shell` | string | first of `/bin/zsh`, `/bin/bash`, `/bin/sh` that exists, else `/bin/sh` | must not be empty |
| `command_timeout` | duration | `30m` | applied as a deadline around each command; a timeout is reported, not silently dropped |
| `max_output_bytes` | int | `1048576` (1 MiB) | raised to at least `65536`; over-long output keeps the most recent bytes, and the exit status is still exact |

## `agent`

| key | type | default | notes |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | gates the whole feature |
| `command` | string | `""` (auto-detect) | searches `opencode`, `claude`, `codex`, `gemini` on `PATH`; a bare name or absolute path pins one. `~` is not expanded — write the full path |
| `screen.mode` | string | `png` | `png` = live screen as a color photo, `text` = code block. The screen posted when a session ends is always text |

See [Agent (TUI bridge)](agent.md).

## `projects`

| key | type | default | notes |
| --- | --- | --- | --- |
| `<name>.path` | string | — | working directory for `/project <name>` |
| `<name>.commands` | map[string]string | — | named shortcuts |
| `<name>.artifacts` | []string | — | paths collected by `get` |
| `<name>.env` | map[string]string | — | exported into that project's shell; keys may not contain `=` or a newline, values no newline or NUL |

## `state_file` (top level)

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
directory, nothing configured) the agent refuses to start rather than
degrading to in-memory sessions, which would look healthy and forget everything
on restart. `state_file` is what makes a service install work when the account
it runs under has no usable home directory.
