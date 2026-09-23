# TermiLink

> Remote terminal access and development automation for your own computer.

TermiLink is a self-hosted remote terminal and development agent that lets you
control your own computer through Telegram. Run terminal commands, manage
persistent shell sessions, switch between projects, monitor processes, transfer
files and receive build artifacts — all remotely.

## Status

🚧 Early development — Phase 1 (Core Terminal) is under active development.

## Requirements

- Go 1.24+
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

## CLI

```bash
termilink start        # start the agent (Telegram gateway)
termilink status       # show runtime status
termilink config       # show resolved configuration
termilink projects     # list configured projects
termilink sessions     # list active sessions
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