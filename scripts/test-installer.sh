#!/usr/bin/env bash
# End-to-end installer test suite
# Validates source builds, piped runs inside clone, env passing, strict modes, collision safety, and checksum rules

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

echo "=== Testing cagy install.sh ==="

TEST_TMP="$(mktemp -d 2>/dev/null || mktemp -d -t 'cagy-test-installer')"
SERVER_DIR="${TEST_TMP}/server"
TARGET_LOCAL_SRC="${TEST_TMP}/bin_local_src"
TARGET_PIPED_INSIDE_CLONE="${TEST_TMP}/bin_piped_inside_clone"
TARGET_ENV_PIPE="${TEST_TMP}/bin_env_pipe"
TARGET_BINARY="${TEST_TMP}/bin_binary"
TARGET_AMBIGUOUS="${TEST_TMP}/bin_ambiguous"
TARGET_MALFORMED="${TEST_TMP}/bin_malformed"

mkdir -p "${SERVER_DIR}" "${TARGET_LOCAL_SRC}" "${TARGET_PIPED_INSIDE_CLONE}" \
         "${TARGET_ENV_PIPE}" "${TARGET_BINARY}" "${TARGET_AMBIGUOUS}" "${TARGET_MALFORMED}"

cleanup() {
  if [ -n "${SERVER_PID:-}" ]; then
    kill "${SERVER_PID}" 2>/dev/null || true
    wait "${SERVER_PID}" 2>/dev/null || true
  fi
  rm -rf "${TEST_TMP}"
}
trap cleanup EXIT INT TERM

# Detect OS and Arch
OS_RAW="$(uname -s)"
case "$OS_RAW" in
  Darwin*) OS="darwin" ;;
  Linux*)  OS="linux" ;;
  *)       echo "Unsupported test OS: $OS_RAW"; exit 1 ;;
esac

ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *)            echo "Unsupported test ARCH: $ARCH_RAW"; exit 1 ;;
esac

# ----------------------------------------------------
# Prepare Mock HTTP Server with Source & Binary Assets
# ----------------------------------------------------
VERSION="0.1.0"
ARCHIVE_NAME="cagy_${VERSION}_${OS}_${ARCH}.tar.gz"

# 1. Mock remote source tarball (archive.tar.gz)
SRC_STAGE="${TEST_TMP}/src_stage/cagy-main"
mkdir -p "${SRC_STAGE}/cmd/cagy" "${SRC_STAGE}/internal"
cp -r "${REPO_ROOT}/go.mod" "${SRC_STAGE}/"
cp -r "${REPO_ROOT}/cmd" "${SRC_STAGE}/"
cp -r "${REPO_ROOT}/internal" "${SRC_STAGE}/"
tar -czf "${SERVER_DIR}/archive.tar.gz" -C "${TEST_TMP}/src_stage" cagy-main

# 2. Mock prebuilt release binary and checksums
BIN_STAGE="${TEST_TMP}/bin_stage"
mkdir -p "${BIN_STAGE}"
CGO_ENABLED=0 go build -C "${REPO_ROOT}" -trimpath -ldflags "-s -w" -o "${BIN_STAGE}/cagy" ./cmd/cagy
tar -czf "${SERVER_DIR}/${ARCHIVE_NAME}" -C "${BIN_STAGE}" cagy

if command -v sha256sum >/dev/null 2>&1; then
  REAL_SHA256="$(sha256sum "${SERVER_DIR}/${ARCHIVE_NAME}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  REAL_SHA256="$(shasum -a 256 "${SERVER_DIR}/${ARCHIVE_NAME}" | awk '{print $1}')"
else
  REAL_SHA256="$(openssl dgst -sha256 "${SERVER_DIR}/${ARCHIVE_NAME}" | awk '{print $NF}')"
fi

echo "${REAL_SHA256}  ${ARCHIVE_NAME}" > "${SERVER_DIR}/checksums.txt"

PORT=$(python3 -c 'import socket; s = socket.socket(); s.bind(("", 0)); print(s.getsockname()[1]); s.close()')
python3 -m http.server "${PORT}" --directory "${SERVER_DIR}" >/dev/null 2>&1 &
SERVER_PID=$!
sleep 1
BASE_URL="http://127.0.0.1:${PORT}"
echo "Local mock server running at ${BASE_URL} (PID: ${SERVER_PID})"

# ----------------------------------------------------
# Test 1: Direct local execution uses local checkout
# ----------------------------------------------------
echo ""
echo "Test 1: Direct local execution uses local clone..."
OUTPUT_LOCAL=$(
  cd "${REPO_ROOT}"
  ./install.sh --dir "${TARGET_LOCAL_SRC}" --source 2>&1
)

