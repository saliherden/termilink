# CLI

[← Back to README](../README.md)

```bash
termilink start        # start the agent (Telegram gateway)
termilink status       # show runtime status
termilink config       # show resolved configuration
termilink projects     # list configured projects
termilink sessions     # list active sessions
termilink audit        # show recent audit log entries
termilink init         # scaffold ~/.termilink/config.yaml and .env.example
termilink service ...  # install as a service (macOS/Linux): render/install/status/uninstall/path
termilink --version    # print the version
```

## Development

```bash
make cert   # macOS only, once: create the code-signing identity
make build  # builds, and on macOS signs the binary
make run
make test   # CI additionally runs the race detector
```

On macOS, `make build` signs with the `TermiLink Local` identity created by
`make cert`. Without it the binary gets an ad-hoc signature, and a Full Disk
Access grant tied to a previous build stops matching after a rebuild.
