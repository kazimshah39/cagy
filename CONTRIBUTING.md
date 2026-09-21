# Contributing to herdr-tandem

Thanks for helping improve herdr-tandem.

## Before opening a change

- Search existing issues first.
- Keep proposals within herdr-tandem's focused scope: one visible Codex or OpenCode supervisor and one visible agy developer inside Herdr.
- Do not add tmux, a daemon, a hidden worker, a queue, or support for unrelated agents.
- Do not include credentials, transcripts, account details, private paths, or terminal dumps containing sensitive data.

## Development setup

Requirements:

- Go 1.25 or newer on Apple Silicon macOS
- Herdr
- Codex CLI or OpenCode CLI
- agy CLI

Clone the repository, then run:

```bash
make verify
# or:
gofmt -w cmd internal
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/herdr-tandem
```

Automated tests must use fake process runners and local fixtures. They must not launch real Codex, OpenCode, or agy sessions, consume model quota, or change a real provider account. The supported target is macOS Apple Silicon (`darwin/arm64`); do not add Linux, Windows, or Intel-only release targets.

## Pull requests

Keep pull requests small and focused. Include:

- the problem being solved;
- the behavior before and after the change;
- relevant security or migration considerations;
- tests for new behavior;
- documentation updates when commands or architecture change.

By contributing, you agree that your contribution is licensed under the repository's MIT License.