if ! echo "$OUTPUT_LOCAL" | grep -q "Building cagy from local source tree"; then
  echo "FAILED: direct local execution should have used local source tree. Output:" >&2
  echo "$OUTPUT_LOCAL" >&2
  exit 1
fi
if [ ! -x "${TARGET_LOCAL_SRC}/cagy" ]; then
  echo "FAILED: binary not installed at ${TARGET_LOCAL_SRC}/cagy" >&2
  exit 1
fi
"${TARGET_LOCAL_SRC}/cagy" --help >/dev/null
echo "✓ Test 1 passed (direct local execution used local tree)"

# ----------------------------------------------------
# Test 2: Piped installer inside clone MUST NOT use local checkout
# ----------------------------------------------------
echo ""
echo "Test 2: Piped installer launched from inside clone fetches remote source..."
OUTPUT_PIPED=$(
  cd "${REPO_ROOT}"
  cat install.sh | CAGY_DOWNLOAD_BASE_URL="${BASE_URL}" CAGY_INSTALL_DIR="${TARGET_PIPED_INSIDE_CLONE}" bash 2>&1
)

if echo "$OUTPUT_PIPED" | grep -q "Building cagy from local source tree"; then
  echo "FAILED: piped execution inside clone must NEVER use local source tree! Output:" >&2
  echo "$OUTPUT_PIPED" >&2
  exit 1
fi
if ! echo "$OUTPUT_PIPED" | grep -q "Fetching cagy source"; then
  echo "FAILED: piped execution inside clone should have fetched remote source. Output:" >&2
  echo "$OUTPUT_PIPED" >&2
  exit 1
fi
if [ ! -x "${TARGET_PIPED_INSIDE_CLONE}/cagy" ]; then
  echo "FAILED: binary not installed via piped execution inside clone" >&2
  exit 1
fi
echo "✓ Test 2 passed (piped execution inside clone fetched remote source)"

# ----------------------------------------------------
# Test 3: Environment variable applied to receiving bash process in pipe
# ----------------------------------------------------
echo ""
echo "Test 3: CAGY_INSTALL_DIR passed to receiving bash process in pipe..."
(
  cd "${TEST_TMP}"
  cat "${REPO_ROOT}/install.sh" | CAGY_DOWNLOAD_BASE_URL="${BASE_URL}" CAGY_INSTALL_DIR="${TARGET_ENV_PIPE}" bash
)

if [ ! -x "${TARGET_ENV_PIPE}/cagy" ]; then
  echo "FAILED: binary not installed to TARGET_ENV_PIPE via receiving bash env" >&2
  exit 1
fi
echo "✓ Test 3 passed (receiving bash process inherited CAGY_INSTALL_DIR)"

# ----------------------------------------------------
# Test 4: Strict validation of CAGY_MODE
# ----------------------------------------------------
echo ""
echo "Test 4: Strict validation of CAGY_MODE (reject invalid values)..."
set +e
INVALID_MODE_OUTPUT=$(
  CAGY_MODE="unsupported_mode" "${REPO_ROOT}/install.sh" --dir "${TEST_TMP}" 2>&1
)
INVALID_MODE_STATUS=$?
set -e

if [ "$INVALID_MODE_STATUS" -eq 0 ]; then
  echo "FAILED: invalid CAGY_MODE should have exited with error, but succeeded" >&2
  exit 1
fi
if ! echo "$INVALID_MODE_OUTPUT" | grep -q -i "invalid install mode"; then
  echo "FAILED: expected 'Invalid install mode' message, got: $INVALID_MODE_OUTPUT" >&2
  exit 1
fi
echo "✓ Test 4 passed (invalid CAGY_MODE rejected)"

# ----------------------------------------------------
# Test 5: Collision-safe temporary file and atomic rename
# ----------------------------------------------------
echo ""
echo "Test 5: Collision-safe install and verification of no leftover temporary files..."
CAGY_DOWNLOAD_BASE_URL="${BASE_URL}" "${REPO_ROOT}/install.sh" \
  --dir "${TARGET_BINARY}" \
  --version "v${VERSION}" \
  --binary >/dev/null

if [ ! -x "${TARGET_BINARY}/cagy" ]; then
  echo "FAILED: binary not installed at ${TARGET_BINARY}/cagy" >&2
  exit 1
fi

