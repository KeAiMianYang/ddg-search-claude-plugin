#!/bin/sh
# Launcher script for DuckDuckGo MCP Server
# Detects platform and executes the appropriate binary

set -e

# Get the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Detect platform
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

# Normalize architecture names to match npm conventions
case "$ARCH" in
    x86_64)
        ARCH="x64"
        ;;
    aarch64)
        ARCH="arm64"
        ;;
    # arm64 and x64 already match
esac

# Construct binary path
BINARY_PATH="${SCRIPT_DIR}/packages/ddg-search-${OS}-${ARCH}/ddg-search"

# Check if binary exists
if [ ! -f "$BINARY_PATH" ]; then
    echo "Error: Binary not found for platform ${OS}-${ARCH}" >&2
    echo "Expected: ${BINARY_PATH}" >&2
    echo "" >&2
    echo "Supported platforms:" >&2
    echo "  - macOS Intel: darwin-x64" >&2
    echo "  - macOS Apple Silicon: darwin-arm64" >&2
    echo "  - Linux x64: linux-x64" >&2
    echo "  - Linux ARM64: linux-arm64" >&2
    echo "  (Windows users: use WSL or Git Bash with linux-x64 binary)" >&2
    exit 1
fi

# Execute the binary, replacing this process
exec "$BINARY_PATH" "$@"
