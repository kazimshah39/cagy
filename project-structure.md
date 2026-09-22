# Herdr Tandem Project Structure

**Status:** Provider-managed, adapter-based architecture
**Date:** September 21, 2026

## Purpose

Herdr Tandem creates one supervisor and one `agy` developer in Herdr for each project. The supervisor can be Codex, OpenCode, or agy (Codex default). The developer is currently agy. Each runtime chooses compact or expanded sidebar presentation independently.

Herdr owns panes and agent lifecycle. Herdr Tandem owns project-scoped workflow orchestration, pane ownership checks, task journals, transcript delivery, and the local stdio MCP bridge.

## Adapter model

```text
Supervisor adapter: Codex, OpenCode, or agy
             │
             ├── shared Herdr Tandem MCP bridge
             │
Developer adapter: agy
             │
             └── Herdr antigravity-cli transcript/session integration
```

`internal/supervisor` contains launch contracts, registry, Codex, OpenCode, and agy adapters. When agy is selected as the supervisor, it uses a runtime-scoped custom agent (`~/.gemini/config/agents/herdr-tandem-<runtime-id>/agent.md`) with private `0700` directory and `0600` file permissions to provide the stdio MCP bridge and supervisor instructions without persistent global configuration; this artifact is unconditionally cleaned up when the supervisor exits or is stopped. `internal/developer` contains the developer contract and the agy profile. `internal/app` remains the shared lifecycle and task engine.

## Developer readiness

Developer startup validates readiness with a bounded 60-second deadline (`? for shortcuts`), interactively accepts the project trust screen if presented, and safely handles transient shell readiness races (`agent_pane_busy`) on newly split panes.

## Provider service boundary

An external local service owns provider login, account selection, quota handling, and fallback. Herdr Tandem only checks `GET /api/health` before startup and delegation. It never reads, stores, or changes provider credentials or account state.

The agy developer adapter declares whether this service is required. This keeps the lifecycle engine independent of a specific provider implementation and leaves room for future developer adapters.

## Concurrent projects

Runtime records are stored per supervisor under:

```text
~/Library/Application Support/herdr-tandem/state/runtimes/<runtime-id>.json
```

Version 4 records contain the supervisor/developer profile IDs, Herdr scope, project, pane IDs, required `sidebar_mode`, optional `supervisor_model`, `developer_model`, and `supervisor_agent_name`, timestamps, and build revision. A duplicate start from the same supervisor pane is rejected; different panes and projects may run concurrently.

## Model selection architecture

Herdr Tandem defines Tandem-owned default model selection:

- agy supervisor default: `claude-opus-4-6-thinking` (applied only when agy is the selected supervisor and `--supervisor-model` was not explicitly supplied)
- agy developer default: `gemini-3.8-flash-high` (applied for all supervisors when `--developer-model` was not explicitly supplied)

### Pinned versions policy

Model versions are intentionally pinned constants in code rather than unpinned or automatic "latest" aliases. This preserves workflow reproducibility and avoids prompt format drift. Constants are updated in future releases after validation.

### Capability preflight & doctor validation

- Fail-fast preflight uses `agy models` (not a model turn) to verify exact tabular model IDs before pane creation or agent execution.
- Single invocation: when both supervisor and developer use agy, model list validation is executed only once per invocation via internal caching.
- Diagnostics privacy: model values, full command output, credentials, instructions, and task text are never logged to diagnostics.
- Runtime records persist effective models; developer repair and exact resume retain the stored model.
- Doctor validates the effective agy models without mutation or model turn.

## Sidebar architecture

Herdr exposes one transient Agent view per server, so Herdr Tandem installs one stable projection while any Tandem runtime exists. The projection hides only rows whose `herdr_tandem_sidebar_visibility` token is exactly `hidden`; rows without that token remain visible. Final cleanup is source-guarded and occurs under the runtime-state lock only after the last runtime record is removed.

Each runtime stores `sidebar_mode` as `compact` or `expanded`:

- Compact mode is the default and shows one real agent row labeled `hdt`. It shows the supervisor at rest and the developer during a Tandem-managed submitting, working, blocked, or unresolved task.
- Expanded mode is an explicit opt-in (`--expanded`) that keeps both owned panes visible with distinct labels (`hdt Supervisor` or `agy Supervisor` when agy is the supervisor, and `agy Developer`), ensuring manual work in the developer pane remains visible even when the supervisor is idle or stopped.
- Compact transitions show the destination before hiding the previous row. A partial failure can temporarily show both rows but must not intentionally hide both.
- Recovery and repair use the persisted mode after validating workspace, tab, project, owner, role, runtime, and environment agreement.
- `task_status` and `developer_status` are read-only. Watchdog polling never writes sidebar metadata.

Herdr Tandem does not synthesize a combined agent or override native Herdr status. Manual work started directly in the developer pane is outside compact automatic switching because this application does not run a persistent watcher.

## Commands

`herdr-tandem` is the canonical command; `hdt` is available alongside it as a shorthand alias.

```text
herdr-tandem [--expanded] [--supervisor codex|opencode|agy] [--supervisor-model <model>] [--developer-model <model>] [DIRECTORY]
herdr-tandem doctor [--supervisor codex|opencode|agy]
herdr-tandem stop
herdr-tandem mcp-server       # internal stdio bridge
herdr-tandem ask --stdin      # emergency fallback
herdr-tandem ask --recover
herdr-tandem ask --forget

# Shorthand alias:
hdt [--expanded] [--supervisor codex|opencode|agy] [--supervisor-model <model>] [--developer-model <model>] [DIRECTORY]
hdt doctor [--supervisor codex|opencode|agy]
hdt stop
```

## Reliability diagnostics

Diagnostics are enabled by default at:

```text
~/Library/Application Support/herdr-tandem/state/logs/herdr-tandem.log
```

Logs record starts, finishes, state transitions, retries, pane/agent lifecycle operations, MCP tool events, and errors while redacting plaintext prompts, answers, secrets, and credentials. Logs are bounded at 8 MiB with up to 5 rotated backups, directory mode `0700`, and file mode `0600`. Set `HERDR_TANDEM_DIAGNOSTICS=0` for an explicit single-run opt-out.

## Testing

Tests use fake Herdr and process runners plus local fixtures. They do not launch real Codex, OpenCode, or agy sessions, change provider accounts, or consume quota.

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/herdr-tandem
```