# Ensure no temporary files (.cagy.install.* or *.tmp) remain in destination dir
LEFTOVER_COUNT=$(find "${TARGET_BINARY}" -type f -name ".cagy.install.*" -o -name "*.tmp*" | wc -l | tr -d ' ')
if [ "$LEFTOVER_COUNT" -ne 0 ]; then
  echo "FAILED: leftover temporary files found in ${TARGET_BINARY}" >&2
  find "${TARGET_BINARY}" -type f
  exit 1
fi
echo "✓ Test 5 passed (installed cleanly with no temporary file leftovers)"

# ----------------------------------------------------
# Test 6: Checksum matching handles ambiguous filenames correctly
# ----------------------------------------------------
echo ""
echo "Test 6: Checksum matching with ambiguous entries in checksums.txt..."
AMBIGUOUS_SERVER_DIR="${TEST_TMP}/ambiguous_server"
mkdir -p "${AMBIGUOUS_SERVER_DIR}"
cp "${SERVER_DIR}/${ARCHIVE_NAME}" "${AMBIGUOUS_SERVER_DIR}/"

# Place a prefix/similar filename before the real archive in checksums.txt
cat <<EOF > "${AMBIGUOUS_SERVER_DIR}/checksums.txt"
1111111111111111111111111111111111111111111111111111111111111111  prefix_${ARCHIVE_NAME}
2222222222222222222222222222222222222222222222222222222222222222  ${ARCHIVE_NAME}.sig
${REAL_SHA256}  ${ARCHIVE_NAME}
3333333333333333333333333333333333333333333333333333333333333333  suffix_${ARCHIVE_NAME}
EOF

AMBIGUOUS_PORT=$(python3 -c 'import socket; s = socket.socket(); s.bind(("", 0)); print(s.getsockname()[1]); s.close()')
python3 -m http.server "${AMBIGUOUS_PORT}" --directory "${AMBIGUOUS_SERVER_DIR}" >/dev/null 2>&1 &
AMB_PID=$!
sleep 1

CAGY_DOWNLOAD_BASE_URL="http://127.0.0.1:${AMBIGUOUS_PORT}" "${REPO_ROOT}/install.sh" \
  --dir "${TARGET_AMBIGUOUS}" \
  --version "v${VERSION}" \
  --binary >/dev/null
kill "${AMB_PID}" 2>/dev/null || true

if [ ! -x "${TARGET_AMBIGUOUS}/cagy" ]; then
  echo "FAILED: ambiguous checksum matching failed to select the exact archive entry" >&2
  exit 1
fi
echo "✓ Test 6 passed (exact archive name matched despite prefix/suffix entries)"

# ----------------------------------------------------
# Test 7: Reject malformed checksum (not 64 hex characters)
# ----------------------------------------------------
echo ""
echo "Test 7: Reject malformed checksum in checksums.txt..."
MALFORMED_SERVER_DIR="${TEST_TMP}/malformed_server"
mkdir -p "${MALFORMED_SERVER_DIR}"
cp "${SERVER_DIR}/${ARCHIVE_NAME}" "${MALFORMED_SERVER_DIR}/"

# Write invalid non-hex checksum
echo "invalid_hash_not_64_hex_chars  ${ARCHIVE_NAME}" > "${MALFORMED_SERVER_DIR}/checksums.txt"

MAL_PORT=$(python3 -c 'import socket; s = socket.socket(); s.bind(("", 0)); print(s.getsockname()[1]); s.close()')
python3 -m http.server "${MAL_PORT}" --directory "${MALFORMED_SERVER_DIR}" >/dev/null 2>&1 &
MAL_PID=$!
sleep 1

set +e
MAL_OUTPUT=$(
  CAGY_DOWNLOAD_BASE_URL="http://127.0.0.1:${MAL_PORT}" "${REPO_ROOT}/install.sh" \
    --dir "${TARGET_MALFORMED}" \
    --version "v${VERSION}" \
    --binary 2>&1
)
MAL_STATUS=$?
set -e
kill "${MAL_PID}" 2>/dev/null || true

if [ "$MAL_STATUS" -eq 0 ]; then
  echo "FAILED: installer should have failed on malformed checksum, but exited 0" >&2
  exit 1
fi
if ! echo "$MAL_OUTPUT" | grep -q -i "checksum"; then
  echo "FAILED: expected checksum error message, got: $MAL_OUTPUT" >&2
  exit 1
fi
echo "✓ Test 7 passed (malformed checksum correctly rejected)"

echo ""
echo "=== All 7 installer test cases passed successfully! ===
"
