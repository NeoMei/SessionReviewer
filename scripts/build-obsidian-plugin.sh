#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "usage: $0 VERSION [DIST]" >&2
  exit 2
fi

version="$1"
dist_input="${2:-dist}"
script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
plugin_root="$repo_root/obsidian-plugin"

if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "version must be semantic without a v prefix" >&2
  exit 2
fi

mkdir -p "$dist_input"
dist="$(cd "$dist_input" && pwd)"

cd "$plugin_root"
if [[ "${SESSION_REVIEWER_PACKAGE_SKIP_CHECK:-}" != "1" ]]; then
  npm ci
  npm run check
else
  npm run build
fi

node -e '
const fs = require("node:fs");
const version = process.argv[1];
const pkg = JSON.parse(fs.readFileSync("package.json", "utf8"));
const manifest = JSON.parse(fs.readFileSync("manifest.json", "utf8"));
const versions = JSON.parse(fs.readFileSync("versions.json", "utf8"));
if (pkg.version !== version || manifest.version !== version || versions[version] !== manifest.minAppVersion) {
  throw new Error("package, manifest, and versions.json do not match requested version");
}
if (manifest.id !== "session-reviewer") throw new Error("unexpected plugin ID");
' "$version"

stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/session-reviewer"
for file in main.js manifest.json styles.css; do
  cp "$plugin_root/$file" "$stage/session-reviewer/$file"
done

epoch="${SOURCE_DATE_EPOCH:-315532800}"
if ! [[ "$epoch" =~ ^[0-9]+$ ]]; then
  echo "SOURCE_DATE_EPOCH must be a nonnegative integer" >&2
  exit 2
fi
if [[ "$epoch" -lt 315532800 ]]; then epoch=315532800; fi
if stamp="$(date -u -r "$epoch" +%Y%m%d%H%M.%S 2>/dev/null)"; then
  :
else
  stamp="$(date -u -d "@$epoch" +%Y%m%d%H%M.%S)"
fi
touch -t "$stamp" "$stage/session-reviewer/main.js" "$stage/session-reviewer/manifest.json" "$stage/session-reviewer/styles.css"

archive="$dist/session-reviewer-obsidian-$version.zip"
rm -f "$archive"
(cd "$stage" && zip -X -D -q "$archive" session-reviewer/main.js session-reviewer/manifest.json session-reviewer/styles.css)
for file in main.js manifest.json styles.css; do
  cp "$stage/session-reviewer/$file" "$dist/$file"
done
(
  cd "$dist"
  shasum -a 256 "$(basename "$archive")" main.js manifest.json styles.css | LC_ALL=C sort -k2
) > "$dist/SHA256SUMS"
echo "$archive"
