# Run as a service

[← Back to README](../README.md)

The definition is generated from your resolved configuration, so there is no
template to fill in. `install` prints the paths it will use and the definition it
is about to write, then asks before touching anything.

## Where the service runs from

`install` copies the binary to `~/.local/bin/termilink` and points the job there.
The copy is deliberate: the job must not depend on where you happened to run the
command from, and on macOS it must not live under `~/Desktop`, `~/Documents` or
`~/Downloads`, which TCC protects against background jobs. A build tree on the
Desktop, for instance, would install a job that launchd starts and the kernel
then denies.

Override the location with `--bin-dir`. The configuration home is `~/.termilink/`
by default — where the state file, audit log and PID lock already live —
`termilink init` creates it, and `--config` still overrides it. `uninstall`
removes the plist or unit and leaves the binary in place, so the next `install`
does not need a build tree to point at.

The copy preserves the code signature because it is byte for byte: `make build`
signs the binary with a stable local identity when one exists (see
`make cert-help`), and the copy carries that signature, so a Full Disk Access
grant survives rebuilds.

## macOS (launchd)

```bash
make service-install     # or: termilink service install
```

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

While it runs under launchd, don't also run `termilink start` by hand — the
single-instance PID guard refuses the second one. The reverse also holds: the
agent runs shell commands as you, so `pkill -f termilink` typed into Telegram
kills the agent's own process, and launchd starts it again.

### The PATH is the part that matters

A launchd job starts with `PATH=/usr/bin:/bin:/usr/sbin:/sbin`. That is enough
to run the binary and nothing else: no Homebrew, no `gh`, no `node`, no `java`.
Because every shell the agent spawns inherits its environment, the job would
work in a way that looks correct until you asked it to do anything real.

So the PATH is captured at install time and written into the plist. Check what
the job would get, without installing anything:

```bash
termilink service path          # the resolved PATH, and what was dropped
termilink service render        # the full definition
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

### Bot token

The definition carries no token. A property list in `~/Library/LaunchAgents` is
world-readable, and the generated file is meant to be pasted into a review, so
putting a credential in it would defeat the reason the job runs as you.

What matters instead is where the token lives. A service job starts with no
environment of its own, so a token that only ever existed in your shell is a job
that comes up and cannot connect. Two of the three places work:

| token lives in | works under a service |
| --- | --- |
| `config.yaml`, as a literal | yes |
| `.env` beside `config.yaml` | yes — the agent loads that file at startup |
| your interactive environment | no |

`service install` reports which one applies, and says so before you commit:

```console
$ termilink service install
note: the bot token is in .env next to config.yaml, which the service can read.
      Keep that file at 0600: it is the one thing that grants shell access.
```

A `.env` file is the usual answer because it keeps `config.yaml` safe to paste
into an issue and the token out of it. `chmod 600` it — it is a shell credential.
And note that `.env` is read from the config file's directory, not from
wherever the command was run, so `termilink --config ../other/config.yaml
service install` inspects the same file the installed agent will.

### Protected folders on macOS

macOS protects `~/Desktop`, `~/Documents` and `~/Downloads` with TCC. A launchd
job has no access to them unless it holds Full Disk Access, so a shell command
that lists or reads a project in one of those folders fails with
`Operation not permitted` while the same command works in a terminal — the
terminal app already holds the grant.

`install` already keeps the binary out of those folders. For the projects the
agent touches you have two options: keep them outside the protected folders (for
example under `~/code`), or grant Full Disk Access to the installed binary at
`~/.local/bin/termilink` in System Settings → Privacy & Security → Full Disk
Access.

The grant is tied to the binary's code signature. A plain `go build` produces an
ad-hoc signature whose hash changes on every rebuild, so the grant is lost with
the next build and has to be given again. Sign with a stable local identity once
— `make build` does it automatically when the identity exists, and
`make cert-help` prints how to create one — and the grant persists across
rebuilds.

## Linux (systemd)

```bash
make service-install     # or: termilink service install
```

The same generated definition, written as a systemd user unit rather than a
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

## Windows

`termilink service` is implemented on macOS (launchd) and Linux (systemd). On
Windows the commands exist and explain what is missing rather than failing
silently:

```console
$ termilink service install
termilink service install is not implemented on windows yet; the planned approach is a service registration (nssm or the SCM directly)
```

The service description, the PATH resolution and the command surface are already
platform-neutral — Linux needed a renderer, not a redesign — so Windows is a
renderer to write as well. It is on the [roadmap](roadmap.md).
