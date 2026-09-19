#!/bin/sh
# cagy installer - secure one-command installer for Apple Silicon macOS
# https://github.com/kazimshah39/cagy

set -eu
(set -o pipefail 2>/dev/null) && set -o pipefail

GITHUB_REPO="${CAGY_REPO:-kazimshah39/cagy}"
INSTALL_DIR="${CAGY_INSTALL_DIR:-${INSTALL_DIR:-$HOME/.local/bin}}"
VERSION="${CAGY_VERSION:-${VERSION:-}}"
MODE="${CAGY_MODE:-auto}"
BASE_URL="${CAGY_DOWNLOAD_BASE_URL:-}"

print_usage() {
  cat <<EOF
cagy installer

Usage:
  install.sh [options]

Options:
  -d, --dir, -b, --bindir <path>  Install directory (default: ~/.local/bin)
  -v, --version <version>         Version or Git ref to install (default: main)
      --source                    Build from source using local Go toolchain
      --binary                    Install prebuilt release binary and verify checksum
  -h, --help                      Show this help message

Environment variables:
  CAGY_INSTALL_DIR, INSTALL_DIR   Install directory override
  CAGY_VERSION, VERSION           Version or ref override
  CAGY_MODE                       Install mode: auto, source, or binary
  CAGY_DOWNLOAD_BASE_URL          Custom download base URL (for mirrors/testing)
  CAGY_REPO                       GitHub repository (default: kazimshah39/cagy)
EOF
}

# Parse command line arguments
while [ "$#" -gt 0 ]; do
  case "$1" in
    -d|--dir|-b|--bindir)
      if [ -z "${2:-}" ]; then
        echo "Error: $1 requires a directory argument" >&2
        exit 1
      fi
      INSTALL_DIR="$2"
      shift 2
      ;;
    -v|--version)
      if [ -z "${2:-}" ]; then
        echo "Error: $1 requires a version argument" >&2
        exit 1
      fi
      VERSION="$2"
      shift 2
      ;;
    --source)
      MODE="source"
      shift
      ;;
    --binary)
      MODE="binary"
      shift
      ;;
    -h|--help)
      print_usage
      exit 0
      ;;
    *)
      echo "Error: Unknown option: $1" >&2
      print_usage >&2
      exit 1
      ;;
  esac
done

# cagy intentionally supports only Apple Silicon macOS. The Keychain-backed
# account manager requires Darwin arm64 and cgo; fail before any filesystem
# mutation when invoked elsewhere.
OS_RAW="$(uname -s)"
ARCH_RAW="$(uname -m)"
if [ "$OS_RAW" != "Darwin" ]; then
  echo "Error: cagy supports only macOS on Apple Silicon (darwin/arm64); detected ${OS_RAW}/${ARCH_RAW}." >&2
  exit 1
fi
case "$ARCH_RAW" in
  arm64|aarch64) ;;
  *)
    echo "Error: cagy supports only Apple Silicon (darwin/arm64); detected ${OS_RAW}/${ARCH_RAW}." >&2
    exit 1
    ;;
esac
OS="darwin"
ARCH="arm64"

# Helper: check Go version (must be >= 1.25)
check_go() {
  if ! command -v go >/dev/null 2>&1; then
    return 1
  fi
  GO_VER_STR="$(go version | awk '{print $3}' | sed 's/^go//')"
  MAJOR="$(echo "$GO_VER_STR" | cut -d. -f1)"
  MINOR="$(echo "$GO_VER_STR" | cut -d. -f2)"
  if [ -n "$MAJOR" ] && [ -n "$MINOR" ]; then
    if [ "$MAJOR" -gt 1 ] || { [ "$MAJOR" -eq 1 ] && [ "$MINOR" -ge 25 ]; }; then
      return 0
    fi
  fi
  return 2
}

# Validate install mode strictly
case "$MODE" in
  auto)
    if check_go; then
      RESOLVED_MODE="source"
    else
      RESOLVED_MODE="binary"
    fi
    ;;
  source|binary)
    RESOLVED_MODE="$MODE"
    ;;
  *)
    echo "Error: Invalid install mode: '${MODE}'. Supported modes are: auto, source, binary." >&2
    exit 1
    ;;
