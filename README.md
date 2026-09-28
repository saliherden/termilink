# TermiLink

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

- Go 1.27.1+ (as required by `go.mod`)
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

There is no `/agent close`: it used to hide the scrollback hint line, which is
nothing like what the name suggests, and one name per action is worth more than a
second spelling. Closing the session is `/agent exit`.

### Closing things — three scopes, three commands

The names are deliberately distinct so nothing closes something you did not mean
to close:

| Command | Closes | Leaves running |
| --- | --- | --- |
| `/agent history exit` | the reader's hint line | the agent session |
| `/agent exit` | the agent session | the shell |
| `/exit` | **everything** — the agent session *and* the shell | nothing, and the project binding is cleared too |

`/exit` is the "shut it all down and start over" command. If an agent session is
running it is torn down too, and its **final screen is posted as text** first. The
agent goes first on purpose: it holds its own PTY, and its screen has to be read
while the TUI is still alive — `Close` interrupts it, and an interrupted TUI
clears the screen and prints a goodbye banner over the session. The audit log
records the agent stop as `agent_stop` with `cmd="exit via /exit"`, so the two
closures stay distinguishable.

`/agent stop` and `/agent exit` post that same final screen and stop there, so
every path that ends a session — `/exit`, `/agent stop`, `/agent exit`, or the
agent exiting on its own — leaves a readable record behind, followed by the
session id. That was not consistently true: `/agent stop` used to send only a
text line, and it also read the screen *after* interrupting the TUI, so it
reported the shutdown banner rather than the session.

