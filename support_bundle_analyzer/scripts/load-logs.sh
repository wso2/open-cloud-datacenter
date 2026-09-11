#!/usr/bin/env bash
set -euo pipefail

ZIP_PATH="${1:?Usage: $0 <path-to-zip>}"
if [[ "$ZIP_PATH" != /* ]]; then
  ZIP_PATH="$PWD/$ZIP_PATH"
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BUNDLE_DIR="$PROJECT_DIR/bundle-logs"
cd "$PROJECT_DIR"

if [[ ! -f "$ZIP_PATH" ]]; then
  echo "Error: zip file not found: $ZIP_PATH"
  exit 1
fi

if ! command -v unzip >/dev/null 2>&1; then
  echo "Error: 'unzip' is not installed."
  exit 1
fi

echo "==> Clearing old logs in $BUNDLE_DIR"
rm -rf "$BUNDLE_DIR"
mkdir -p "$BUNDLE_DIR"

echo "==> Extracting $ZIP_PATH"
TMP_EXTRACT_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_EXTRACT_DIR"' EXIT
unzip -q -o "$ZIP_PATH" -d "$TMP_EXTRACT_DIR"

shopt -s nullglob dotglob
entries=("$TMP_EXTRACT_DIR"/*)

if [[ "${#entries[@]}" -eq 1 && -d "${entries[0]}" ]]; then
  echo "==> Zip contains a single wrapper folder; flattening it"
  mv "${entries[0]}"/* "$BUNDLE_DIR"/
else
  mv "$TMP_EXTRACT_DIR"/* "$BUNDLE_DIR"/
fi
shopt -u nullglob dotglob

echo "==> Extracting node zip files inside 'nodes' folders"
while IFS= read -r -d '' nodes_dir; do
  echo "    - Found nodes folder: $nodes_dir"

  shopt -s nullglob
  for node_zip in "$nodes_dir"/*.zip; do
    node_name="$(basename "$node_zip" .zip)"
    dest_dir="$nodes_dir/$node_name"

    echo "      - Extracting $(basename "$node_zip") -> $dest_dir"
    mkdir -p "$dest_dir"
    unzip -q -o "$node_zip" -d "$dest_dir"
    rm -f "$node_zip"

    shopt -s nullglob dotglob
    inner_entries=("$dest_dir"/*)
    if [[ "${#inner_entries[@]}" -eq 1 && -d "${inner_entries[0]}" ]]; then
      wrapper="${inner_entries[0]}"
      mv "$wrapper"/* "$dest_dir"/
      rmdir "$wrapper"
    fi
    shopt -u nullglob dotglob
  done
  shopt -u nullglob
done < <(find "$BUNDLE_DIR" -type d -name nodes -print0)

echo "==> Starting containers if they are not running"
docker compose up -d --build --wait

echo "==> Resetting Promtail's read position (full re-read of new logs)"
docker compose exec -T promtail rm -f /tmp/positions.yaml || true

echo "==> Restarting Promtail container"
docker compose restart promtail

echo
echo "Done. Logs from '$ZIP_PATH' are now being ingested."
echo "   Grafana: http://localhost:3000"
echo "   Promtail: http://localhost:9080"
