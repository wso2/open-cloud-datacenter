#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DOWNLOAD_URL="${HARV_LOGS_RELEASE_URL:?HARV_LOGS_RELEASE_URL must contain the full release archive URL}"

# Ensure required commands are available
for command_name in curl tar; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "Error: '$command_name' is not installed."
    echo "macOS users can install missing tools via Homebrew: brew install $command_name"
    exit 1
  fi
done

TARGET_DIR="$PROJECT_DIR/dist"

if [[ -d "$TARGET_DIR" ]]; then
  echo "==> Harv Logs plugin is already extracted at:"
  echo "    $TARGET_DIR"
  echo "==> Skipping download."
  exit 0
fi

ARCHIVE_PATH="$PROJECT_DIR/harv-logs-app-release.tar.gz"

echo "==> Downloading Harv Logs plugin release"
curl --fail --location --show-error --retry 3 \
  --output "$ARCHIVE_PATH" "$DOWNLOAD_URL"

echo "==> Extracting release into $PROJECT_DIR"
tar -xzf "$ARCHIVE_PATH" -C "$PROJECT_DIR"

rm "$ARCHIVE_PATH"

echo "==> Successfully installed into $TARGET_DIR"
