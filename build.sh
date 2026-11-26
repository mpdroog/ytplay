#!/bin/sh
# libvlc-go is cgo, and CGO_ENABLED=0 silently drops every file in the package,
# which shows up as "undefined: vlc.Init" rather than anything about cgo.
#
# The build runs inside podman (see Containerfile) so the host needs no vlc-dev
# or C toolchain; the binary it leaves here runs natively.
#
#   ./build.sh               go build
#   ./build.sh -o /tmp/x     extra arguments go to go build
#   ./build.sh test ./...    any other go subcommand: test, vet, run, ...
set -eu
cd "$(dirname "$0")"

podman build -q -t localhost/ytplay-dev -f Containerfile . >/dev/null

case "${1:-}" in
test | vet | run | mod | get | list | generate | env) ;;
*) set -- build "$@" ;;
esac

exec podman run --rm --userns=keep-id \
	-v "$PWD:/src:Z" -v ytplay-cache:/cache \
	localhost/ytplay-dev go "$@"
