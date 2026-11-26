# Build environment for ytplay, so the host needs no vlc-dev or C toolchain.
#
# Same Alpine release as the host: the binary runs natively there, against the
# host's libvlc (from the vlc package). Bump this when the host moves to a new
# Alpine release, or the binary may link against libraries the host lacks.
FROM docker.io/library/alpine:3.24

RUN apk add --no-cache go build-base pkgconf vlc-dev

ENV GOTOOLCHAIN=auto \
    GOPATH=/cache/go \
    GOCACHE=/cache/go-build \
    CGO_ENABLED=1 \
    HOME=/tmp

WORKDIR /src
