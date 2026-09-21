# Herdr Tandem

Herdr Tandem runs one visible supervisor and one visible `agy` developer inside Herdr.
The supported supervisors are **Codex** and **OpenCode**. The developer remains `agy`.

```text
Project A: Herdr Tandem (Codex/OpenCode) + agy
Project B: Herdr Tandem (Codex/OpenCode) + agy
Project C: Herdr Tandem (Codex/OpenCode) + agy
```

Each project has an independent Herdr Tandem runtime. Provider access and account fallback stay outside this application.

## Requirements

- Apple Silicon macOS
- Herdr, `codex`, `opencode`, and `agy` on `PATH`
- Herdr's current Antigravity transcript integration
- A local provider service with account fallback configured

Herdr Tandem checks the provider service health endpoint at `http://127.0.0.1:20128/api/health`.
Set `HERDR_TANDEM_PROVIDER_SERVICE_URL` only when the service uses another loopback URL. Herdr Tandem does not configure the service or manage provider credentials, accounts, or quota fallback.

## Commands

```bash
herdr-tandem [--supervisor codex|opencode] [DIRECTORY]
herdr-tandem --show-agents [--supervisor codex|opencode] [DIRECTORY]
herdr-tandem doctor [--supervisor codex|opencode]
herdr-tandem stop
```

Emergency task commands remain available through the same binary:

```bash
herdr-tandem ask --stdin
herdr-tandem ask --recover
herdr-tandem ask --forget
```

The default supervisor is Codex. OpenCode is selected explicitly with `--supervisor opencode`.

## How it works

1. Herdr Tandem validates Herdr, the selected supervisor, `agy`, and the provider service.
2. It creates a project-scoped runtime record under `~/Library/Application Support/herdr-tandem/state`.
3. It starts a visible right-hand `agy` pane.
4. It starts Codex or OpenCode in the current pane with a local stdio MCP bridge.
5. The supervisor delegates through `delegate_task` and reviews the exact transcript result.
6. Herdr Tandem journals task hashes and transcript offsets and requires acknowledgement receipts.
7. The provider service owns account selection, quota handling, and fallback.

Each supervisor pane has its own runtime, so multiple projects can run at the same time.
A duplicate start from the same Herdr pane is rejected.

## Safety

- No tmux, daemon, listener, hidden worker, queue, or web UI is used.
- Process arguments are passed as arrays; prompts and paths are never shell-interpolated.
- State files are private (`0700` directories and `0600` files).
- Diagnostics never contain prompts, answers, credentials, service secrets, or complete transcripts.
- Automated tests never launch real agents or consume model quota.

## Development checks

```bash
make verify
go build ./cmd/herdr-tandem
go test -race ./...
go vet ./...
```
