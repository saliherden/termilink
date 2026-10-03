# Telegram Commands

[← Back to README](../README.md)

Everything that is not a command is executed in the persistent shell.

## Direct terminal

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

## Agent session

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

## Closing things — three scopes, three commands

The names are deliberately distinct so nothing closes something you did not mean
to close:

| Command | Closes | Leaves running |
| --- | --- | --- |
| `/agent history exit` | the reader's hint line | the agent session |
| `/agent exit` | the agent session | the shell |
| `/exit` | everything — the agent session and the shell | nothing, and the project binding is cleared too |

`/exit` is the "shut it all down and start over" command. It stops the agent
before closing the shell, so TermiLink can capture its final visible screen, then
clears the project binding, the working directory and the last command.
`/agent stop` and `/agent exit` capture that same final screen but leave the
shell and the project in place, so every path that ends an agent session — those
two, `/exit`, or the agent exiting on its own — leaves a readable record behind.

## The exit message carries the session id

Once the agent has closed, TermiLink asks its CLI which session that run was and
posts the id with the command that reopens it:

```
🆔 Session `ses_f318e258fffe5G8PbTz5Zh` — resume with `opencode -s ses_f318e258fffe5G8PbTz5Zh`
```

Only opencode-compatible CLIs answer (`session list --format json`); one that
does not understand the subcommand is detected once and the feature steps aside.
The id is reported only if exactly one session was created during the run —
never guessed, never invented.

## A slash is only a command when the bot knows it

A leading `/` is treated as a command only when it matches a known command.
Unknown slash-prefixed messages are passed to the running agent as text; without
an agent they return `⚠️ Unknown command`. Special TUI keys are typed without a
slash — `up`, `down`, `esc`, `enter` — so they can never collide with a real
command.
