# Install and run

[← Back to README](../README.md)

TermiLink runs on one machine as your user account and is driven from Telegram.
There are three ways to run it, and they are not interchangeable: a development
run from the source tree, an installed binary, and a service. Most confusion
comes from mixing them.

## Where everything lives

One directory holds the runtime state, so an installed agent never depends on
where the source tree or the download happened to be:

| path | what |
| --- | --- |
| `~/.termilink/config.yaml` | configuration |
| `~/.termilink/.env` | secrets, auto-loaded at startup |
| `~/.termilink/state.json` | persisted terminal sessions |
| `~/.termilink/audit.log` | append-only audit trail |
| `~/.termilink/termilink.pid` | single-instance lock |

`termilink init` creates the directory and writes an example `config.yaml` and
`.env.example` there, with owner-only permissions. `--config <path>` overrides
the config location; the `.env` is then read from that file's directory.

## 1. Development run

From a checkout, for iterating on the code:

```bash
make run          # go run ./cmd/termilink
make build        # writes ./termilink (signed on macOS)
./termilink start
```

This ties the process to the source tree; move or delete it and the run is gone.
It is not how you keep the agent up.

## 2. Installed binary

A binary that runs from anywhere.

From a release: download the `termilink_<version>_<os>_<arch>.tar.gz` from the
Releases page, unpack it, and put `termilink` on your `PATH`.

From source, with Go 1.27.1 or newer:

```bash
go install github.com/saliherden/termilink/cmd/termilink@latest
```

`go install` writes to `$(go env GOBIN)`, which defaults to `$(go env
GOPATH)/bin` (usually `~/go/bin`). That directory is often not on `PATH`; if
`termilink` is not found afterwards, either add it, or use the service install
below, which copies the binary to `~/.local/bin`.

Then:

```bash
termilink init                       # writes ~/.termilink/config.yaml
# edit ~/.termilink/config.yaml (security.allowed_users)
# and ~/.termilink/.env (TELEGRAM_BOT_TOKEN)
termilink start
```

`start` runs in the foreground and stops when the terminal closes. To keep it
running, use the service.

## 3. Service

The supported way to keep it running. The operating system starts it at login
and restarts it after a crash:

```bash
termilink service install
```

macOS gets a launchd agent in `~/Library/LaunchAgents`; Linux a systemd user
unit in `~/.config/systemd/user`. Either way `install` copies the binary to
`~/.local/bin/termilink` and points the job there, so it does not depend on
where you ran the command. On macOS that location is also outside the folders
TCC protects against background jobs. `uninstall` removes the job and leaves the
binary and logs in place.

```bash
termilink service status     # paths and whether it is running
termilink service render     # the definition it would write
termilink service path       # the PATH the job would run with
termilink service uninstall  # stop and remove the job
```

On macOS, grant Full Disk Access to `~/.local/bin/termilink` if the agent must
read a protected folder such as `~/Desktop`, `~/Documents` or `~/Downloads`.
Sign the binary with a stable identity first (`make cert`; after that `make
build` signs automatically) so the grant survives later rebuilds. See
[Run as a service](service.md#protected-folders-on-macos).

## Updating

```bash
git pull && make build && ./termilink service install   # from a checkout
```

or, from a release or `go install`, replace the binary and run `termilink
service install` again. Reinstalling copies the new binary over
`~/.local/bin/termilink` and restarts the job. Configuration and state in
`~/.termilink/` are untouched.

## Releasing (maintainers)

Releases are tag-driven; there is no manual upload step:

```bash
git tag v0.2.0
git push origin v0.2.0
```

The tag starts `.github/workflows/release.yml`, which runs GoReleaser with
`.goreleaser.yaml`: pure-Go cross-compiles for `linux` and `darwin` on `amd64`
and `arm64`, a `tar.gz` per target, and `checksums.txt`, published to the GitHub
Releases page with a changelog. The version is injected into `internal/version`
at build time.

macOS binaries are linker-signed (ad-hoc), so a downloaded binary may need
"Open Anyway" or `xattr -d com.apple.quarantine termilink`; a locally built
binary is unaffected. There is no Homebrew formula yet; it needs a separate tap
repository and is on the [roadmap](roadmap.md).

## See also

- [Configuration](configuration.md)
- [Telegram commands](commands.md)
- [Run as a service](service.md)
- [CLI](cli.md)
- [Troubleshooting](troubleshooting.md)
