# cagy

`cagy` runs a visible two-agent setup inside Herdr:

- **Codex** on the left as supervisor.
- **agy** on the right as developer.

Both always run in YOLO mode. cagy trusts the selected project for that Codex invocation and visibly accepts agy's project trust screen when its default **Yes** option is selected. It waits for agy's real input prompt before starting Codex. Herdr shows whether each agent is working, blocked, done, or idle.

By default, Herdr's Agent sidebar shows one **cagy** row. That row is the Codex supervisor, so its working, blocked, done, or idle indicator reports Codex's progress. The agy developer keeps running visibly in the right pane but is filtered out of the Agent sidebar.

To show both real agents, start with:

```bash
cagy --show-agents
```

Expanded mode shows **cagy Supervisor** and **cagy Developer**. The names are display-only: Codex and agy keep their detected identities, lifecycle state, and cagy's unique internal developer target.

---

## Installation

### One-Command Install (Recommended)

Install `cagy` to `~/.local/bin` in one command:

```bash
curl -fsSL https://raw.githubusercontent.com/kazimshah39/cagy/main/install.sh | bash
```

The installer:

- supports only Apple Silicon macOS (`darwin/arm64`);
- installs to `~/.local/bin` without requiring `sudo` or root permissions;
- also supports downloading prebuilt release binaries (`--binary`) with SHA-256 checksum verification if published.

#### Custom Directory or Version

You can customize the destination directory or install a specific Git branch or tag:

```bash
curl -fsSL https://raw.githubusercontent.com/kazimshah39/cagy/main/install.sh | bash -s -- --dir "$HOME/bin" --version main
```

Or using environment variables:

```bash
curl -fsSL https://raw.githubusercontent.com/kazimshah39/cagy/main/install.sh | CAGY_INSTALL_DIR="$HOME/bin" bash
```

#### Prebuilt Binary Mode (Optional)

If prebuilt release binaries and `checksums.txt` have been published on GitHub:

```bash
curl -fsSL https://raw.githubusercontent.com/kazimshah39/cagy/main/install.sh | bash -s -- --binary --version v0.1.0
```

### Install with Go

You can also install directly via the Go CLI:

```bash
go install -trimpath github.com/kazimshah39/cagy/cmd/cagy@latest
```

### Supported Platform

| Operating System | Architecture            | Target    |
| :--------------- | :---------------------- | :-------- |
| **macOS**        | Apple Silicon (`arm64`) | Supported |

### PATH Configuration

If `~/.local/bin` is not yet in your shell's `PATH`, add it to your profile:

**For Zsh** (`~/.zshrc`, default on macOS):

```bash
export PATH="$HOME/.local/bin:$PATH"
```

**For Bash** (`~/.bashrc` or `~/.bash_profile`):

```bash
export PATH="$HOME/.local/bin:$PATH"
```

Then reload your shell:

```bash
source ~/.zshrc   # or source ~/.bashrc
```

Verify the installation:

```bash
cagy --help
```

### Upgrade

To update `cagy` to the latest version, re-run the one-command installer:

```bash
curl -fsSL https://raw.githubusercontent.com/kazimshah39/cagy/main/install.sh | bash
```

Or with Go:

```bash
go install -trimpath github.com/kazimshah39/cagy/cmd/cagy@latest
```

### Uninstall

To uninstall `cagy`, delete the binary from your install directory:

```bash
rm -f "$HOME/.local/bin/cagy"
```

---

## Requirements

- Herdr
- Codex CLI
- agy CLI
- Go 1.25+ (required for source compilation and building)

cagy stores imported agy account snapshots as private local files. Normal startup, repair, quota monitoring, and `cagy accounts doctor` do not touch agy's canonical `gemini` Keychain item. When quota is confirmed low or exhausted during delegated work, cagy automatically replaces the canonical item through its non-interactive delete-and-add flow, restarts agy in the same verified pane, live-checks the next stored account, and continues the task. This flow does not ask for the macOS login password and never opens Google automatically. Browser login is started only by the explicit `cagy accounts add` command. The legacy external account manager is not required, installed, or used. cagy also needs Herdr's official agy integration so complete answers can be read from agy's transcript:

