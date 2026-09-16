#!/bin/sh
set -eu
# Pass the exact executable path, then the reviewed uninstall command flags.
cli_path=${1:?Usage: uninstall.sh /absolute/path/eigenflux [uninstall flags]}
shift
case "$cli_path" in /*) ;; *) printf '%s\n' 'An absolute CLI path is required.' >&2; exit 2 ;; esac
exec "$cli_path" uninstall "$@"
