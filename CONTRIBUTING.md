# Contributing to cagy

Thanks for helping improve cagy.

## Before opening a change

- Search existing issues first.
- Keep proposals within cagy's focused scope: one visible Codex supervisor and one visible agy developer inside Herdr.
- Do not add tmux, a daemon, a hidden worker, a queue, or support for unrelated agents.
- Do not include credentials, transcripts, account details, private paths, or terminal dumps containing sensitive data.

## Development setup

Requirements:

- Go 1.22 or newer
- Herdr
- Codex CLI
- agy CLI
- AGM

Clone the repository, then run:

```bash
make verify
# or:
gofmt -w cmd internal
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/cagy
```

Automated tests must use fake process runners and local fixtures. They must not launch real Codex or agy sessions, consume model quota, or switch a real AGM account.

## Pull requests

Keep pull requests small and focused. Include:

- the problem being solved;
- the behavior before and after the change;
- relevant security or compatibility considerations;
- tests for new behavior;
- documentation updates when commands or architecture change.

By contributing, you agree that your contribution is licensed under the repository's MIT License.
