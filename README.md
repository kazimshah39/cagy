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
- uses your local Go toolchain (Go 1.22+) to build a clean binary;
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

### Supported Platforms

| Operating System | Architecture | Target |
| :--- | :--- | :--- |
| **macOS** | Apple Silicon (`arm64`) | Supported |
| **macOS** | Intel (`amd64`) | Supported |
| **Linux** | 64-bit ARM (`arm64` / `aarch64`) | Supported |
| **Linux** | 64-bit x86 (`amd64` / `x86_64`) | Supported |

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
- [agm](https://github.com/shyim/agm)
- Go 1.22+ (used for source compilation and building)

AGM stays an unchanged external dependency. cagy also needs Herdr's official agy integration so complete answers can be read from agy's transcript:

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

Talk to Codex in the left pane. Codex sends work to agy with `cagy ask`.

Other commands:

```bash
cagy doctor
cagy stop
```

Before each delegated task, cagy checks agy's real `/model` and `/quota` status. While a task is running, it checks again after each five-minute wait segment if agy appears stuck. The task is submitted only once. A transient Herdr `idle` or `done` state does not finish the task.

Herdr's pane remains the visible status and progress view. cagy waits for agy's real footer to stay idle, treats `esc to cancel` and a non-zero `task(s)` count as still working, and watches separately for a blocked state. It then reads the exact task's final answer from agy's JSONL transcript instead of terminal scrollback. `transcript_full.jsonl` is preferred so long answers are not cut off; the compact transcript is used only when the full file does not exist. Intermediate progress messages, tool output, model reasoning, system messages, old turns, and blocked-screen scrollback are never returned as the answer. If the transcript integration is missing or the final record is incomplete, cagy stops with a clear error instead of silently returning a partial answer.

`cagy ask --help` and `cagy ask -h` show local help immediately; they are never sent to agy.

When the active model's weekly quota is 3% or less, or the 5-hour quota is 2% or less, cagy visibly runs:

```bash
agm refresh-all
agm auto-switch --min 5
```

`agm refresh-all` runs only when recovery is needed and at most once per hour, so it never runs from an idle timer. Partial refresh failures are allowed only when at least one account refreshed successfully. After switching, cagy checks the new account's real quota before restarting agy with `--continue` and resuming the same task. Recovery stops after two failed attempts. If quota data cannot be read, cagy does not switch blindly.

cagy ignores quota-error examples inside the echoed task text. It only treats new developer output as the signal. Recovery command completion also requires a numeric marker, so Herdr's echoed shell command cannot finish a wait early.

> YOLO mode lets both agents make changes without approval prompts. Use cagy only in projects and environments you trust.

---

## Manual Release Publishing (Optional)

Releases can be generated and published locally without automated CI:

1. **Build cross-platform release packages and checksums**:
   ```bash
   make release-build
   ```
   This generates `dist/cagy_<version>_<os>_<arch>.tar.gz` for macOS and Linux (`amd64` and `arm64`) along with `dist/checksums.txt`.

2. **Publish on GitHub**:
   Use GitHub CLI or the GitHub Releases web interface to upload the assets:
   ```bash
   gh release create v0.1.0 --title "cagy v0.1.0" --notes "Release v0.1.0" dist/*.tar.gz dist/checksums.txt
   ```

3. **Verify Checksums Manually**:
   ```bash
   # On macOS:
   shasum -a 256 -c checksums.txt --ignore-missing

   # On Linux:
   sha256sum -c checksums.txt --ignore-missing
   ```