esac

# Check HTTP downloader
if command -v curl >/dev/null 2>&1; then
  DOWNLOADER="curl"
elif command -v wget >/dev/null 2>&1; then
  DOWNLOADER="wget"
else
  DOWNLOADER=""
fi

http_get() {
  url="$1"
  dest="$2"
  if [ "$DOWNLOADER" = "curl" ]; then
    curl -fsSL "$url" -o "$dest"
  elif [ "$DOWNLOADER" = "wget" ]; then
    wget -qO "$dest" "$url"
  else
    echo "Error: Neither curl nor wget was found. Please install curl or wget." >&2
    exit 1
  fi
}

http_get_stdout() {
  url="$1"
  if [ "$DOWNLOADER" = "curl" ]; then
    curl -fsSL "$url"
  elif [ "$DOWNLOADER" = "wget" ]; then
    wget -qO- "$url"
  else
    echo "Error: Neither curl nor wget was found. Please install curl or wget." >&2
    exit 1
  fi
}

compute_sha256() {
  file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$file" | awk '{print $NF}'
  else
    echo "Error: No SHA-256 tool found. Please install sha256sum, shasum, or openssl." >&2
    exit 1
  fi
}

# Determine whether install.sh was directly executed as a local file inside a clone.
# Piped invocations (curl ... | bash, sh < script) never qualify as local executions.
detect_local_repo() {
  # If $0 is a shell name or stdin descriptor, it was piped or read from stdin.
  case "$0" in
    bash|sh|zsh|-*|"")
      return 1
      ;;
  esac
  if [ ! -f "$0" ]; then
    return 1
  fi
  script_dir="$(cd "$(dirname "$0")" 2>/dev/null && pwd)" || return 1
  if [ -f "${script_dir}/go.mod" ] && [ -f "${script_dir}/cmd/cagy/main.go" ]; then
    if grep -q "module github.com/kazimshah39/cagy" "${script_dir}/go.mod" 2>/dev/null; then
      echo "$script_dir"
      return 0
    fi
  fi
  return 1
}

# Ensure install directory exists and is user-writable
if ! mkdir -p "$INSTALL_DIR" 2>/dev/null; then
  echo "Error: Cannot create directory ${INSTALL_DIR}." >&2
  echo "Please check permissions or choose another directory using --dir or CAGY_INSTALL_DIR." >&2
  exit 1
fi

if [ ! -w "$INSTALL_DIR" ]; then
  echo "Error: Install directory ${INSTALL_DIR} is not writable by current user." >&2
  echo "cagy does not require root/sudo. Please specify a user-writable directory (e.g. ~/.local/bin)." >&2
  exit 1
fi

# Set up temporary working directory
TMP_DIR="$(mktemp -d 2>/dev/null || mktemp -d -t 'cagy-install')"
TARGET_TMP=""

