# Herdr Tandem

Herdr Tandem runs one supervisor and one `agy` developer inside Herdr. The supported supervisors are **Codex**, **OpenCode**, and **agy** (Codex remains the default). The developer remains `agy`.

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
herdr-tandem [--expanded] [--supervisor codex|opencode|agy] [--supervisor-model <model>] [--developer-model <model>] [DIRECTORY]
herdr-tandem doctor [--supervisor codex|opencode|agy]
herdr-tandem stop
```

Or using the `hdt` shorthand:

```bash
hdt [--expanded] [--supervisor codex|opencode|agy] [--supervisor-model <model>] [--developer-model <model>] [DIRECTORY]
hdt doctor [--supervisor codex|opencode|agy]
hdt stop
```

Emergency task commands remain available through the same binary:

```bash
herdr-tandem ask --stdin
herdr-tandem ask --recover
herdr-tandem ask --forget
```

The default supervisor is Codex. OpenCode is selected explicitly with `--supervisor opencode`, and agy is selected with `--supervisor agy`.

## Model selection

Herdr Tandem defines Tandem-owned default model selections:

- **agy supervisor default:** `claude-opus-4-6-thinking` (applied when agy is the selected supervisor and `--supervisor-model` is not explicitly supplied)
- **agy developer default:** `gemini-3.8-flash-high` (applied for all supervisors when `--developer-model` is not explicitly supplied)

### Model overrides

Explicit CLI model flags always override the defaults:

- `--supervisor-model <model>`: overrides the agy supervisor model (supported only when `--supervisor agy` is selected).
- `--developer-model <model>`: overrides the agy developer model (supported with any supervisor).
- Explicit CLI flags always win, including explicitly passing a default model value.
- Codex and OpenCode supervisors do not receive a supervisor model default or flag; their launch arguments and environment remain supervisor-native.

### Version pinning policy

Herdr Tandem intentionally pins explicit model versions in code constants (`claude-opus-4-6-thinking` and `gemini-3.8-flash-high`) rather than relying on floating or automatic "latest" aliases. This policy ensures:

- **Reproducibility:** Pair-programming workflows and tool-calling behaviors behave consistently across runs without unannounced upstream changes or prompt format drift.
- **Validation before upgrade:** Each pinned version is tested and validated against Tandem's MCP bridge and agy's edit modes before adoption. Future releases of Herdr Tandem update these constants after testing.

### Capability preflight & doctor validation

- **Start preflight:** Before creating panes or starting agents, `herdr-tandem` prints a progress message and performs a fail-fast capability check using `agy models` (verifying exact tabular model IDs without initiating a model turn or consuming quota). The query has a dedicated 20-second upper bound. When both supervisor and developer use agy, model list validation is executed only once per invocation.
- **Doctor check:** `herdr-tandem doctor` validates that the effective developer model is available, and if `--supervisor agy` is selected, also validates that the effective supervisor model is available.
- **Runtime persistence & repair:** The effective models are persisted in the runtime record and retained during developer repair and exact session resumption.

## Sidebar modes

Compact mode is the default:

```bash
herdr-tandem  # or: hdt
```

Compact mode shows one real Herdr agent row labeled `hdt`:

- The supervisor is shown while no Tandem-managed developer task is active.
- The developer becomes the visible row before delegated work starts, so Herdr shows its native working or blocked indicator.
- The supervisor becomes visible again after confirmed completion, safe recovery, safe forgetting, or stop.
- Work typed manually in the developer pane does not switch the compact representative automatically because Herdr Tandem does not run a persistent watcher.

Expanded mode is an explicit opt-in:

```bash
herdr-tandem --expanded  # or: hdt --expanded
```

Expanded mode always shows both the supervisor (`hdt Supervisor`, or `agy Supervisor` when agy is selected as the supervisor) and `agy Developer` as separate real native rows. This ensures manual work typed directly in the developer pane remains visible even when the supervisor is idle or stopped. Starting an expanded project does not change other projects.

Starting a compact project does not hide an expanded project's developer, and starting an expanded project does not expand other projects.

Herdr currently supports one transient Agent view per server. Herdr Tandem uses one stable projection that hides only panes carrying its explicit hidden visibility token. This keeps unrelated agents visible, but another tool that replaces Herdr's single transient view cannot be composed with Tandem's view.

## How it works

The supervisor handles questions, read-only investigation, task scoping, and independent review. Unless you ask it not to delegate, it gives bounded repository implementation and relevant automated tests to agy, with the goal, constraints, acceptance criteria, and checks to run. Larger work is handled as sequential tasks, one at a time.

1. Herdr Tandem validates Herdr, the selected supervisor, `agy`, and the provider service.
2. It creates a project-scoped runtime record under `~/Library/Application Support/herdr-tandem/state` with the selected sidebar mode.
3. It creates a visible right-hand developer pane, validates agy startup readiness within a 60-second window (accepting project trust if shown), and applies project-specific ownership, label, and visibility metadata.
4. It starts Codex, OpenCode, or agy in the current pane with a local stdio MCP bridge. When agy is the supervisor, a temporary runtime-owned custom agent (`~/.gemini/config/agents/herdr-tandem-<runtime-id>/agent.md`) is prepared failure-atomically with `0700` directories and `0600` file permissions to supply the stdio MCP bridge and supervisor instructions without persistent global configuration; this artifact is unconditionally removed when the session exits or is stopped.
5. The supervisor submits implementation work once through `delegate_task`. The call returns after bounded submission while the stdio MCP process monitors the developer for up to 30 minutes.
6. The supervisor ends its turn and stays idle while the local MCP monitor watches the developer without supervisor model calls. At a terminal state, the monitor waits for the verified supervisor to be idle and sends one fixed wake prompt. A restarted MCP bridge repeats this check for pending terminal state. The supervisor may check `task_status` if the user asks for an update or a session is interrupted.
7. When `task_status` reports `completed_unacknowledged`, `recover_task` returns the exact transcript answer and delivery receipt without resubmitting.
8. After reviewing the output, the supervisor calls `acknowledge_task(receipt="...")` to clear durable task state.
9. The provider service owns account selection, quota handling, and fallback.

Each supervisor pane has its own runtime. A duplicate start from the same Herdr pane is rejected. When the foreground supervisor exits, running `herdr-tandem stop` in the pane automatically discovers the project runtime to clean up any temporary supervisor artifacts, stop the developer pane, and remove the runtime record.

## Safety

- No tmux, daemon, listener, persistent worker, queue, or web UI is used. The only background work is the bounded monitor inside the existing stdio MCP child process.
- Process arguments are passed as arrays; prompts and paths are never shell-interpolated.
- State files are private (`0700` directories and `0600` files).
- Diagnostics never contain task prompts, answers, receipts, credentials, service secrets, or complete transcripts. The terminal-state supervisor wake is fixed internal text and never includes developer-controlled content. Diagnostics are stored in `~/Library/Application Support/herdr-tandem/state/logs/herdr-tandem.log`, rotated at 8 MiB, and include stable `HTD-*` event codes documented in `AGENTS.md`.
- Sidebar metadata is written only on lifecycle transitions, not on every watchdog poll.
- Automated tests never launch real agents or consume model quota.

## Development checks

```bash
make verify
go build ./cmd/herdr-tandem
go test -race ./...
go vet ./...
```
