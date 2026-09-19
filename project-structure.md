# cagy Project Structure

**Status:** Implemented and tested
**Date:** 2026-09-18

## Purpose

`cagy` creates one simple, visible pair inside the user's existing Herdr session:

```text
┌──────────────────────────────┬──────────────────────────────┐
│ Codex Supervisor             │ agy Developer                │
│                              │                              │
│ User talks here.             │ Delegated work is visible.   │
└──────────────────────────────┴──────────────────────────────┘
```

Herdr owns the terminal UI, panes, agent detection, and sidebar status. cagy adds no server or daemon.

By default, the Agent sidebar shows only the Codex supervisor as `cagy`:

```text
Project Space
└── cagy   # Codex lifecycle state and progress
```

The agy developer remains a real Herdr agent and stays visible in the right terminal pane, but cagy marks it with the `cagy_role=developer` metadata token and installs a transient Herdr Agent-view filter that hides only cagy developer rows. Other agents remain visible.

Users can opt into the full two-row view:

```text
Project Space
├── cagy Supervisor   # detected internally as codex
└── cagy Developer    # detected internally as agy; unique cagy_dev_* target retained
```

`cagy --show-agents` clears the compact view only when cagy still owns it. Herdr supports one transient Agent view per server, so compact mode intentionally becomes the active projection; a later view installed by another tool is not cleared by cagy.

## Why Go

Go fits this project better than Python or Rust:

- fast startup and a single binary;
- simple subprocess and JSON support;
- easy tests with injected runners;
- easy cross-compilation;
- less complexity than Rust for a small CLI orchestrator.

Runtime speed is mostly controlled by Codex, agy, and Herdr, so Rust would add build complexity without a useful user-visible speed gain. The supported runtime and release target is Apple Silicon macOS (`darwin/arm64`).

## Commands

```bash
cagy [DIRECTORY]                     # Start with one Codex-based cagy sidebar row
cagy --show-agents [DIRECTORY]       # Show both supervisor and developer rows
cagy doctor                          # Check Herdr, required CLIs, and native account health
cagy stop                            # Safely interrupt, stop, and close the verified developer pane
cagy mcp-server                      # Internal: local stdio MCP server for Codex (not for direct use)

# Compatibility/emergency CLI commands (Codex uses native MCP tools in normal operation)
cagy ask --stdin                     # Read task from stdin and delegate to agy
cagy ask --recover                   # Recover one exact completed answer after caller loss
cagy ask --forget                    # Explicitly discard stale state after pane inspection
```

The user runs `cagy` from an interactive shell pane inside Herdr.

## Fixed Agent Commands

Codex supervisor:

```bash
codex --yolo --dangerously-bypass-hook-trust --search -c 'projects={<directory>={trust_level="trusted"}}' -c 'developer_instructions=<supervisor-instructions>' -C <directory>
```

agy developer:

```bash
agy --dangerously-skip-permissions --mode accept-edits
```

agy after quota recovery:

```bash
agy --conversation <saved-session-id> --dangerously-skip-permissions --mode accept-edits  # exact resume; fresh starts omit --conversation
```

These YOLO flags are required and not configurable. The trusted-project override applies only to this Codex invocation and prevents startup from pausing at the directory trust question.

## Dependencies

Executables on `PATH`:

