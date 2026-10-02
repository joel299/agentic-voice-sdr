#!/bin/sh
# Single local launch path: no shell evaluation of the protected .env.
set +x
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
if [ "$#" -ne 1 ]; then
  echo 'Usage: scripts/run-local-api.sh /absolute/path/to/api-binary' >&2
  exit 2
fi
case "$1" in /*) ;; *) echo 'API binary must be an absolute path' >&2; exit 2 ;; esac
if [ ! -x "$1" ]; then echo 'API binary is unavailable' >&2; exit 2; fi
exec "$1" --local-env .env
