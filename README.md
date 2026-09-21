# Herdr Tandem

Herdr Tandem runs one supervisor and one `agy` developer inside Herdr. The supported supervisors are **Codex** and **OpenCode**. The developer remains `agy`.

Each project has an independent Herdr Tandem runtime, so compact and expanded projects can run together without changing each other.

## Requirements

- Apple Silicon macOS
- Herdr, `codex`, `opencode`, and `agy` on `PATH`
- Herdr's current Antigravity transcript integration
- A local provider service with account fallback configured

Herdr Tandem checks the provider service health endpoint at `http://127.0.0.1:20128/api/health`.
Set `HERDR_TANDEM_PROVIDER_SERVICE_URL` only when the service uses another loopback URL. Herdr Tandem does not configure the service or manage provider credentials, accounts, or quota fallback.

## Commands

`herdr-tandem` is the canonical command. An `hdt` shorthand alias is installed alongside it.

```bash
herdr-tandem [--supervisor codex|opencode] [DIRECTORY]
herdr-tandem --show-agents [--supervisor codex|opencode] [DIRECTORY]
herdr-tandem doctor [--supervisor codex|opencode]
herdr-tandem stop
```

Or using the `hdt` shorthand:

```bash
hdt [--supervisor codex|opencode] [DIRECTORY]
hdt --show-agents [--supervisor codex|opencode] [DIRECTORY]
hdt doctor [--supervisor codex|opencode]
hdt stop
```

Emergency task commands remain available through the same binary:

```bash
herdr-tandem ask --stdin
herdr-tandem ask --recover
herdr-tandem ask --forget
```

The default supervisor is Codex. OpenCode is selected explicitly with `--supervisor opencode`.

## Sidebar modes

The normal command starts a compact project:

```bash
herdr-tandem  # or: hdt
```

Compact mode shows one real Herdr agent row labeled `hdt`:

- The supervisor is shown while no Tandem-managed developer task is active.
- The developer becomes the visible row before delegated work starts, so Herdr shows its native working or blocked indicator.
- The supervisor becomes visible again after confirmed completion, safe recovery, safe forgetting, or stop.

Expanded mode is selected per project:

```bash
herdr-tandem --show-agents  # or: hdt --show-agents
```

Expanded mode always shows `hdt Supervisor` and `agy Developer` as separate rows. Starting a compact project does not hide an expanded project's developer, and starting an expanded project does not expand other projects.

Compact switching covers work submitted through Herdr Tandem. Work typed manually in the developer pane does not switch the compact representative automatically. Use expanded mode when both native states must remain visible.

Herdr currently supports one transient Agent view per server. Herdr Tandem uses one stable projection that hides only panes carrying its explicit hidden visibility token. This keeps unrelated agents visible, but another tool that replaces Herdr's single transient view cannot be composed with Tandem's view.

## How it works

1. Herdr Tandem validates Herdr, the selected supervisor, `agy`, and the provider service.
2. It creates a project-scoped runtime record under `~/Library/Application Support/herdr-tandem/state` with the selected sidebar mode.
3. It creates a visible right-hand developer pane, validates agy startup readiness within a 60-second window (accepting project trust if shown), and applies project-specific ownership, label, and visibility metadata.
4. It starts Codex or OpenCode in the current pane with a local stdio MCP bridge.
5. The supervisor delegates implementation work through `delegate_task`, receiving the completed transcript answer alongside a delivery receipt (`completed_unacknowledged`).
6. The supervisor calls `acknowledge_task(receipt="...")` after reviewing the output. If a session or command is interrupted, `task_status` indicates when recovery is possible and `recover_task` retrieves the answer and receipt without resubmitting the prompt.
7. The provider service owns account selection, quota handling, and fallback.

Each supervisor pane has its own runtime. A duplicate start from the same Herdr pane is rejected.

## Safety

- No tmux, daemon, listener, hidden worker, queue, or web UI is used.
- Process arguments are passed as arrays; prompts and paths are never shell-interpolated.
- State files are private (`0700` directories and `0600` files).
- Diagnostics never contain prompts, answers, credentials, service secrets, or complete transcripts.
- Sidebar metadata is written only on lifecycle transitions, not on every watchdog poll.
- Automated tests never launch real agents or consume model quota.

## Development checks

```bash
make verify
go build ./cmd/herdr-tandem
go test -race ./...
go vet ./...
```