```bash
herdr integration install antigravity-cli
```

---

## Build from Source

Clone the repository and build using Make or Go:

```bash
git clone https://github.com/kazimshah39/cagy.git
cd cagy
make build
```

Or directly with `go`:

```bash
go build -trimpath -o cagy ./cmd/cagy
```

To run all automated verification checks (formatting, vet, race tests, installer tests):

```bash
make verify
```

---

## Use

Open a project shell inside Herdr, then run:

```bash
cagy
```

Or choose another project directory:

```bash
cagy /path/to/project
```

Show both agents in the sidebar when needed:

```bash
cagy --show-agents
cagy --show-agents /path/to/project
```

The compact/expanded choice uses Herdr's transient Agent-view API. Herdr supports one active Agent view per server, so compact mode becomes the active view while leaving every non-cagy agent visible. Expanded mode clears the view only when cagy still owns it; it does not remove a view installed later by another tool.

Talk to Codex in the left pane. Codex delegates implementation to agy through the native cagy MCP bridge — a required local stdio server that Codex initializes automatically. Codex uses structured MCP tools instead of shell commands, eliminating supervisor shell interpolation and keeping task text out of Codex supervisor launch arguments and durable journals. When cagy delegates to the developer, prompt text is passed directly as a positional argument array element to `herdr agent prompt`:

- **`delegate_task`** — send a task to agy; returns the final answer and a delivery receipt.
- **`task_status`** — inspect the current status of any delegated task (read-only).
- **`recover_task`** — retrieve the exact completed answer after an interruption without resubmitting (persists or migrates the delivery receipt safely).
- **`acknowledge_task`** — confirm receipt; clears durable task state only after the answer is in Codex's context.
- **`forget_task`** — explicitly discard unrecoverable state (requires confirmation; refused while agy is working).
- **`developer_status`** — inspect agy's pane, session readiness, and task summary (read-only).

The MCP bridge is a Codex-managed local stdio child process. It is not a daemon, network listener, or native Codex subagent. No global Codex configuration or plugin installation is required.

> **Note:** Because the MCP bridge configuration is injected per-invocation when launching Codex, any already-running Codex supervisor sessions must be restarted (`cagy stop` followed by `cagy`) to receive the per-invocation MCP bridge.

### Compatibility and Emergency Fallback

`cagy ask --stdin` is preserved strictly as a compatibility and emergency fallback command for manual scripting or situations where the supervisor needs a direct CLI escape hatch:

```bash
cat <<'CAGY_TASK' | cagy ask --stdin
Implement the requested change safely.
CAGY_TASK
```

Direct `cagy ask "<task>"` also remains available as a manual fallback for short tasks.

Other commands:

```bash
cagy doctor
cagy stop

# Native agy account manager (simple private local credential snapshots)
cagy accounts add
cagy accounts import-active
cagy accounts list
cagy accounts status [ACCOUNT]
cagy accounts switch ACCOUNT
cagy accounts remove ACCOUNT --yes
cagy accounts doctor
cagy accounts export --output FILE
cagy accounts export --metadata-only --output FILE
cagy accounts import FILE
```

Encrypted exports require an interactive passphrase and never activate an account automatically. Metadata-only exports contain full account labels and email addresses, but no credentials or OAuth tokens. `accounts doctor` reports incomplete transactions or missing local credential snapshots without changing Keychain state. When an account catalog exists, the main `cagy doctor` also checks native account health. A global lock prevents concurrent account changes.

`cagy accounts add` runs its own loopback OAuth callback with PKCE, opens Google only because the user explicitly requested login, verifies the returned Google identity, and stores the agy-compatible credential only in cagy's private local vault. It never reads or changes the existing `gemini` Keychain item, and it does not change agy's currently active login. Normal Google consent or MFA may still appear in the browser.

