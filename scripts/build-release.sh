#!/bin/sh
set -eu

version=${1:-0.2.13}
dist=${2:-dist}

if [ -n "$(git status --porcelain=v1 --untracked-files=all)" ]; then
  echo "build-release: working tree must be clean" >&2
  exit 1
fi

commit=$(git rev-parse HEAD)
built_at=$(git show -s --format=%cI HEAD)

go run ./cmd/release-packager \
  --source . \
  --dist "$dist" \
  --version "$version" \
  --commit "$commit" \
  --built-at "$built_at"

for artifact in "$dist"/session-reviewer_"$version"_darwin_amd64.tar.gz "$dist"/session-reviewer_"$version"_darwin_arm64.tar.gz; do
  tar -tzf "$artifact" | grep -qx 'session-reviewer/schemas/review-job-status-v1.schema.json'
done
unzip -Z1 "$dist"/session-reviewer_"$version"_windows_amd64.zip | grep -qx 'session-reviewer/schemas/review-job-status-v1.schema.json'

echo "release candidate: $version $commit"
echo "artifacts: $dist"