cleanup() {
  rm -rf "$TMP_DIR"
  if [ -n "${TARGET_TMP:-}" ] && [ -f "${TARGET_TMP:-}" ]; then
    rm -f "${TARGET_TMP:-}" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

# ==========================================
# Source Installation Mode (via Go toolchain)
# ==========================================
install_from_source() {
  go_status=0
  check_go || go_status=$?

  if [ "$go_status" -eq 1 ]; then
    echo "Error: Go toolchain not found on PATH." >&2
    echo "cagy source installation requires Go 1.25 or newer." >&2
    echo "Install Go from https://go.dev/dl/ or use prebuilt release binaries if published." >&2
    exit 1
  elif [ "$go_status" -eq 2 ]; then
    echo "Error: Go version is older than 1.25 ($(go version))." >&2
    echo "cagy requires Go 1.25 or newer to compile." >&2
    exit 1
  fi

  LOCAL_CLONE="$(detect_local_repo || true)"

  # Only use local checkout if directly executed as a local file, not piped, and no specific version was requested
  if [ -n "$LOCAL_CLONE" ] && [ -z "$BASE_URL" ] && [ -z "$VERSION" ]; then
    echo "Building cagy from local source tree at ${LOCAL_CLONE}..."
    SRC_DIR="$LOCAL_CLONE"
  else
    REF="${VERSION:-main}"
    echo "Fetching cagy source (${REF}) from github.com/${GITHUB_REPO}..."

    if [ -n "$BASE_URL" ]; then
      SRC_URL="${BASE_URL}/archive.tar.gz"
    else
      case "$REF" in
        v[0-9]*)
          SRC_URL="https://github.com/${GITHUB_REPO}/archive/refs/tags/${REF}.tar.gz"
          ;;
        *)
          SRC_URL="https://github.com/${GITHUB_REPO}/archive/refs/heads/${REF}.tar.gz"
          ;;
      esac
    fi

    if ! http_get "$SRC_URL" "$TMP_DIR/source.tar.gz"; then
      echo "Error: Failed to download source archive from ${SRC_URL}." >&2
      exit 1
    fi

    tar -xzf "$TMP_DIR/source.tar.gz" -C "$TMP_DIR"
    SRC_DIR="$(find "$TMP_DIR" -name "go.mod" -exec dirname {} \; | head -n 1)"

    if [ -z "$SRC_DIR" ] || [ ! -f "$SRC_DIR/cmd/cagy/main.go" ]; then
      echo "Error: Source archive did not contain expected cagy Go package." >&2
      exit 1
    fi
  fi

  echo "Compiling cagy binary..."
  CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -C "$SRC_DIR" -trimpath -ldflags "-s -w" -o "$TMP_DIR/cagy" ./cmd/cagy

  if [ ! -f "$TMP_DIR/cagy" ]; then
    echo "Error: Compilation failed to generate cagy binary." >&2
    exit 1
  fi
}

