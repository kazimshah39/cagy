# cagy Project Structure

**Status:** Router-mode architecture
**Date:** September 20, 2026

## Purpose

`cagy` creates one visible Codex supervisor and one visible `agy` developer in Herdr for each project.

```text
Project A: Codex + agy ─┐
Project B: Codex + agy ─┼─ 9Router Antigravity MITM ─ provider accounts
Project C: Codex + agy ─┘
```

Herdr owns the panes, terminal UI, and agent lifecycle. cagy owns only the project-scoped workflow, safe pane ownership checks, task journals, transcript delivery, and the local stdio MCP bridge.

## Router ownership

9Router owns all provider-account concerns:

- provider login and OAuth refresh;
- account selection and account-level fallback;
- quota and rate-limit handling;
- the Antigravity MITM certificate, local HTTPS listener, and DNS/hosts routing.

cagy never reads, writes, imports, rotates, probes, or stores provider credentials. It does not call `agy -p /model` or `agy -p /quota`. It does not start, stop, configure, or repair 9Router.

Before starting a supervisor, before delegating a task, and in `cagy doctor`, cagy checks 9Router's public local health endpoint, `GET /api/health`. This confirms only that the external router is reachable. 9Router itself owns MITM readiness, certificate trust, DNS interception, and provider fallback.

Set `CAGY_ROUTER_URL` only when 9Router is intentionally served at another loopback URL. The value is passed to the per-supervisor MCP bridge. Cagy never reads 9Router CLI tokens or private status endpoints.

## Concurrent projects

cagy stores one runtime record per supervisor under:

```text
~/Library/Application Support/cagy/state/runtimes/<runtime-id>.json
```

Runtime ownership is scoped to the Herdr workspace, supervisor pane, developer name, and project. Different supervisor panes can run concurrently. A second cagy start from the same supervisor pane is rejected.

A stale pre-router `runtime.json` is removed only after cagy proves it is not live. A live pre-router session must be stopped once before router-mode sessions begin.

## Commands

```text
cagy [DIRECTORY]
cagy --show-agents [DIRECTORY]
cagy doctor
cagy stop
cagy mcp-server                 # internal stdio bridge
cagy ask --stdin                # compatibility/emergency fallback
cagy ask --recover
cagy ask --forget
```

There is no `cagy accounts` command.

## Start flow

1. Validate Apple Silicon macOS, Herdr context, required CLIs, Herdr's current `antigravity-cli` integration, and 9Router health.
2. Create a project-scoped runtime record.
3. Create and verify the right-hand agy developer pane.
4. Start agy with required non-interactive flags.
5. Start Codex with the per-invocation MCP bridge and trusted-project override.

## Delegation flow

1. Acquire a per-developer lock and verify there is no unresolved journal.
2. Verify 9Router MITM is ready.
3. Submit the original task exactly once through Herdr.
4. Monitor visible agy state, transcript changes, blocked state, progress stalls, five-minute heartbeats, and the 30-minute deadline.
5. Read only the exact task's final response from the agy JSONL transcript.
6. Persist an acknowledgement receipt before delivery. The supervisor acknowledges that receipt after review.

Cagy never retries a task merely because output is incomplete. A healthy-stall recovery may cancel the verified task and send one generic continuation prompt in the same conversation; it never repeats the original task text.

## Security and diagnostics

- cagy does not log task text, final answer text, provider credentials, OAuth data, 9Router API keys, complete environments, or transcripts.
- Task journals contain task hashes and transcript offsets only.
- Diagnostics are enabled during the September 2026 validation period at `~/Library/Application Support/cagy/state/logs/cagy.log`; `CAGY_DIAGNOSTICS=0` disables them for one run.
- cagy does not configure certificates, DNS, `/etc/hosts`, sudo access, or Keychain items. Those are 9Router setup concerns.

## Testing

Automated tests use fake Herdr/process runners and local HTTP test servers. They never launch real Codex or agy processes, mutate a provider account, consume quota, or configure MITM networking.

Required checks:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/cagy
```
