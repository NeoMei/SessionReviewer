#!/bin/sh
set -eu

if [ "$#" -lt 2 ]; then
  echo "usage: apply-proposal.sh <proposal> <evidence> [flags]" >&2
  exit 2
fi

proposal=$1
evidence=$2
shift 2

exec session-reviewer apply --proposal "$proposal" --evidence "$evidence" "$@"
