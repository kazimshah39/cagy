#!/usr/bin/env bash
# End-to-end installer test suite
# Validates source builds, piped runs inside clone, env passing, strict modes, collision safety, and checksum rules on darwin/arm64

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

echo "=== Testing herdr-tandem install.sh ==="

TEST_TMP="$(mktemp -d 2>/dev/null || mktemp -d -t 'herdr-tandem-test-installer')"
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

# herdr-tandem is intentionally tested only on Apple Silicon macOS.
OS_RAW="$(uname -s)"
ARCH_RAW="$(uname -m)"
if [ "$OS_RAW" != "Darwin" ] || { [ "$ARCH_RAW" != "arm64" ] && [ "$ARCH_RAW" != "aarch64" ]; }; then
  echo "SKIP: installer tests require Apple Silicon macOS (darwin/arm64); detected ${OS_RAW}/${ARCH_RAW}."
  exit 0
fi
OS="darwin"
ARCH="arm64"

# ----------------------------------------------------
# Prepare Mock HTTP Server with Source & Binary Assets
# ----------------------------------------------------
VERSION="0.1.0"
ARCHIVE_NAME="herdr-tandem_${VERSION}_${OS}_${ARCH}.tar.gz"

# 1. Mock remote source tarball (archive.tar.gz)
SRC_STAGE="${TEST_TMP}/src_stage/herdr-tandem-main"
mkdir -p "${SRC_STAGE}/cmd/herdr-tandem" "${SRC_STAGE}/internal"
cp -r "${REPO_ROOT}/go.mod" "${SRC_STAGE}/"
[ -f "${REPO_ROOT}/go.sum" ] && cp -r "${REPO_ROOT}/go.sum" "${SRC_STAGE}/"
cp -r "${REPO_ROOT}/cmd" "${SRC_STAGE}/"
cp -r "${REPO_ROOT}/internal" "${SRC_STAGE}/"
tar -czf "${SERVER_DIR}/archive.tar.gz" -C "${TEST_TMP}/src_stage" herdr-tandem-main

# 2. Mock prebuilt release binary and checksums
BIN_STAGE="${TEST_TMP}/bin_stage"
mkdir -p "${BIN_STAGE}"
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -C "${REPO_ROOT}" -trimpath -ldflags "-s -w" -o "${BIN_STAGE}/herdr-tandem" ./cmd/herdr-tandem
(cd "${BIN_STAGE}" && ln -sf herdr-tandem hdt)
tar -czf "${SERVER_DIR}/${ARCHIVE_NAME}" -C "${BIN_STAGE}" herdr-tandem hdt

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

if ! echo "$OUTPUT_LOCAL" | grep -q "Building herdr-tandem from local source tree"; then
  echo "FAILED: direct local execution should have used local source tree. Output:" >&2
  echo "$OUTPUT_LOCAL" >&2
  exit 1
fi
if [ ! -x "${TARGET_LOCAL_SRC}/herdr-tandem" ]; then
  echo "FAILED: binary not installed at ${TARGET_LOCAL_SRC}/herdr-tandem" >&2
  exit 1
fi
if [ ! -L "${TARGET_LOCAL_SRC}/hdt" ] || [ ! -x "${TARGET_LOCAL_SRC}/hdt" ]; then
  echo "FAILED: hdt alias symlink not installed at ${TARGET_LOCAL_SRC}/hdt" >&2
  exit 1
fi
"${TARGET_LOCAL_SRC}/herdr-tandem" --help >/dev/null
"${TARGET_LOCAL_SRC}/hdt" --help >/dev/null
echo "✓ Test 1 passed (direct local execution used local tree)"

# ----------------------------------------------------
# Test 2: Piped installer inside clone MUST NOT use local checkout
# ----------------------------------------------------
echo ""
echo "Test 2: Piped installer launched from inside clone fetches remote source..."
OUTPUT_PIPED=$(
  cd "${REPO_ROOT}"
  cat install.sh | HERDR_TANDEM_DOWNLOAD_BASE_URL="${BASE_URL}" HERDR_TANDEM_INSTALL_DIR="${TARGET_PIPED_INSIDE_CLONE}" bash 2>&1
)

if echo "$OUTPUT_PIPED" | grep -q "Building herdr-tandem from local source tree"; then
  echo "FAILED: piped execution inside clone must NEVER use local source tree! Output:" >&2
  echo "$OUTPUT_PIPED" >&2
  exit 1
fi
if ! echo "$OUTPUT_PIPED" | grep -q "Fetching herdr-tandem source"; then
  echo "FAILED: piped execution inside clone should have fetched remote source. Output:" >&2
  echo "$OUTPUT_PIPED" >&2
  exit 1
fi
if [ ! -x "${TARGET_PIPED_INSIDE_CLONE}/herdr-tandem" ]; then
  echo "FAILED: binary not installed via piped execution inside clone" >&2
  exit 1
fi
if [ ! -L "${TARGET_PIPED_INSIDE_CLONE}/hdt" ] || [ ! -x "${TARGET_PIPED_INSIDE_CLONE}/hdt" ]; then
  echo "FAILED: hdt alias symlink not installed via piped execution inside clone" >&2
  exit 1
fi
"${TARGET_PIPED_INSIDE_CLONE}/hdt" --help >/dev/null
echo "✓ Test 2 passed (piped execution inside clone fetched remote source)"

