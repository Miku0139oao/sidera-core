#!/usr/bin/env bash

set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
go_version=$(tr -d '\r\n' < "$root/.go-version")

# Updating .go-version must update this reviewed checksum in the same commit.
case "$go_version" in
  1.26.5)
    archive_sha256=5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053
    ;;
  *)
    echo "no reviewed linux-amd64 Go checksum for $go_version" >&2
    exit 1
    ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
archive="$tmp/go${go_version}.linux-amd64.tar.gz"
curl --fail --location --proto '=https' --tlsv1.2 \
  --output "$archive" "https://go.dev/dl/go${go_version}.linux-amd64.tar.gz"
printf '%s  %s\n' "$archive_sha256" "$archive" | sha256sum --check --strict
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf "$archive"
test "$(/usr/local/go/bin/go env GOVERSION)" = "go${go_version}"