CAGY does not commit Google OAuth client credentials in source control. Before using `cagy accounts add`, or before refreshing an account after its access token expires, configure the same OAuth client locally in your shell:

```bash
export CAGY_GOOGLE_CLIENT_ID="your-google-oauth-client-id"
export CAGY_GOOGLE_CLIENT_SECRET="your-google-oauth-client-secret"
```

Keep those values in your local shell configuration only; never commit them to this repository. Imported accounts can still be listed and inspected without opening OAuth.

`cagy accounts switch ACCOUNT` does not run agy and does not open OAuth. It refreshes the selected stored credential directly with Google, verifies the account identity, and then replaces agy's canonical Keychain item using the same delete-and-add, allow-all flow as AGM. This avoids legacy ACL password prompts. Restart the active agy/cagy session after switching so the new process reads the selected account.

Before each delegated task, cagy checks agy's real `/model` and `/quota` status. During a task it polls visible state every second, probes agy's current session every 45 seconds, and treats 150 seconds without meaningful progress as a healthy-stall candidate. Five-minute messages are only user-facing heartbeats; the overall task deadline is 30 minutes. If quota is low, cagy automatically rotates through eligible stored accounts. Before work starts, the original task is submitted exactly once after a healthy account is active. After work starts, cagy resumes the exact conversation and sends only a generic continuation instruction, never the original prompt again. A transient Herdr `idle` or `done` state does not finish the task.

Herdr's pane remains the visible status and progress view. cagy waits for agy's real footer to stay idle, treats `esc to cancel` and a non-zero `task(s)` count as still working, and watches separately for a blocked state. It also follows transcript background-task lifecycle events, so a temporary idle footer and an intermediate “still waiting” message cannot be mistaken for the final answer. It then reads the exact task's final answer from agy's JSONL transcript instead of terminal scrollback. `transcript_full.jsonl` is preferred so long answers are not cut off; the compact transcript is used only when the full file does not exist. Intermediate progress messages, tool output, model reasoning, system messages, old turns, and blocked-screen scrollback are never returned as the answer. If the transcript integration is missing or the final record is incomplete, cagy stops with a clear error instead of silently returning a partial answer.

`cagy ask --help` and `cagy ask -h` show local help immediately; they are never sent to agy.

Before one task is submitted, cagy atomically writes a bounded private `0600` journal inside a `0700` user state directory. Symlinks, non-regular files, oversized files, and unsafe permissions are rejected. The journal contains cagy/Herdr identity, timestamps, transcript offsets, and a SHA-256 task identifier. It never contains task text, credentials, model output, or reasoning. cagy prints an immediate submission message and a bounded heartbeat every five minutes to stderr; the final agy answer remains the only normal stdout payload.

If the shell, terminal host, Codex tool runner, or `cagy ask` process disappears, agy can continue in the visible pane. The next `cagy doctor` reconciles the journal with the verified live developer and exact JSONL transcript. It reports whether the task is still running, blocked, completed without acknowledgment, or uncertain. cagy never resends that task automatically.

Recover a completed answer after a lost caller with:

```bash
cagy ask --recover
```

After manually inspecting the visible developer pane, discard stale or unrecoverable state with:

```bash
cagy ask --forget
```

`--forget` refuses while the developer is still visibly working. A crashed caller's lock is reclaimed only after its recorded process is confirmed dead; a live lock remains exclusive.

cagy classifies the active agy session's quota as available, low, exhausted, or unknown. Unknown or malformed results never trigger switching. When quota is confirmed low or exhausted, cagy automatically tries each eligible stored account at most once. Fresh known-good accounts are preferred; stale or unknown accounts are refreshed, identity-validated, started, and live-probed before use. Fresh low/exhausted, disabled, needs-login, missing-credential, current, cooling-down, and already-attempted accounts are skipped. A successful account becomes the active default and is bound to the visible developer pane. If every reusable account is unavailable, cagy restores the original visible developer when safe and prints one clear instruction to add or refresh an account. It never opens OAuth automatically.