- `herdr`
- `codex`
- `agy`
- Herdr's current `antigravity-cli` integration, installed with `herdr integration install antigravity-cli`
- macOS Security.framework Keychain (used only for agy's canonical session)

Build dependency:

- Go 1.25 or newer (required for source builds; the official MCP Go SDK v1.8.0 requires Go 1.25)

cagy does not depend on any external account manager. It stores imported or explicitly added account snapshots in private atomic 0600 files and non-secret metadata in private atomic files. `cagy accounts add` uses an explicit loopback Google OAuth flow with PKCE and writes only to that local vault; it does not change agy's active session. Normal startup, repair, doctor, and quota monitoring leave agy's canonical Keychain item under agy's control. Confirmed quota recovery automatically stops the verified developer, refreshes and validates the selected local credential, replaces the canonical item through the AGM-compatible `security` delete-and-add allow-all flow, restarts agy in the same pane, and live-probes quota. This avoids legacy ACL password dialogs and never opens OAuth. Account commands and metadata-only exports show full labels and email addresses for this personal-machine workflow; credential material and OAuth tokens remain excluded. Explicit manual switching uses the same activation transaction but is an emergency/admin command rather than the normal recovery path.

## Start Flow

1. Confirm `HERDR_ENV=1` and read the current workspace/pane IDs.
2. Resolve the requested project directory.
3. Check required executables, supported flags, and Herdr's current `antigravity-cli` integration.
4. Report the guarded display-only label `cagy` for the current pane, or `cagy Supervisor` when `--show-agents` is set. The guard applies it only while Herdr detects Codex there.
5. In default mode, install cagy's Agent-view projection so panes carrying `cagy_role=developer` are hidden. In expanded mode, clear that projection only when cagy still owns it.
6. Derive a unique developer target from the current Herdr workspace and pane.
7. Rename the current pane `Codex Supervisor`.
8. Split the current pane to the right with the project directory and `--no-focus`.
9. Rename the new pane `agy Developer`.
10. Start agy with `herdr agent start` and strict YOLO arguments.
11. Report the guarded display-only label `cagy Developer` and the `cagy_role=developer` token for the developer pane. The internal `cagy_dev_*` target and detected `agy` identity do not change.
12. Read the visible agy screen. If the project trust screen is present with **Yes** selected, press Enter. Then wait for agy's `? for shortcuts` prompt.
13. Export cagy identity variables for child commands.
14. Start Codex using strict YOLO arguments, an invocation-only project trust override, and supervisor text as developer instructions. Do not create an automatic first task.

If the same verified developer already exists, cagy reuses it instead of making a duplicate and reapplies its label and role token so stale or missing sidebar metadata is repaired.

## Delegation Flow

`cagy ask`:

1. Acquires one per-developer private lock. Dead-owner locks are reclaimed; live locks remain exclusive. There is no queue.
2. Refuses to submit new work when an unresolved task journal exists, preventing duplicate side effects after caller interruption.
3. Resolves the named developer and validates its workspace and project.
4. Checks agy's machine-readable `/model` and `/quota` status before submitting work. If quota is confirmed low, cagy starts bounded automatic account recovery before submitting the original task. A failed or unknown probe never causes a switch.
5. Resolves Herdr's agy conversation ID and records separate byte offsets for `transcript.jsonl` and `transcript_full.jsonl` when the session already exists.
6. Reads a baseline of recent developer output for visible status and quota-error checks.
7. Sends the task once with `herdr agent prompt --wait` using a 30-second initial wait.
8. Treats Herdr `idle` and `done` results as hints only because they can appear briefly while agy is still processing. Subsequent Herdr waits match only `blocked`.
9. Reads the current visible screen only for lifecycle markers. `esc to cancel` or a non-zero `task(s)` footer count means working. `? for shortcuts` without either working signal means idle. Marker text in old response content is ignored.
10. Requires the real idle footer to remain stable for the transcript flush grace period. A non-empty planner message seen while agy is working can be only a progress update, so it is not returned early. Transcript `GENERIC/RUNNING` background-task events also keep the task active even if the footer temporarily looks idle; cagy waits for that task's completion signal and a later non-empty planner response.
11. After the initial wait, polls visible state every second, probes agy's current session every 45 seconds, and sends a user-facing heartbeat every five minutes. A 30-second initial timeout or `agent_prompt_stalled` result does not cause task resubmission.
12. Stops waiting after the fixed 30-minute task budget.
13. After stable completion, resolves the conversation reported by Herdr and reads only new JSONL events after the checkpoint. It matches the exact task's `USER_INPUT`, then returns the last non-empty `MODEL` + `PLANNER_RESPONSE` + `DONE` content.
14. Uses `transcript_full.jsonl` whenever it exists because the compact `transcript.jsonl` can truncate long content. The compact file is only a fallback when the full file does not exist.
15. Never prints model reasoning, tool events, system messages, old turns, the full transcript, or terminal scrollback from a blocked task. The terminal remains the visible progress/status view but is not the completed-answer transport.
16. If the session or complete final transcript event is missing, returns a clear error instead of possibly truncated terminal text.
17. Removes the echoed task before checking strong quota-error patterns, so task examples cannot trigger false recovery.
18. If strong output evidence or a successful low-quota probe appears, starts automatic visible recovery: stop the verified developer, rotate through eligible stored accounts, restart in the same pane, live-probe quota, and continue the task.
19. Releases the lock.

Herdr still owns panes, identity, blocked detection, and the visible terminal. cagy combines its verified agent/session data with agy's footer, structured transcript events, and background-task lifecycle because no one signal is reliable enough by itself for turn completion.

### Interrupted caller recovery

A foreground `cagy ask` process cannot prevent its external terminal host from terminating it. Before submission, cagy therefore atomically writes a private task journal in the OS user state directory. The record contains only validated cagy/Herdr identity, timestamps, transcript byte offsets, phase, and the SHA-256 hash of the exact task. It never persists task plaintext or model output. State files are bounded, regular-file-only, private (`0600` inside a `0700` directory), and atomically replaced.

Normal completion is ordered deliberately:

1. Verify the stable idle footer and exact final transcript event.
2. Mark the journal `completed_unacknowledged`.
3. Write the final answer to stdout and check the write result.
4. Remove the journal only after stdout succeeds.

After caller loss, `cagy doctor` reconciles the journal without mutating panes. `cagy ask --recover` hashes transcript user events after the saved offsets and returns only the exact matching final planner response. `cagy ask --forget` is the explicit escape hatch for corrupt or unrecoverable state and refuses while the developer is visibly working. New work is never sent while unresolved state exists.

The normal supervisor path uses the native cagy MCP tools (`delegate_task`, `task_status`, `recover_task`, `acknowledge_task`). Tasks arrive through structured JSON inputs, never through shell command strings. Shell CLI commands (`cagy ask --stdin`, `--recover`, `--forget`) remain available for emergency, scripting, and manual compatibility use.

Because the MCP bridge configuration is injected per-invocation when launching Codex, any already-running Codex supervisor sessions must be restarted (`cagy stop` followed by `cagy`) to receive the per-invocation MCP bridge. Stop sends one interrupt, waits briefly, then sends a second interrupt automatically if agy is still registered; this handles agy’s cancel-then-exit behavior in one command.

A delivery receipt is returned by `delegate_task` and `recover_task`. Codex must call `acknowledge_task` with the matching receipt to clear completed state. The journal persists as `completed_unacknowledged` until the receipt is acknowledged, so a restarted session can recover the exact answer without resubmitting the task.

## Quota Detection

First, new output from the current request is inspected. Strong case-insensitive patterns are:

```text
RESOURCE_EXHAUSTED
quota exceeded
quota exhausted
out of quota
out of credits
usage limit reached
rate limit exceeded
429 combined with quota or rate-limit text
```

A plain use of the word `quota` is not an error. The echoed task is removed before these patterns are checked, so a task that discusses or tests a quota error does not trigger recovery by itself.

Before a new task and every 45 seconds while it remains active, cagy runs bound-account headless checks:

```bash
agy -p "/model" --output-format json --print-timeout 30s
agy -p "/quota" --output-format json --print-timeout 30s
```

The active model chooses either `Gemini Models` or `Claude and GPT models`. The account is healthy only when weekly quota is above 3% and 5-hour quota is above 2%. A weekly `remaining_fraction` of 0.03 or lower, or a 5-hour value of 0.02 or lower, triggers recovery. Unknown models, missing groups, malformed data, and failed probes never trigger an account switch by themselves.

## Visible Transactional Native Account Recovery

When a typed agy probe or strong provider output confirms low or exhausted quota, cagy performs automatic recovery. Spinner-only output, malformed probes, and unknown errors never trigger account changes.

1. Persist the current account's quota state and task journal before mutation.
2. Build a deterministic candidate list: fresh known-available accounts first, then stale/unknown accounts that need a live probe. Exclude the current, attempted, missing-credential, disabled, needs-login, active-cooldown, and fresh low/exhausted accounts.
3. Verify pane ownership, workspace, project, developer target, and conversation identity.
4. Stop the developer and confirm target release before changing the canonical credential.
5. For each candidate once: refresh and verify identity, activate through the non-interactive canonical Keychain transaction, restart agy in the same pane, and live-probe `/model` and `/quota`. Persist every result.
6. Bind the first available account to the pane and continue. Before initial submission, send the original task once. After work has started, resume the exact conversation and send only the generic continuation prompt.
7. If a candidate also exhausts, stop it and continue through the remaining unique candidates inside the original task budget.
8. If the pool is exhausted, best-effort restore the original account and visible developer, keep uncertain task state when completion cannot be proven, and return one concise action to add or refresh an account. OAuth is never opened automatically.

A verified fresh `pending` session may be replaced with another fresh session only before the first task, when no conversation work exists. After submission, exact conversation identity is mandatory. The original task is never resent after partial execution.

## Missing-Developer Self-Healing

Every supervisor/developer pair has a deterministic developer name and persistent pane ownership metadata. When `cagy ask` receives `agent_not_found`:

1. Read the supervisor pane and list panes in its Herdr workspace.
2. Restrict candidates to the same tab and canonical project; unknown working directories fail closed.
3. Reuse exactly one pane whose `cagy_owner` matches and whose role is `developer` or `recovery`. Unsafe label-only adoption is prohibited; user-editable labels are never trusted.
4. Verify via Herdr `pane process-info` that the inactive pane is running a real interactive shell (`bash`, `zsh`, `sh`, `fish`, etc.); reject unreadable or non-shell processes.
5. If no candidate exists, create a new owned right pane.
6. An existing owned repair pane must have a valid persisted `cagy_session` and resumes only that ID. A newly created replacement starts a fresh conversation and never uses `--continue` or `--conversation`; its ID is saved after Herdr reports it, normally after the first completed task. If `StartAgy` succeeds but subsequent validation, metadata reporting, or prompt readiness fails, send Ctrl+C, wait for name release, leave a clean owned recovery shell, and report both original and cleanup errors.
7. If a newly created repair pane hits `agent_name_taken` while the existing developer is valid, automatically close the redundant pane and reuse the valid agent.
8. Fail without mutation when candidates are ambiguous, foreign, or running another agent. Startup failures leave the owned recovery pane visible so a later `ask` can retry.

`cagy doctor` remains read-only. Inside a cagy supervisor it checks the live deterministic developer, accepts only a verified fresh `pending` state or a valid `ready` `cagy_session` that matches Herdr's live conversation ID, and reports abandoned recovery panes.

## Runtime Identity

No database is needed. cagy keeps only small private state files for lock recovery, interrupted-task reconciliation. cagy derives a stable agent name from:

For the September 2026 local reliability-validation period, real cagy processes also write bounded diagnostics to `~/Library/Application Support/cagy/state/logs/cagy.log`. The logger is always on unless `CAGY_DIAGNOSTICS=0`, rotates at 8 MiB, keeps five backups, and enforces `0700`/`0600` permissions. Entries contain timestamps, PID, source file/line, safe lifecycle metadata, task hashes/byte counts, quota classifications, state transitions, retries, and errors. They must never contain task plaintext, completed answers, receipts, OAuth codes, tokens, passwords, Keychain values, or full transcripts. Go test binaries do not write to the user's real state directory.

- `HERDR_WORKSPACE_ID`
- the supervisor `HERDR_PANE_ID`

It passes these values to Codex through inherited `CAGY_*` environment variables. Pane operations are validated against the current workspace before mutation. The non-secret `cagy_session` pane token stores the exact Herdr-reported agy conversation ID and `cagy_session_state` records `pending` or `ready`. `pending` is allowed only for a live fresh process before its first prompt. It is never replaced with a “most recent” guess.

## Repository Layout

```text
cagy/
├── AGENTS.md
├── README.md
├── project-structure.md
├── go.mod
├── cmd/
│   └── cagy/
│       └── main.go
└── internal/
    ├── app/
    │   ├── app.go
    │   ├── commands.go
    │   ├── mcp.go            # local stdio MCP server and six native tools
    │   ├── mcp_config.go     # safe per-invocation Codex MCP configuration
    │   ├── recovery.go
    │   ├── task_state.go
    │   └── *_test.go
    ├── herdr/
    │   ├── client.go
    │   └── client_test.go
    ├── process/
    │   └── runner.go
    ├── quota/
    │   ├── quota.go
    │   └── quota_test.go
    └── transcript/
        ├── transcript.go
        └── transcript_test.go
```

`.help/` is read-only reference material and is not part of the product.

## Safety Boundaries

- Project paths and prompts are subprocess argument arrays, never shell strings. Supervisor task text arrives through MCP structured inputs without shell interpolation, is kept out of Codex launch arguments and persistent task state (journal stores only a SHA-256 hash), and is passed directly as a positional argument array element to `herdr agent prompt`.
- The `cagy mcp-server` child process communicates with Codex only through inherited stdin/stdout. It opens no socket, port, or external connection.
- Recovery uses argument arrays and fixed process boundaries; credentials never enter shell text.
- Pane IDs always come from Herdr JSON.
- cagy closes only the developer pane it can verify in the current workspace and project.
- No credentials or complete environment dumps are logged.
- Visible state is polled every second, bound quota every 45 seconds, healthy stalls at 150 seconds, heartbeats every five minutes, and the overall task budget is 30 minutes; recovery never loops forever.
- Startup, repair, doctor, and quota probes cannot touch the canonical Keychain item; low quota requires an explicit user account change.

## Automated Validation

```bash
gofmt -w cmd internal
go mod tidy
go mod verify
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/cagy
make verify
```

Automated tests use fakes only. With explicit user approval, visible end-to-end tests may verify fresh-project trust handling, first-task delivery, typed quota exhaustion, explicit account-change guidance, resumed work, and spinner/stall handling.
