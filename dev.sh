#!/usr/bin/env bash
# Run any Go command inside the pinned toolchain image.
#
# Go is deliberately not installed on the host. Docker is the toolchain:
# `pi` edits files on the host, this wrapper compiles/tests them in a
# container with the same Go version the Dockerfile uses.
#
#   ./dev.sh go build ./...
#   ./dev.sh go test ./...
#   ./dev.sh go mod tidy
#
# Named cache volumes keep the module cache and build cache between runs,
# so only the first invocation pays for dependency downloads.
set -euo pipefail

IMAGE="${GO_IMAGE:-golang:1.24-alpine}"
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

exec docker run --rm \
  -v "$SRC:/src" -w /src \
  -v whatsapp-gomod:/go/pkg/mod \
  -v whatsapp-gobuild:/root/.cache/go-build \
  -e CGO_ENABLED=0 \
  -e GOFLAGS=-mod=mod \
  "$IMAGE" "$@"
