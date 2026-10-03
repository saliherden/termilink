# Troubleshooting

[← Back to README](../README.md)

"Unknown command" for something that looks like a command. A leading `/`
only makes it a command if the bot knows the word; with an agent running, an
unknown one is typed into the agent as text. See
[A slash is only a command when the bot knows it](commands.md#a-slash-is-only-a-command-when-the-bot-knows-it).

Every shell command fails on a machine without zsh. The example config sets
`/bin/zsh` explicitly, which overrides auto-detection, and the path is not
checked at load time — install zsh or point `terminal.shell` at your own shell.

A second `termilink start` is refused. The PID lock at
`~/.termilink/termilink.pid` is held by another instance — stop it, or delete the
file if you are sure nothing is running. Note that under launchd the instance you
want to stop is the service: `launchctl bootout gui/$(id -u)/com.termilink.agent`.

`ls` (or anything else) fails with `Operation not permitted` under the service
on macOS. macOS protects `~/Desktop`, `~/Documents` and `~/Downloads` with TCC,
and a launchd job has no access to them unless it holds Full Disk Access, so
listing such a folder fails with EPERM while the same command works in a
terminal. `service install` already puts the binary at `~/.local/bin/termilink`,
outside those folders, so only the project itself is at issue: move it out of a
protected folder (for example to `~/code`), or add `~/.local/bin/termilink` in
System Settings → Privacy & Security → Full Disk Access. The grant is tied to the
binary's code signature, and a plain `go build` produces a new ad-hoc hash each
time, which loses it; sign with a stable identity (`make cert-help`) and the grant
survives rebuilds.

Everything in `~/.termilink/` is private. The directory is `0700` and its
files `0600`, on the reasoning that the audit log records every command you ran
and the state file holds the chat id of every conversation and the path of every
directory the agent has touched. That is enough to describe your work to another
account on a shared machine without revealing any of it. An older build wrote
some of it as `0644`; the permissions are corrected on the next write, or run
`chmod 700 ~/.termilink && chmod 600 ~/.termilink/*`.

`agent` says the project is not set. It runs inside the selected project
directory, so send `/project <name>` first.

`get` sent a search instead of the file you meant. An argument that looks
like a path to an existing file is a file; anything else is a keyword matched
against artifact names. `get ./release/app.apk` is that file, `get release/14` is
a search for `release/14.apk`. See [Downloading](files.md#downloading).

A file over 50 MB was not sent. Without `big_file_link_host` set it is
refused and nothing leaves the machine; with a host set it needs an owner
approval and the link is public. See [Size limits](files.md#size-limits).

The bot went quiet. `termilink audit | grep panic` first: a contained fault
is recorded there and the gateway is still running.
