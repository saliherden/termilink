# Roadmap

[← Back to README](../README.md)

Ordered by what is actually queued, not by wish list. Nothing here is promised
on a date.

## Later

- Scheduled tasks — the one thing the agent CLIs cannot do: a job that runs
  when no agent session is open. Everything else here is either already shipped
  or a few lines of template.
- Windows service renderer — `termilink service` implements macOS (launchd)
  and Linux (systemd). The service description, the PATH resolution and the
  command surface are already platform-neutral, so Windows is a renderer to
  write: an SCM or `nssm` registration. `state_file` and `audit_keep` are in
  place for the case that matters most there, an account with no home directory.
- Homebrew formula — distribution from a separate `homebrew-tap` repository via
  GoReleaser's `brews:` support, published with a tap-scoped token. Packaging
  only; the binary is already signed on macOS. Until then, GitHub Releases and
  `go install` are the install paths.
- Coverage reporting in CI — the test suite is wired, but coverage is not
  published yet.

## Not planned

Each of these was queued once. The reason it came off is kept here so it does not
get re-proposed:

- Multiple concurrent agent sessions. The agent CLIs already persist their own
  sessions per working directory, so `/project` and `cd` are the switch you
  actually want. A second TermiLink-side session would duplicate what the agent
  already does, and two screen relays would interleave in one chat. Rate limits
  are not the reason — the live relay edits a single message rather than posting
  one per frame, so it costs one message per session no matter how long it runs.
- Git worktree support. It exists to keep concurrent sessions off each other's
  files. With one session per chat there is nothing to isolate.
- Automated build/test loops, `git status` inspection, PR workflows. The agent
  has a shell: it runs `npm test`, reads `git status` and calls `gh pr create` on
  its own. A Telegram command for each would be a worse version of a tool the
  agent already has. `get` already delivers build artifacts, so that half shipped.
- Gateway mode and device management. TermiLink stays on one machine, which is
  also the security argument: a leaked bot token reaches the one machine TermiLink
  runs on, but behind a gateway the same token reaches every machine you have
  connected. A bad trade for one chat instead of two.
- Web dashboard. One operator, one machine, and Telegram is the interface that
  already works. A dashboard is a second interface to build, secure and test, in
  order to show you data you can already read.
- Any hosted or multi-tenant mode. This is a single-operator tool: it runs as
  your user account, on your machine, with your bot token. If you need an OS
  sandbox around it, run it inside a VM or container — the process boundary is
  the real one, and the workspace policy in
  [Known limitations](security.md#known-limitations) is not a substitute for it.
