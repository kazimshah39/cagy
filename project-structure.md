# Herdr Tandem Project Structure

**Status:** Provider-managed, adapter-based architecture
**Date:** September 21, 2026

## Purpose

Herdr Tandem creates one visible supervisor and one visible `agy` developer in Herdr for each project. The supervisor can be Codex or OpenCode. The developer is currently agy.

Herdr owns panes and agent lifecycle. Herdr Tandem owns project-scoped workflow orchestration, pane ownership checks, task journals, transcript delivery, and the local stdio MCP bridge.

## Adapter model

```text
Supervisor adapter: Codex or OpenCode
             │
             ├── shared Herdr Tandem MCP bridge
             │
Developer adapter: agy
             │
             └── Herdr antigravity-cli transcript/session integration
```

`internal/supervisor` contains launch contracts, registry, Codex, and OpenCode adapters. `internal/developer` contains the developer contract and the agy profile. `internal/app` remains the shared lifecycle and task engine.

## Provider service boundary

An external local service owns provider login, account selection, quota handling, and fallback. Herdr Tandem only checks `GET /api/health` before startup and delegation. It never reads, stores, or changes provider credentials or account state.

The agy developer adapter declares whether this service is required. This keeps the lifecycle engine independent of a specific provider implementation and leaves room for future developer adapters.

## Concurrent projects

Runtime records are stored per supervisor under:

```text
~/Library/Application Support/herdr-tandem/state/runtimes/<runtime-id>.json
```

Records contain the supervisor/developer profile IDs, Herdr scope, project, pane IDs, timestamps, and build revision. A duplicate start from the same supervisor pane is rejected; different panes and projects may run concurrently.

## Commands

```text
herdr-tandem [--supervisor codex|opencode] [DIRECTORY]
herdr-tandem doctor [--supervisor codex|opencode]
herdr-tandem stop
herdr-tandem mcp-server       # internal stdio bridge
herdr-tandem ask --stdin      # emergency fallback
herdr-tandem ask --recover
herdr-tandem ask --forget
```

## Testing

Tests use fake Herdr and process runners plus local fixtures. They do not launch real Codex, OpenCode, or agy sessions, change provider accounts, or consume quota.

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/herdr-tandem
```