It also **resets the context**: the project binding, the working directory and
the last command are cleared, so the next plain message starts fresh in your
home directory. That part is not cosmetic — see
[`/exit` did nothing you could see](#exit-did-nothing-you-could-see).

### Photos while it runs, text when it stops

The live screen is relayed as a **photo**. That is the right medium for it: the
frame is re-uploaded about three times a second, a document per frame would drop
a file card into the chat on every tick, and the reader is watching a moving
screen and opens it deliberately.

The final screen is **text**. When the agent stops — via `/agent stop`,
`/agent exit`, `/exit`, or by exiting on its own — the screen is posted as a code
block, because that is what the output actually is. A rendered PNG of the same
screen is neither copyable nor searchable, and a terminal transcript is something
you paste into an editor, quote to somebody, or grep later. The photo stays for
the one thing a photo is good at: showing what it looked like while it ran.

So the two are deliberately different, and the split is about *use*, not about
how wide the screen is:

| | sent as | why |
| --- | --- | --- |
| live screen (every frame) | **photo** | re-uploaded ~3×/second; a document per frame would drop a file card into the chat on every tick |
| final screen (`/agent stop`, `/agent exit`, `/exit`, or the agent exiting on its own) | **text**, a code block | it is written output, not a picture of one — copyable, searchable, pasteable |
| scrollback reader (`/agent history`) | **document** | a page of scrollback is as wide as the TUI, and Telegram does not scale documents, so it opens at native resolution |

The exit text is the **visible screen at the moment the agent stopped**, read
*before* the session is interrupted. Both halves of that matter. `Close` sends
Ctrl-C and SIGINT, and a real TUI answers that by clearing the screen and
printing a goodbye banner — so a screen read afterwards is a perfectly correct
transcript of the shutdown, and a couple of stray words is exactly what the owner
saw. Reading first costs nothing: the process is alive to be interrupted either
way. `/agent stop` used to do the opposite, and the two paths disagreed about it,
which is why the same session could report two different things depending on how
it was closed.

The limitation worth stating: this is the window the TUI was showing, not the
whole conversation. Your prompts scroll up and off the top of the 40-row window,
so the exit text holds the tail of the session. For the whole thing, use
`/agent history` — that is what the reader is for.

### The exit message carries the session id

Once the agent has closed, TermiLink asks its CLI which session that run was and
puts the id in the chat, together with the command that reopens it:

```
🆔 Session `ses_f318e258fffe5G8PbTz5Zh` — resume with `opencode -s ses_f318e258fffe5G8PbTz5Zh`
```

The id is not scraped out of the TUI — there is nothing there worth scraping, and
a screen reader is the wrong place to go looking for an identifier. The CLI is
asked directly, after the run, and only opencode-compatible CLIs answer
(`session list --format json`); a CLI that does not understand the subcommand is
detected once and the feature steps aside quietly from then on. Nothing about the
exit depends on it — the screen text has already been sent by then, and the id
follows it as a separate message precisely so a slow or unsupported CLI cannot
delay the output.

Where the project already has sessions, deciding which row belongs to the run is
a real question, so the answer is deliberately conservative. Rows created during
the run's own lifetime are collected, and the id is reported **only if exactly
one** of them exists. Zero means the agent exited before its first turn.
More than one means something else started a session in the same directory at the
same moment, and TermiLink declines to guess: a wrong id looks authoritative and
walks you into the wrong conversation, which is worse than reporting none. No id
is ever invented or filled in.

### A slash is only a command when the bot knows it

A leading `/` does **not** by itself make a message a command. The bot matches
the first word against its command table:

- **known command** → it runs (`/status`, `/agent up`, `/exit`, …)
- **unknown slash word, agent running** → the whole message is typed into the
  agent, so `/update.sh`, `/etc/hosts has the entry` or a mistyped
  `/agent stpo` reach the TUI as text instead of being refused
- **unknown slash word, no agent** → `⚠️ Unknown command`, as before

This is why there is no `/up` command. Arrow keys, `esc`, `enter` and friends are
sent by typing the **bare word** — `up`, `down`, `esc`, `enter` — which can never
collide with a real command, and the slash variants were dropped because Telegram's
command autocomplete rewrites the input box around them and because the old
`/up <text>` handling silently discarded the trailing text. Known commands always
win, so the pass-through never shadows one.

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
agent. Each message starts a new turn: its screen is posted as a **colored image
screenshot** and live-updated in place while the agent renders. When the session
ends, the screen you are left with is plain text.

Which medium is used is decided by what the thing is, not by how wide the screen
is: a photo for the moving screen, text for written output, and a document for
the reader. The reasoning is in
[photos while it runs, text when it stops](#photos-while-it-runs-text-when-it-stops).
Set `agent.screen.mode: text` to drop the live images as well and get copyable
text for the whole session.

Supported keys:

| You type… | Meaning |
| --- | --- |
| plain text | typed into the agent input box, Enter pressed |
| `^p`, `^c`, … | Ctrl-key (`^p` = Ctrl+P → command palette) |
| `↑` `↓` `←` `→` | arrow keys |
| `up`, `down`, `left`, `right`, `esc`, `tab`, `enter`, `backspace`, `delete`, `home`, `end`, `pageup`, `pagedown`, `insert` | the named special key, sent as real key bytes |
| `^x l` | **key chord**: `Ctrl+X` then `l` (switch session) — opencode uses `ctrl+x` chord shortcuts like `ctrl+x n` (new session), `ctrl+x m` (switch model) |
| `^p enter` | chord that ends with Enter (select the highlighted palette entry) |

Type the key names **without a slash** — `up`, not `/up`. Anything the bot does
not recognize as a command is passed to the agent as text, so a leading slash
never hides what you wrote, and the bare words can never be mistaken for a
command.

Navigation inside panels is **`↑`/`↓` + `enter` to select, `esc` to close**.

Session management: `/agent status` shows the run, `/agent stop` (or
`/agent exit`) closes it gracefully, and **`/agent history`** opens the scrollback
reader. The reader is a
single message you *scroll* rather than a set of numbered pages: it starts at the
newest line, `/agent up` and `/agent down` move one screenful at a time and
repaint that same message in place, `/agent top` and `/agent bottom` jump to
either end, and `/agent history exit` (short: `/agent off`) drops the hint line
and leaves the image in place so you can keep re-reading it — reopening with
`/agent history` brings the hints back. Scrolling is deliberately slash-only —
the bare words `up` and `down` stay reserved for the TUI's arrow keys, so
scrolling can never swallow a key you meant for the agent. The view spans
everything the TUI has drawn — the lines that scrolled off *plus* the rows
currently on screen, so scrolling up from the bottom never hits a gap. Consecutive
screens overlap by four lines, which means walking to the top shows every line at
least once; the scrollback itself is capped at the last 2000 distinct lines. The
reader renders in the agent's own colors with a `AGENT SCROLLBACK` title bar
naming the visible slice, so the image reads like a terminal window rather than a
flat wall of text, and it arrives as an image in `png` mode or a code block in
`text` mode.
Because a long assistant reply can be far longer than the 40-row terminal window,
this is how you re-read it while the session keeps running. **Only the owner**
can start an agent or write into one — the running agent has the same machine
permissions as the bot user, so it is never exposed to workers.

```yaml
agent:
  enabled: true            # false disables the whole feature
  command: ""              # "" = auto-detect (opencode → claude → codex → gemini)
                           #     or a name on PATH ("opencode") or an absolute
                           #     path ("/usr/local/bin/opencode").
                           #     `~` is NOT expanded — write the full path.
  screen:
    mode: png              # "png" = colored screenshot, "text" = code block
                           # (live screen only; the final screen is always text)
```

### How the scrollback reader works

The reader is not a transcript recorder. It is built from the same terminal
emulator that draws the live screen, so it inherits the agent's real colors,
panels and box drawing instead of approximating them:

1. **The screen keeps a scrollback.** `Screen.persistScroll` runs on every frame
   and moves the rows that *left* the visible window into a transcript. Each
   distinct line is stored at most once, so a TUI that repaints its status line
   or animates a spinner does not fill the buffer with duplicates. Lines shorter
   than three characters are skipped as spinner noise, and the transcript is a
   FIFO capped at the last **2000** lines.
2. **Rows are stored as cells, not text.** A captured row is a `Row`: its plain
   `Text` plus one `RowCell` per rune carrying the foreground and background in
   effect when it was written. This is what makes the reader colored — an
   earlier version kept only the text and had to be drawn in a single color.
3. **The view is transcript + live rows.** `Session.ViewRows` returns the
   transcript followed by the rows still on screen, de-duplicated against
   adjacent repeats. The live rows are *appended*, not snapshotted, so scrolling
   up from the bottom never lands in a gap between what scrolled away and what
   is displayed now.
4. **One message, repainted in place.** The reader is a single Telegram message
   showing up to 40 rows. `/agent up` and `/agent down` move the window by 36
   lines — 4 lines of overlap, so no line is ever skipped and every line is seen
   at least once on the way to the top. Near the top the window is allowed to
   become **shorter** than 40 rows and shows what is actually left above, rather
   than refusing to move: forcing a full page there used to skip the lines in
   between (at 57 lines, one `/agent up` jumped from 57 to 40 and hid 41–57 for
   good). Each move re-renders and edits that same message, so a long read never
   floods the chat and the live screen relay keeps updating in parallel.
5. **The image gets a title bar.** `RenderRowsPNG` draws the rows with their own
   colors plus a `AGENT SCROLLBACK · <first>–<last> of <total>` strip, so the
   picture reads as a terminal window and always says where you are.
6. **It is uploaded as a document, and still repainted in place.** A page is as
   wide as the TUI, so a photo of it would be scaled down to the chat bubble and
   be illegible. Uploading a document instead costs nothing: `editMessageMedia`
   takes a document as well as a photo, so the "one message, scrolled" design is
   unaffected.

There is deliberately no `/agent close` alias for this. It once existed, and it
only hid the hint line — a name that reads like "shut the agent down" while doing
something much smaller, which is the worst kind of surprise a command can offer.
`/agent history exit` (short: `/agent off`) only clears the hint line and
repaints the current view, so the image stays in the chat to re-read later; the
agent session keeps running either way. Reopening with `/agent history` — or
scrolling again with `/agent up` — brings the hints back.

Twelve bugs worth knowing about, all fixed and all now covered by tests:

- **The alias path swallowed text.** `/up`, `/down`, `/esc` … used to be commands
  of their own that forwarded a key to the TUI. Anything after the key word —
  `/up uygula` — was parsed and then **discarded without a word**, so the text
  you had typed vanished and only a bare arrow reached the agent. They also
  collided with Telegram's command autocomplete, which rewrites the input box
  around a slash command. The bare key words do the same job and cannot collide
  with anything, so the slash commands are gone; see
  [a slash is only a command when the bot knows it](#a-slash-is-only-a-command-when-the-bot-knows-it).
- **The alias path skipped the owner check.** It wrote to the TUI without
  verifying who asked, so a non-owner could type into the owner's agent session
  — the one place where the owner-only rule actually matters, since the agent
  runs with the bot user's own permissions. The regression test types `/enter`
  as a worker and asserts the fixture never echoes it.
- **The renderer was shared across goroutines.** The live screen and the
  scrollback reader both draw through `drawGlyph`, and they both used one
  global `font.Face`. An `opentype.Face` is **not** safe for concurrent use —
  every `Glyph` call rewrites the face's own sfnt buffer, mask image and vector
  rasterizer. A relay frame landing on top of an `/agent history` render
  corrupted the mask bounds and the process died with
  `panic: slice bounds out of range … [104:42]` inside `vector.fixedLineTo`,
  taking the whole bot with it and leaving every later command unanswered.
  `renderMu` now serializes glyph drawing;
  `TestRenderConcurrentWithScreen` renders both paths from 8 goroutines and
  fails under `-race` without it.
- **A panic killed the bot silently.** The Telegram library dispatches each
  update on its own goroutine and nothing recovered from it, so any fault in any
  handler stopped the entire gateway. Update handling now recovers, writes the
  panic and its stack to the log and the audit trail as `panic`, and tells the
  owner the bot is still running. The relay does the same per frame and falls
  back to text for the rest of the session.
- **`/agent exit` could answer nothing.** Tearing an agent session down is
  claimed by whichever goroutine arrives first: the command, or the relay, which
  fires `onAgentExit` when the process exits on its own. If the command lost that
  race it returned without sending anything, so the session was closed, the
  audit log had recorded `agent_stop`, and the owner saw silence — the command
  looked broken. Both losing paths now say what happened, and
  `TestAgentCleanupSingleWinner` pins the invariant that exactly one goroutine
  can win a teardown, so the two paths never announce one closure twice.
- **`/exit` left the agent running.** The agent opens its own PTY and is tracked
  in a separate map from the persistent shell, so `/exit` — which only tore down
  the `zsh -i` session — left the agent process alive and its screen frozen
  mid-turn. `/exit` now closes both and posts the agent's final screen as text
  before the shell goes down; `/agent exit` is unchanged and still closes only the
  agent.
<a id="exit-did-nothing-you-could-see"></a>

- **`/exit` did nothing you could see.** It closed both processes and left the
  session state alone, and because the state is what `getShell` reads, the very
  next plain message opened a fresh shell **in the same directory, under the same
  project** — a new pid and nothing else. The audit log showed an `exit`; the chat
  showed a working session, indistinguishable from before. Worse, `/exit` was
  reaching for a job `/stop` (a running command) and `/agent stop` (the agent)
  already do, which left "reset my context" as the one thing only `/exit` could
  mean. `/exit` now clears the project binding, the working directory and the last
  command, and says so. The working directory is set to `$HOME` explicitly rather
  than left empty, because an empty `Cwd` falls back to the *gateway process's*
  working directory — wherever the bot happened to be started, not a predictable
  "home". `TestExitResetsContext`, `TestExitResetSurvivesRestart` (the state file
  is reloaded, so the reset has to outlast a restart) and
  `TestCommandAfterExitStartsUnbound` (the next command comes back with no
  `TERMILINK_PROJECT`) pin it.
- **The final screen was an image, and now it is text.** A chain of reports: an
  unreadable thumbnail, then a magnified scrap, then a couple of stray words.
  Each was chased as a rendering problem and none of them was one. The exit
  announcement was reading a *picture* of a terminal, and a picture of a
  terminal cannot be copied, searched, or pasted into an editor — which is what
  the owner wanted from it all along. The exit now posts the screen as a code
  block, read from the visible window before the session is interrupted, and the
  session id alongside it so the conversation can be reopened
  (`opencode -s <id>`). The live relay keeps sending photos: that is a picture of
  a moving screen and belongs in a bubble. `TestExitSendsTheAgentScreenAsText`
  and `TestExitScreenIsReadBeforeTheTUIShutsDown` pin the new behaviour;
  `TestRelayStillSendsPhotos` pins the half that did not change.
- **Reading the screen at close was a race with the shutdown, and `/agent stop`
  lost it every time.** `Close` sends Ctrl-C and SIGINT; a TUI answers by
  clearing and drawing a banner. `/exit` read the screen first, but
  `handleAgentStop` called `Close` *before* reading, so `/agent stop` reported
  the shutdown — which is what "a couple of stray words" actually was. The two
  paths had disagreed about the ordering since the announcement was first wired
  up. `captureAgentExitScreen` is now called before `Close` on every path.
  `TestExitScreenIsReadBeforeTheTUIShutsDown` uses a fixture that answers SIGINT
  the way a TUI does, and asserts the delivered message contains the session and
  not the banner.
- **A reported session id can be wrong, which is worse than none.** The id is not
  read off the screen; the CLI is asked directly once the run is over, which
  means choosing between the project's existing sessions. Recency alone would be
  a guess. The lookup collects the sessions created during the run's own lifetime
  and reports an id **only when exactly one** of them exists — zero means the
  agent exited before its first turn, and more than one means something else
  started a session here at the same moment. `resolveAgentSessionID` returns ""
  in both cases rather than picking one. A failed probe is cached, because a CLI
  that does not support `session list` will not start supporting it on the next
  close, but a run that was never wired up to ask is not cached: doing that would
  disable the lookup for every other chat in the process.
  `TestResolveAgentSessionIDDeclinesWhenAmbiguous` and
  `TestResolveAgentSessionIDSkipsCLIsWithoutSessionList` pin those two decisions,
  and `TestExitFollowsTheScreenWithTheSessionID` plus
  `TestExitSurvivesACliWithNoSessionList` pin that the screen text still goes out
  first, with the id only ever following it.
- **`/agent exit` posted no screenshot at all.** `announceAgentExit` was wired
  into `/exit` and into the natural-exit relay but not into `handleAgentStop`, so
  `/agent stop` and `/agent exit` closed the session and sent only a text line.
  The last image left in the chat was the live relay's scaled-down photo — so the
  final-frame bugs above were invisible on that path, and the owner's "the
  screenshot came out broken" was really "there was no screenshot". The path now
  posts the final screen before its confirmation, and
  `TestAgentExitWinsTheRace` asserts the screen text is actually sent — the same
  reason the bug survived: the test only ever checked the text line.
- **`/agent close` did something other than what it said.** It was an alias for
  hiding the scrollback hint line — not for closing anything. Someone typing it
  would reasonably expect the session to end, and instead one line of text would
  disappear while the CLI kept running with the project directory still held. The
  alias is gone; the session is closed by `/agent stop` or `/agent exit`, and
  `TestAgentCloseIsNotACommand` pins that `/agent close` is rejected *and* leaves
  the process alive.

Note: while the agent session is open, the agent process holds the project
directory — don't start the same CLI by hand in that same folder. `/agent stop`
(or `/agent exit`) releases it and leaves everything else alone. `/exit` releases
it too, and additionally clears the project binding and working directory, so
reach for it when you want the whole context gone rather than just the process.

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

With a project selected, `TERMILINK_PROJECT=<name>` is already exported into
the shell. Add more per-project variables with `env:` — they are exported when
the shell session is (re)created (first command, `/project` switch, or
`/exit`):

```yaml
projects:
  app:
    path: ~/code/app
    env:
      APP_PORT: "8080"
      NODE_ENV: production
```

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
  audit_log: ""       # empty = ~/.termilink/audit.log, "off" disables, or a path
  audit_max_bytes: 0  # rotate to audit.log.1 once exceeded (0 = unlimited)
```

Every security-sensitive event is appended to the audit file as one JSON line
per event: commands (with result, duration and exit state), the danger-approval
flow (requested / approved / rejected / blocked / timeout), whitelist
rejections, workspace vet blocks, project switches, `/input`, `/stop` and
`/exit`. Obvious secret values in commands (`password=…`, `token=…`,
`Authorization: Bearer …`, …) are written as `***redacted***`; the executed
command is never modified.

The action names written today:

| action | raised when |
| --- | --- |
| `command`, `command_result` | a command ran, and how it ended |
| `approval_requested`, `approval_approved`, `approval_rejected`, `approval_blocked`, `approval_timeout` | the dangerous-command flow |
| `access_denied` | a non-owner, or a non-whitelisted user, tried to do something |
| `project_switch`, `vet_blocked`, `file_get`, `file_upload`, `file_link` | project and file activity |
| `agent_start`, `agent_input`, `agent_stop`, `agent_history`, `agent_session`, `agent_error` | agent sessions — including which slice of scrollback was rendered, the session id that was reported, and `cmd="exit via /exit"` when `/exit` closed the agent |
| `panic` | a fault was contained; the message and stack also go to the log |

`panic` is the one to grep for first when the bot goes quiet: it means a command
died but the gateway survived.

Read recent entries with:

```bash
termilink audit          # last 20 entries, human-readable
termilink audit -n 100   # last 100 entries
termilink audit --json   # raw JSON lines, e.g. for machine processing
```

Auditing never takes the agent down — write errors are reported once to stderr
and ignored. `security.audit_log: off` disables it entirely. To keep the log
from growing forever, set `audit_max_bytes` (default `0` = unlimited); once the
file exceeds it, the current log is rotated to `audit.log.1` before the next
entry.

## Run as a service

### macOS (launchd)

A template is provided at `scripts/com.termilink.agent.plist`. Fill in the
binary path and the directory containing `config.yaml`, then install:

```bash
sed "s|/PATH/TO/termilink|/Users/you/bin/termilink|; s|/PATH/TO/DIR/WITH/config.yaml|/Users/you/termilink|" \
    scripts/com.termilink.agent.plist > ~/Library/LaunchAgents/com.termilink.agent.plist
launchctl load ~/Library/LaunchAgents/com.termilink.agent.plist
```

Replace the placeholders (`/PATH/TO/termilink` binary, `/PATH/TO/DIR/WITH/config.yaml`
directory — also fix the `StandardOutPath`/`StandardErrorPath` if you like).
Control it with:

```bash
launchctl unload ~/Library/LaunchAgents/com.termilink.agent.plist   # stop
launchctl print gui/$(id -u)/com.termilink.agent                    # status
```

`RunAtLoad` starts the agent on login; `KeepAlive.SuccessfulExit = false`
restarts it if it crashes. While it runs under launchd, **don't** also run
`termilink start` manually — the single-instance PID guard will refuse the second
one.

### Other platforms

A systemd unit (Linux) or Windows Service wrapper can be added the same way;
only the macOS template ships today.

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