# Agent (TUI bridge)

[← Back to README](../README.md)

TermiLink does not reimplement your coding agent. `agent <prompt>` launches the
real CLI — OpenCode, Claude Code, Codex or Gemini CLI — inside a pseudo-terminal
rooted at the selected project directory (you need `/project <name>` first), and
relays its screen into the chat. You drive the actual TUI from your phone: every
message you send is typed into its input box, colors and keyboard shortcuts
included, and when the session ends you get the final screen and the id to
resume it at your desk.

```
🤖 Agent started.

Project: myapp
Working dir: /Users/me/code/myapp
PID: 31337
```

While a session is active, every message you send is typed into the agent's
input box (Enter appended), so the workload runs interactively inside the
agent. Each message starts a new turn. Set `agent.screen.mode: text` to drop the
live images and get copyable text for the whole session.

## What arrives in the chat, and as what

The medium follows what the thing is, not how wide the screen is:

| | sent as | why |
| --- | --- | --- |
| live screen (every frame) | photo | re-uploaded ~3×/second; a document per frame would drop a file card into the chat on every tick |
| final screen (`/agent stop`, `/agent exit`, `/exit`, or the agent exiting on its own) | text, a code block | it is written output, not a picture of one — copyable, searchable, pasteable |
| scrollback reader (`/agent history`) | document | a page of scrollback is as wide as the TUI, and Telegram does not scale documents, so it opens at native resolution |

The final screen is the window the TUI was showing at the moment the agent
stopped, not the whole conversation: your prompts scroll up and off the top of
the 40-row window, so it holds the tail of the session. For the whole thing, use
`/agent history`.

## Keys

| You type… | Meaning |
| --- | --- |
| plain text | typed into the agent input box, Enter pressed |
| `^p`, `^c`, … | Ctrl-key (`^p` = Ctrl+P → command palette) |
| `↑` `↓` `←` `→` | arrow keys |
| `up`, `down`, `left`, `right`, `esc`, `tab`, `enter`, `backspace`, `delete`, `home`, `end`, `pageup`, `pagedown`, `insert` | the named special key, sent as real key bytes |
| `^x l` | key chord: `Ctrl+X` then `l` (switch session) — opencode uses `ctrl+x` chord shortcuts like `ctrl+x n` (new session), `ctrl+x m` (switch model) |
| `^p enter` | chord that ends with Enter (select the highlighted palette entry) |

Navigation inside panels is `↑`/`↓` + `enter` to select, `esc` to close. Key
names go in without a slash — `up`, not `/up`, for the reason given in
[A slash is only a command when the bot knows it](commands.md#a-slash-is-only-a-command-when-the-bot-knows-it).

## The scrollback reader

`/agent history` opens it; `/agent up` and `/agent down` move one screenful at a
time and repaint the same message in place, `/agent top` and `/agent bottom` jump
to either end, and `/agent history exit` (short: `/agent off`) drops the hint
line and leaves the image in the chat to re-read later.

It is a single message showing up to 40 rows rather than a set of numbered
pages, and it renders in the agent's own colors. Consecutive screens overlap by
four lines, so walking to the top shows every line at least once; near the top
the window is allowed to become shorter than 40 rows and shows what is left
above rather than skipping it. The view spans the lines that scrolled off plus
the rows currently on screen, so scrolling up from the bottom never lands in a
gap; the scrollback itself is capped at the last 2000 distinct lines. Scrolling
is slash-only, so the bare words `up` and `down` stay reserved for the TUI's
arrow keys. This is how you re-read a long reply while the session keeps running.

Only the owner can start an agent or write into one — the running agent has
the same machine permissions as the bot user, so it is never exposed to workers.
