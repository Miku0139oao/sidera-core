#!/usr/bin/env bash

set -euo pipefail

VERSION="1.26.5"
PATCH_COMMITS=(
  "70cc7249b8cf65e80163d792643de8bd90ad51a6"
  "d8f95fa6d13742a90c1810be7cd4b48cdb9cb061"
  "fc11427d8d43eb11947d11bf67781a3d9bfa89e8"
)
CURL_ARGS=(
  -fL
  --silent
  --show-error
)

if [[ -n "${GITHUB_TOKEN:-}" ]]; then
  CURL_ARGS+=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
fi

mkdir -p "$HOME/go"
cd "$HOME/go"
wget "https://dl.google.com/go/go${VERSION}.darwin-arm64.tar.gz"
tar -xzf "go${VERSION}.darwin-arm64.tar.gz"
#cp -a go go_bootstrap
mv go go_osx
cd go_osx

# These patch URLs apply to Go 1.26.x.
# See: https://github.com/MetaCubeX/go/commits/release-branch.go1.26/
# revert:
# d90a57ffe8: "cmd/link/internal/ld: unify OS/SDK versions for macOS linking"
# 33d3f603c1: "cmd/link/internal/ld: use 12.0.0 OS/SDK versions for macOS linking"
# 937368f84e: "crypto/x509: change how we retrieve chains on darwin"

for patch_commit in "${PATCH_COMMITS[@]}"; do
  curl "${CURL_ARGS[@]}" "https://github.com/MetaCubeX/go/commit/${patch_commit}.diff" | patch --verbose -p 1
done

# Rebuild is not needed: we build with CGO_ENABLED=1, so Apple's external
# linker handles LC_BUILD_VERSION via MACOSX_DEPLOYMENT_TARGET, and the
# stdlib (crypto/x509) is compiled from patched src automatically.
#cd src
#GOROOT_BOOTSTRAP="$HOME/go/go_bootstrap" ./make.bash
#cd ../..
#rm -rf go_bootstrap "go${VERSION}.darwin-arm64.tar.gz"