# ----------------------------------------------------
# Test 3: Environment variable applied to receiving bash process in pipe
# ----------------------------------------------------
echo ""
echo "Test 3: HERDR_TANDEM_INSTALL_DIR passed to receiving bash process in pipe..."
(
  cd "${TEST_TMP}"
  cat "${REPO_ROOT}/install.sh" | HERDR_TANDEM_DOWNLOAD_BASE_URL="${BASE_URL}" HERDR_TANDEM_INSTALL_DIR="${TARGET_ENV_PIPE}" bash
)

if [ ! -x "${TARGET_ENV_PIPE}/herdr-tandem" ]; then
  echo "FAILED: binary not installed to TARGET_ENV_PIPE via receiving bash env" >&2
  exit 1
fi
if [ ! -L "${TARGET_ENV_PIPE}/hdt" ] || [ ! -x "${TARGET_ENV_PIPE}/hdt" ]; then
  echo "FAILED: hdt alias symlink not installed to TARGET_ENV_PIPE" >&2
  exit 1
fi
"${TARGET_ENV_PIPE}/hdt" --help >/dev/null
echo "✓ Test 3 passed (receiving bash process inherited HERDR_TANDEM_INSTALL_DIR)"

# ----------------------------------------------------
# Test 4: Strict validation of HERDR_TANDEM_MODE
# ----------------------------------------------------
echo ""
echo "Test 4: Strict validation of HERDR_TANDEM_MODE (reject invalid values)..."
set +e
INVALID_MODE_OUTPUT=$(
  HERDR_TANDEM_MODE="unsupported_mode" "${REPO_ROOT}/install.sh" --dir "${TEST_TMP}" 2>&1
)
INVALID_MODE_STATUS=$?
set -e

if [ "$INVALID_MODE_STATUS" -eq 0 ]; then
  echo "FAILED: invalid HERDR_TANDEM_MODE should have exited with error, but succeeded" >&2
  exit 1
fi
if ! echo "$INVALID_MODE_OUTPUT" | grep -q -i "invalid install mode"; then
  echo "FAILED: expected 'Invalid install mode' message, got: $INVALID_MODE_OUTPUT" >&2
  exit 1
fi
echo "✓ Test 4 passed (invalid HERDR_TANDEM_MODE rejected)"

# ----------------------------------------------------
# Test 5: Collision-safe temporary file and atomic rename
# ----------------------------------------------------
echo ""
echo "Test 5: Collision-safe install and verification of no leftover temporary files..."
HERDR_TANDEM_DOWNLOAD_BASE_URL="${BASE_URL}" "${REPO_ROOT}/install.sh" \
  --dir "${TARGET_BINARY}" \
  --version "v${VERSION}" \
  --binary >/dev/null

if [ ! -x "${TARGET_BINARY}/herdr-tandem" ]; then
  echo "FAILED: binary not installed at ${TARGET_BINARY}/herdr-tandem" >&2
  exit 1
fi
if [ ! -L "${TARGET_BINARY}/hdt" ] || [ ! -x "${TARGET_BINARY}/hdt" ]; then
  echo "FAILED: hdt alias symlink not installed at ${TARGET_BINARY}/hdt" >&2
  exit 1
fi
"${TARGET_BINARY}/hdt" --help >/dev/null

# Test idempotent re-installation into existing destination directory
HERDR_TANDEM_DOWNLOAD_BASE_URL="${BASE_URL}" "${REPO_ROOT}/install.sh" \
  --dir "${TARGET_BINARY}" \
  --version "v${VERSION}" \
  --binary >/dev/null

if [ ! -L "${TARGET_BINARY}/hdt" ] || [ ! -x "${TARGET_BINARY}/hdt" ]; then
  echo "FAILED: hdt alias symlink broken after re-installation" >&2
  exit 1
fi

# Ensure no temporary files (.herdr-tandem.install.* or *.tmp*) remain in destination dir
LEFTOVER_COUNT=$(find "${TARGET_BINARY}" -type f -name ".herdr-tandem.install.*" -o -name "*.tmp*" | wc -l | tr -d ' ')
if [ "$LEFTOVER_COUNT" -ne 0 ]; then
  echo "FAILED: leftover temporary files found in ${TARGET_BINARY}" >&2
  find "${TARGET_BINARY}" -type f
  exit 1
fi
echo "✓ Test 5 passed (installed cleanly with hdt alias and no temporary file leftovers)"

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

HERDR_TANDEM_DOWNLOAD_BASE_URL="http://127.0.0.1:${AMBIGUOUS_PORT}" "${REPO_ROOT}/install.sh" \
  --dir "${TARGET_AMBIGUOUS}" \
  --version "v${VERSION}" \
  --binary >/dev/null
kill "${AMB_PID}" 2>/dev/null || true

if [ ! -x "${TARGET_AMBIGUOUS}/herdr-tandem" ]; then
  echo "FAILED: ambiguous checksum matching failed to select the exact archive entry" >&2
  exit 1
fi
if [ ! -L "${TARGET_AMBIGUOUS}/hdt" ] || [ ! -x "${TARGET_AMBIGUOUS}/hdt" ]; then
  echo "FAILED: hdt alias symlink not installed at ${TARGET_AMBIGUOUS}/hdt" >&2
  exit 1
fi
"${TARGET_AMBIGUOUS}/hdt" --help >/dev/null
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
  HERDR_TANDEM_DOWNLOAD_BASE_URL="http://127.0.0.1:${MAL_PORT}" "${REPO_ROOT}/install.sh" \
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
