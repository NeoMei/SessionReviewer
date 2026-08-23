#!/bin/sh
set -eu

if [ "$#" -lt 2 ]; then
  echo "usage: prepare-workflow.sh <review|checkpoint> <output> [flags]" >&2
  exit 2
fi

mode=$1
output=$2
shift 2

case "$mode" in
  review|checkpoint) ;;
  *)
    echo "prepare-workflow.sh: mode must be review or checkpoint" >&2
    exit 2
    ;;
esac

exec session-reviewer prepare "$mode" --output "$output" "$@"