## Diagnostic logs

During the current local reliability-testing period, detailed diagnostics are enabled by default for real cagy processes. The active log is:

```text
~/Library/Application Support/cagy/state/logs/cagy.log
```

Each line includes a UTC timestamp, process ID, source file/line, lifecycle event, and safe metadata. cagy logs startup, Herdr pane/agent operations, account binding and switching, quota probes, MCP tool calls, task-journal phases, recovery, acknowledgements, retries, timeouts, and failures. It records task byte counts and short SHA-256 fingerprints instead of task text. It never intentionally records credentials, authorization codes, bearer tokens, passwords, receipts, complete transcripts, or model answers.

Logs are private (`0600`) inside a private directory (`0700`). The active file rotates at 8 MiB and keeps at most five backups (`cagy.log.1` through `cagy.log.5`). To temporarily disable diagnostics for one run:

```bash
CAGY_DIAGNOSTICS=0 cagy
```

When reporting a problem, keep the terminal error and the matching time. That is enough to locate the relevant events in the diagnostic log without reproducing the whole workflow.

cagy stores non-secret ownership metadata (`cagy_owner` and `cagy_role`) on its panes, the exact agy conversation ID as `cagy_session`, and a `cagy_session_state` marker on the developer pane. A brand-new agy process is marked `pending` until its first prompt lets Herdr report the conversation; completed tasks store the exact ID and mark it `ready`. If the named developer is unexpectedly missing, the next `cagy ask` safely repairs one matching pane or creates a replacement. Owned metadata is strictly required; user-editable labels are never trusted. Inactive panes reused for repair must be verified as interactive shells via Herdr `pane process-info`. It never adopts ambiguous, cross-workspace, cross-tab, cross-project, unreadable, non-shell, or active-agent panes. Unknown working directories fail closed. If a newly created repair pane encounters `agent_name_taken` while the existing developer is valid, the extra pane is automatically closed. `cagy stop` validates ownership and scope, sends Ctrl+C, and automatically sends a second Ctrl+C after a short grace period when agy remains open (agy commonly uses the first press to cancel and the second to exit). It waits for confirmed agent release and never closes the pane if stopping still fails.

When run inside a cagy supervisor, `cagy doctor` also checks the live named developer, accepts only a verified fresh `pending` state or a `ready` `cagy_session` that matches Herdr's live conversation ID, and reports abandoned recovery panes. A new agy session does not report its ID until its first prompt. If preflight quota is low while that verified fresh session is still `pending`, cagy may switch accounts and start another fresh session because no task has begun. Once work starts, automatic recovery requires and resumes the exact reported conversation ID. After task completion, cagy saves that ID automatically. This prevents a dependency-only false green result while keeping first-task recovery automatic.

cagy ignores quota-error examples inside the echoed task text. It only treats new developer output as the signal. Recovery command completion also requires a numeric marker, so Herdr's echoed shell command cannot finish a wait early.

> YOLO mode lets both agents make changes without approval prompts. Use cagy only in projects and environments you trust.

---

## Manual Release Publishing (Optional)

Releases can be generated and published locally without automated CI:

1. **Build the Apple Silicon macOS release package and checksum**:

   ```bash
   make release-build
   ```

   This generates `dist/cagy_<version>_darwin_arm64.tar.gz` and `dist/checksums.txt`. Releases intentionally contain only the `darwin/arm64` cgo build.

2. **Publish on GitHub**:
   Use GitHub CLI or the GitHub Releases web interface to upload the assets:

   ```bash
   gh release create v0.1.0 --title "cagy v0.1.0" --notes "Release v0.1.0" dist/*.tar.gz dist/checksums.txt
   ```

3. **Verify Checksums Manually**:

   ```bash
   # On macOS:
   shasum -a 256 -c checksums.txt --ignore-missing

   ```
