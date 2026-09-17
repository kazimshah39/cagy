# cagy Project Structure

**Status:** Implemented and tested
**Date:** 2026-09-17

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

Runtime speed is mostly controlled by Codex, agy, Herdr, and AGM, so Rust would add build complexity without a useful user-visible speed gain.

## Commands

```bash
cagy [DIRECTORY]                     # Start with one Codex-based cagy sidebar row
cagy --show-agents [DIRECTORY]       # Show both supervisor and developer rows
cagy doctor                          # Check Herdr context and required CLIs
cagy stop              # Close the verified developer pane
cagy ask "<task>"      # Internal command used by Codex to delegate
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
agy --continue --dangerously-skip-permissions --mode accept-edits
```

These YOLO flags are required and not configurable. The trusted-project override applies only to this Codex invocation and prevents startup from pausing at the directory trust question.

## Dependencies

Executables on `PATH`:

- `herdr`
- `codex`
- `agy`
- `agm`
- Herdr's current `antigravity-cli` integration, installed with `herdr integration install antigravity-cli`

Build dependency:

- Go 1.22 or newer

`agm` is used only through its public command line. cagy never imports AGM code, reads its database, or handles its credentials.

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

1. Acquires one per-developer lock. There is no queue.
2. Resolves the named developer and validates its workspace and project.
3. Checks agy's machine-readable `/model` and `/quota` status before submitting work. If the confirmed quota is low, recovery happens first. A failed or unknown probe does not cause a blind switch.
4. Resolves Herdr's agy conversation ID and records separate byte offsets for `transcript.jsonl` and `transcript_full.jsonl` when the session already exists.
5. Reads a baseline of recent developer output for visible status and quota-error checks.
6. Sends the task once with `herdr agent prompt --wait` using a five-minute wait segment.
7. Treats Herdr `idle` and `done` results as hints only because they can appear briefly while agy is still processing. Subsequent Herdr waits match only `blocked`.
8. Reads the current visible screen only for lifecycle markers. `esc to cancel` or a non-zero `task(s)` footer count means working. `? for shortcuts` without either working signal means idle. Marker text in old response content is ignored.
9. Requires the real idle footer to remain stable for the transcript flush grace period. A non-empty planner message seen while agy is working can be only a progress update, so it is not returned early.
10. If a five-minute prompt wait times out, reads new terminal output and checks `/model` and `/quota` again. A five-second `agent_prompt_stalled` result does not consume a five-minute segment. The task is never resent.
11. Stops waiting after the fixed 30-minute task budget.
12. After stable completion, resolves the conversation reported by Herdr and reads only new JSONL events after the checkpoint. It matches the exact task's `USER_INPUT`, then returns the last non-empty `MODEL` + `PLANNER_RESPONSE` + `DONE` content.
13. Uses `transcript_full.jsonl` whenever it exists because the compact `transcript.jsonl` can truncate long content. The compact file is only a fallback when the full file does not exist.
14. Never prints model reasoning, tool events, system messages, old turns, the full transcript, or terminal scrollback from a blocked task. The terminal remains the visible progress/status view but is not the completed-answer transport.
15. If the session or complete final transcript event is missing, returns a clear error instead of possibly truncated terminal text.
16. Removes the echoed task before checking strong quota-error patterns, so task examples cannot trigger false recovery.
17. If strong output evidence or a successful low-quota probe appears, starts visible recovery.
18. Releases the lock.

Herdr still owns panes, identity, blocked detection, and the visible terminal. cagy combines its verified agent/session data with agy's footer and structured transcripts because no one signal is reliable enough by itself for turn completion.

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

Before a new task and whenever a five-minute Herdr wait expires, cagy runs read-only headless checks:

```bash
agy -p "/model" --output-format json --print-timeout 30s
agy -p "/quota" --output-format json --print-timeout 30s
```

The active model chooses either `Gemini Models` or `Claude and GPT models`. The account is healthy only when weekly quota is above 3% and 5-hour quota is above 2%. A weekly `remaining_fraction` of 0.03 or lower, or a 5-hour value of 0.02 or lower, triggers recovery. Unknown models, missing groups, malformed data, and failed probes never trigger an account switch by themselves.

## Visible AGM Recovery

Recovery is limited to two attempts per task.

1. Resolve and verify the current developer pane.
2. Stop agy, close that pane, and create a fresh right pane.
3. If no confirmed bulk refresh was recorded in the last hour, run `agm refresh-all` visibly. This check is lazy: it runs only during recovery, never from a daemon or idle timer. Continue only if its summary reports at least one successful refresh; partial failures remain visible. Wait for a numeric completion marker so the shell's echoed command cannot be mistaken for completion.
4. Run `agm auto-switch --min 5` visibly.
5. Wait for `Switch to this account? [y/N]:` and send `y` plus Enter.
6. Read visible output. A non-zero exit caused only by the IDE is acceptable when `✓ Antigravity CLI (agy)` confirms the CLI credential changed.
7. Immediately verify the selected account with agy's `/model` and `/quota` JSON commands. Reject the account if quota is low or cannot be read.
8. Start agy with `--continue` and strict YOLO arguments, then restore the guarded `cagy Developer` sidebar label.
9. Handle the trust screen if it appears and wait for the real input prompt.
10. If recovery happened before task submission, send the original task. Otherwise, send a continuation task that tells agy to inspect the working tree and avoid repeating completed work.
11. If quota is hit again, repeat once. Then stop with a clear error and leave the final right pane visible for diagnosis.

## Runtime Identity

No database is needed. cagy derives a stable agent name from:

- `HERDR_WORKSPACE_ID`
- the supervisor `HERDR_PANE_ID`

It passes these values to Codex through inherited `CAGY_*` environment variables. Pane operations are validated against the current workspace before mutation.

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
    │   ├── recovery.go
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

- Project paths and prompts are subprocess arguments, never shell text.
- Shell text used in recovery contains only fixed AGM commands and random hexadecimal completion markers. Waits require a numeric marker status and cannot match the echoed command's `%s` format.
- Pane IDs always come from Herdr JSON.
- cagy closes only the developer pane it can verify in the current workspace and project.
- No credentials or complete environment dumps are logged.
- Herdr waits are split into five-minute segments with a 30-minute budget, and recovery never loops forever.
- The hourly AGM refresh timestamp contains no credentials.
- A failed or unknown quota probe cannot switch an account, and every switched account is verified before agy restarts.

## Automated Validation

```bash
gofmt -w cmd internal
go test ./...
go vet ./...
go build ./cmd/cagy
```

Automated tests use fakes only. With explicit user approval, visible end-to-end tests verified fresh-project trust handling, first-task delivery, quota-triggered AGM refresh and switching, resumed work, and the one-hour refresh skip.
