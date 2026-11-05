# Release contract

A Sidera release fails closed. Passing unit tests alone is not authorization to
publish. The source identity, generated files, build inputs, artifacts,
licenses, and provenance below are all required.

## Source and tag

Release from an immutable commit with no staged, unstaged, or untracked source
files. Remove any ignored artifact that could be mistaken for a release input.
The release tag must resolve to that exact commit.
Run from the repository root:

```sh
set -euo pipefail
test -z "$(git status --porcelain=v1 --untracked-files=all)"
git diff --check
test "$(git rev-parse HEAD)" = "$(git rev-list -n 1 "${RELEASE_TAG:?set RELEASE_TAG}")"
test "$(go env GOVERSION)" = "go$(tr -d '\r\n' < .go-version)"
test "$(go env GOTOOLCHAIN)" = "local"
go mod verify
go mod tidy -diff
```

CI uses official Go 1.27.3 with `GOTOOLCHAIN=local` and
`GOFLAGS=-mod=readonly`. Release jobs must not silently download another
compiler, accept a dirty checkout, force an existing tag to another commit, or
turn an unavailable Git identity into `dirty=false`. A manual publishing
workflow must be dispatched from the release tag/commit itself; an alternate
ref that resolves to a different commit is rejected.

## Required gate

`.github/workflows/release-gate.yml` is both a required check and a reusable
publication dependency. It executes `script/ci-release-gate.sh` and requires:

- the portable full (`DEFAULT_BUILD_TAGS_OTHERS` on Linux/386 and
  `DEFAULT_BUILD_TAGS_WINDOWS` on supported Windows targets) and low-memory
  profiles on native Linux and Windows amd64 runners;
- CGO-disabled command builds for Linux and Windows amd64, arm64, and 386 using
  the platform-appropriate full tags and the low-memory tags;
- the literal low-memory command documented in `README.md`, including
  `release/LDFLAGS`;
- full/low validation of `release/config/config.json` and byte-identical schema
  surfaces;
- tracked schema and shell-completion regeneration with no Git diff;
- targeted race tests for lifecycle, DNS, VLESS Encryption, AnyTLS, traffic,
  API, and gRPC-lite seams;
- two from-scratch, separate-cache builds of both amd64 profiles that compare
  byte-for-byte; and
- a final clean-tree/diff check.

The arm64/386 rows are compile gates because hosted runners cannot execute
those binaries. Naive-enabled `DEFAULT_BUILD_TAGS` builds require pinned Cronet
native libraries and remain covered by the dedicated publication jobs. The
deterministic comparison covers CGO-disabled portable Go binaries, not signed
packages or platform archives. Native Windows privileged WinDivert
and TLS-spoof integration remains a separate required workflow check; the
release gate does not claim runtime coverage for Windows arm64/386.

## Artifact identity and provenance

Every publication job must:

1. use reviewed actions and release tools pinned to immutable commit SHA values;
2. set the release target commit explicitly to `GITHUB_SHA` and reject an
   existing remote tag that resolves elsewhere;
3. retain the exact Go version, GOOS/GOARCH, build tags, ldflags, source commit,
   and `go version -m` metadata for each binary;
4. produce a lexically sorted `SHA256SUMS` covering every uploaded file before
   provenance attestation;
5. attest that same content-addressed artifact set; and
6. include `LICENSE`, `NOTICE.md`, `UPSTREAM.md`, and `release/licenses/` in
   binary distributions.

Consumers verify a downloaded directory with:

```sh
sha256sum --check SHA256SUMS
```

A missing artifact, unknown source revision, dirty VCS marker, checksum
mismatch, generated-file drift, failed platform row, or unavailable telemetry
is a release failure—not a warning or an implied pass.

## Benchmark evidence

Performance evidence has a separate, stricter provenance boundary. The harness
and all compared source trees must be clean version-controlled commits. Retain
source archive hashes, complete environments and commands, configurations,
binary SHA-256 values, raw samples, logs, analysis code, and a bundle-level
`SHA256SUMS`. `SKIP_BUILD`-style reuse is allowed only with predeclared expected
binary hashes. Dirty, unknown, unmanifested, or unreconstructable runs remain
unattested engineering observations and cannot support release claims; see
`BENCHMARK.md`.
