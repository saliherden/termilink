# Contributing

[← Back to README](../README.md)

`master` is protected: outside contributors go through a pull request, and
the repository owner is deliberately exempt so a stale branch is never a reason to
skip the checks. CI runs `ubuntu-24.04` and `macos-latest` with `fail-fast:
false`, and each job runs `gofmt` → `go build` → `go vet` → `go test -race
-timeout 15m`, ordered cheapest first; `staticcheck` is pinned at `v0.8.1` and
runs once, on the Linux job. Green in both is the bar for a pull request, though
these are not currently enforced as required status checks and the ruleset has
never actually gated a merge.

A cross-compile step also builds and vets `GOOS=windows` so a Windows-only
compile error surfaces on the Linux runner rather than in a user's build.

On macOS, run `make cert` once before `make build`. It creates a local
code-signing identity (`TermiLink Local`) in a dedicated keychain, so builds are
signed with a stable identity. Without it every rebuild falls back to an ad-hoc
signature, and a Full Disk Access grant on a protected folder stops matching the
new binary. `make cert-help` prints the manual steps.

The Linux label is pinned to `ubuntu-24.04` rather than `ubuntu-latest`: these
tests drive a real `zsh` over a PTY and read the terminal's line settings, so they
are sensitive to what the runner image ships — enough that they broke on an image
the local machine could not reproduce. `ubuntu-latest` moves from 24.04 to 26.04
between 19 Oct and 19 Nov 2026, so bumping it should be a deliberate edit and a
visible failure, not a surprise on a Tuesday. Windows is absent on purpose —
`creack/pty` compiles there but `StartWithSize` returns `ErrUnsupported` at
runtime, so every PTY test would fail on a runner that could never be made to
pass.
