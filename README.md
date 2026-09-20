# cagy

`cagy` runs one visible Codex supervisor and one visible `agy` developer inside Herdr.

It is designed for several projects running at the same time. Provider accounts and quota fallback are handled by **9Router**, not by cagy.

```text
Project A cagy ── agy A ─┐
Project B cagy ── agy B ─┼─ 9Router MITM ─ provider account pool
Project C cagy ── agy C ─┘
```

## Requirements

- Apple Silicon macOS
- Herdr, Codex, and agy on `PATH`
- Herdr's current Antigravity transcript integration
- 9Router running locally with Antigravity MITM enabled, its certificate trusted, and Antigravity DNS interception enabled

By default cagy checks 9Router's public health endpoint at `http://127.0.0.1:20128/api/health`. Use `CAGY_ROUTER_URL` only if you intentionally run 9Router at another loopback URL. This check confirms that 9Router is reachable; configure MITM, certificates, DNS, and account fallback in 9Router itself.

## Commands

```bash
cagy [DIRECTORY]
cagy --show-agents [DIRECTORY]
cagy doctor
cagy stop
```

Compatibility commands:

```bash
cagy ask --stdin
cagy ask --recover
cagy ask --forget
```

`cagy accounts` was intentionally removed. cagy does not store credentials, edit Keychain entries, run OAuth, check `/quota`, or rotate accounts.

## How it works

1. `cagy` checks Herdr, agy integration, and 9Router health.
2. It starts a visible right-hand agy developer pane.
3. It starts Codex in the current pane with a local stdio MCP bridge.
4. Codex delegates work through `delegate_task`.
5. cagy tracks the exact transcript response and durable task state.
6. 9Router selects a healthy provider account and retries eligible provider failures.

Each supervisor has its own runtime record. Different Herdr panes and projects can run simultaneously. A duplicate start from the same supervisor pane is rejected.

## Important limits

cagy verifies only that 9Router is reachable, but it does not install, configure, inspect, or repair MITM. Set up 9Router first. Cagy also cannot make a provider quota reset sooner; it relies on 9Router's configured account and model fallback policy.

## Safety

- cagy does not change 9Router settings, certificates, DNS, `/etc/hosts`, Keychain items, or provider accounts.
- cagy never writes task text, answers, credentials, or complete transcripts to its diagnostics.
- The final answer comes only from the exact matching agy transcript event.

## Development checks

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/cagy
```