# ==========================================
# Binary Installation Mode (from Release)
# ==========================================
install_from_binary() {
  if [ -z "$DOWNLOADER" ]; then
    echo "Error: Neither curl nor wget was found. Please install curl or wget." >&2
    exit 1
  fi

  if [ -z "$VERSION" ]; then
    echo "Resolving latest cagy release..."
    TAG=""
    if [ "$DOWNLOADER" = "curl" ]; then
      EFFECTIVE_URL="$(curl -fsSIL -o /dev/null -w "%{url_effective}" "https://github.com/${GITHUB_REPO}/releases/latest" 2>/dev/null || true)"
      case "$EFFECTIVE_URL" in
        */releases/tag/*)
          TAG="${EFFECTIVE_URL##*/}"
          ;;
      esac
    fi

    if [ -z "$TAG" ]; then
      LATEST_JSON="$(http_get_stdout "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" 2>/dev/null || true)"
      TAG="$(echo "$LATEST_JSON" | grep '"tag_name":' | head -n 1 | sed -E 's/.*"tag_name":[[:space:]]*"([^"]+)".*/\1/' || true)"
    fi

    if [ -z "$TAG" ]; then
      echo "Error: Could not resolve latest release version from ${GITHUB_REPO}." >&2
      echo "Ensure prebuilt releases have been published, or install from source using --source." >&2
      exit 1
    fi
  else
    case "$VERSION" in
      v*) TAG="$VERSION" ;;
      *)  TAG="v$VERSION" ;;
    esac
  fi

  VERSION_NUM="${TAG#v}"
  ARCHIVE_NAME="cagy_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
  CHECKSUMS_NAME="checksums.txt"

  if [ -n "$BASE_URL" ]; then
    ARCHIVE_URL="${BASE_URL}/${ARCHIVE_NAME}"
    CHECKSUMS_URL="${BASE_URL}/${CHECKSUMS_NAME}"
  else
    ARCHIVE_URL="https://github.com/${GITHUB_REPO}/releases/download/${TAG}/${ARCHIVE_NAME}"
    CHECKSUMS_URL="https://github.com/${GITHUB_REPO}/releases/download/${TAG}/${CHECKSUMS_NAME}"
  fi

  echo "Downloading cagy ${TAG} for ${OS}/${ARCH}..."
  if ! http_get "$CHECKSUMS_URL" "$TMP_DIR/$CHECKSUMS_NAME"; then
    echo "Error: Failed to download ${CHECKSUMS_NAME} from ${CHECKSUMS_URL}." >&2
    echo "If no release binary is published yet, install from source: install.sh --source" >&2
    exit 1
  fi

  if ! http_get "$ARCHIVE_URL" "$TMP_DIR/$ARCHIVE_NAME"; then
    echo "Error: Failed to download release archive from ${ARCHIVE_URL}." >&2
    exit 1
  fi

  # Exact filename match in checksums file
  EXPECTED_HASH="$(awk -v target="$ARCHIVE_NAME" '
    $2 == target || $2 == "*"target { print $1; exit }
    NF >= 2 && $NF == target { print $1; exit }
  ' "$TMP_DIR/$CHECKSUMS_NAME" || true)"

  if [ -z "$EXPECTED_HASH" ]; then
    echo "Error: Checksum for ${ARCHIVE_NAME} not found in ${CHECKSUMS_NAME}." >&2
    exit 1
  fi

  # Validate that the expected hash is exactly 64 hexadecimal characters
  case "$EXPECTED_HASH" in
    *[!0-9a-fA-F]*)
      echo "Error: Checksum for ${ARCHIVE_NAME} in ${CHECKSUMS_NAME} contains non-hexadecimal characters: '${EXPECTED_HASH}'." >&2
      exit 1
      ;;
  esac
  if [ "${#EXPECTED_HASH}" -ne 64 ]; then
    echo "Error: Checksum for ${ARCHIVE_NAME} in ${CHECKSUMS_NAME} has invalid length ${#EXPECTED_HASH} (expected 64 hex characters)." >&2
    exit 1
  fi

  ACTUAL_HASH="$(compute_sha256 "$TMP_DIR/$ARCHIVE_NAME")"
  EXPECTED_LOWER="$(echo "$EXPECTED_HASH" | tr '[:upper:]' '[:lower:]')"
  ACTUAL_LOWER="$(echo "$ACTUAL_HASH" | tr '[:upper:]' '[:lower:]')"

  if [ "$EXPECTED_LOWER" != "$ACTUAL_LOWER" ]; then
    echo "Error: SHA-256 checksum verification failed for ${ARCHIVE_NAME}!" >&2
    echo "  Expected: ${EXPECTED_LOWER}" >&2
    echo "  Actual:   ${ACTUAL_LOWER}" >&2
    exit 1
  fi

  echo "✓ Checksum verified"
  tar -xzf "$TMP_DIR/$ARCHIVE_NAME" -C "$TMP_DIR"

  if [ ! -f "$TMP_DIR/cagy" ]; then
    echo "Error: Release archive did not contain 'cagy' binary." >&2
    exit 1
  fi
}

# Execute chosen installation mode
if [ "$RESOLVED_MODE" = "source" ]; then
  install_from_source
else
  install_from_binary
fi

# Collision-safe atomic install inside the destination directory
chmod 755 "$TMP_DIR/cagy"
TARGET_TMP="$(mktemp "${INSTALL_DIR}/.cagy.install.XXXXXX" 2>/dev/null || mktemp "${INSTALL_DIR}/cagy.tmp.XXXXXX")"
cp "$TMP_DIR/cagy" "$TARGET_TMP"
chmod 755 "$TARGET_TMP"
mv -f "$TARGET_TMP" "$INSTALL_DIR/cagy"
TARGET_TMP=""

echo "✓ Successfully installed cagy to ${INSTALL_DIR}/cagy"

# PATH verification & guidance
case ":$PATH:" in
  *":$INSTALL_DIR:"*)
    ;;
  *)
    echo ""
    echo "Note: ${INSTALL_DIR} is not currently in your PATH."
    echo "To run cagy directly, add it to your shell profile:"
    echo ""
    echo "  # For zsh (~/.zshrc):"
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    echo ""
    echo "  # For bash (~/.bashrc or ~/.bash_profile):"
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    echo ""
    ;;
esac
