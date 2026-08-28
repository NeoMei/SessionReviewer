#!/bin/sh
set -eu

version=${1:-0.2.3}
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

echo "release candidate: $version $commit"
echo "artifacts: $dist"
