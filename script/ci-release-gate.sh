#!/usr/bin/env bash
# Fail-closed release checks shared by GitHub Actions and `make ci_release_gate`.
set -Eeuo pipefail
IFS=$'\n\t'

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

fail() {
  printf 'release gate: %s\n' "$*" >&2
  exit 1
}

read_value() {
  local path=$1 value
  [[ -s "$path" ]] || fail "$path is missing or empty"
  value=$(tr -d '\r' < "$path")
  [[ -n "$value" && "$value" != *$'\n'* ]] || fail "$path must contain exactly one non-empty line"
  printf '%s' "$value"
}

profile_tags() {
  local profile=$1 target_os=$2 target_arch=$3
  case "$profile" in
    low)
      read_value release/LOW_MEMORY_BUILD_TAGS
      ;;
    full)
      if [[ "$target_os" == windows && "$target_arch" != 386 ]]; then
        read_value release/DEFAULT_BUILD_TAGS_WINDOWS
      else
        read_value release/DEFAULT_BUILD_TAGS_OTHERS
      fi
      ;;
    *)
      fail "unknown profile: $profile"
      ;;
  esac
}

assert_inputs() {
  local required actual tags ldflags ref license_tmp
  required=$(read_value .go-version)
  actual=$(go env GOVERSION)
  [[ "$actual" == "go$required" ]] || fail "Go $required is required; found $actual"

  for tags in \
    release/DEFAULT_BUILD_TAGS \
    release/DEFAULT_BUILD_TAGS_OTHERS \
    release/DEFAULT_BUILD_TAGS_WINDOWS \
    release/LOW_MEMORY_BUILD_TAGS; do
    read_value "$tags" >/dev/null
  done

  tags=$(read_value release/LOW_MEMORY_BUILD_TAGS)
  [[ ",$tags," == *,with_low_memory,* ]] || fail "low-memory tags omit with_low_memory"
  [[ ",$tags," == *,badlinkname,* ]] || fail "low-memory tags omit badlinkname"
  [[ "$tags" != *[[:space:]]* ]] || fail "low-memory tags contain whitespace"
  ldflags=$(read_value release/LDFLAGS)
  [[ "$ldflags" == *-checklinkname=0* ]] || fail "release/LDFLAGS omits -checklinkname=0"

  cmp -s common/ja3/LICENSE release/licenses/JA3-BSD-3-Clause.txt ||
    fail "redistributed JA3 license drifted"
  license_tmp=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/sidera-license.XXXXXX")
  tr -d '\r' < common/windivert/assets/LICENSE.txt | sed '${/^$/d;}' > "$license_tmp/source"
  tr -d '\r' < release/licenses/WinDivert-LICENSE.txt > "$license_tmp/release"
  cmp -s "$license_tmp/source" "$license_tmp/release" ||
    fail "redistributed WinDivert license drifted"
  rm -rf "$license_tmp"
  grep -q 'Mozilla Public License Version 2.0' release/licenses/MPL-2.0.txt ||
    fail "redistributed MPL-2.0 text is missing"

  while IFS= read -r ref; do
    [[ "$ref" =~ ^[0-9a-f]{40}$ ]] || fail "mutable GitHub Action ref: $ref"
  done < <(
    sed -nE 's/^[[:space:]-]*uses:[[:space:]]*[^@[:space:]]+@([^[:space:]#]+).*/\1/p' \
      .github/workflows/*.yml
  )
  if grep -R -n -- '@latest' Makefile .github/workflows; then
    fail "mutable @latest tool found in release/build entry points"
  fi
  command -v node >/dev/null 2>&1 || fail "node is required for release workflow structure checks"
  node script/check-release-workflows.mjs

  git diff --check
  go mod verify

  # `go mod tidy -diff` compares bytes, while Windows checkouts may present
  # tracked LF files as CRLF. Normalize only these two files temporarily so a
  # line-ending conversion cannot masquerade as dependency drift.
  local tidy_tmp tidy_status=0
  tidy_tmp=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/sidera-tidy.XXXXXX")
  cp go.mod go.sum "$tidy_tmp/"
  tr -d '\r' < "$tidy_tmp/go.mod" > go.mod
  tr -d '\r' < "$tidy_tmp/go.sum" > go.sum
  go mod tidy -diff || tidy_status=$?
  cp "$tidy_tmp/go.mod" "$tidy_tmp/go.sum" .
  rm -rf "$tidy_tmp"
  [[ $tidy_status -eq 0 ]] || fail "go.mod or go.sum is not tidy"
}

compile_target() {
  local target_os=$1 target_arch=$2 profile=$3 tags ldflags suffix output
  case "$target_os/$target_arch" in
    linux/amd64|linux/arm64|linux/386|windows/amd64|windows/arm64|windows/386) ;;
    *) fail "unsupported compile gate target: $target_os/$target_arch" ;;
  esac
  tags=$(profile_tags "$profile" "$target_os" "$target_arch")
  ldflags=$(read_value release/LDFLAGS)
  suffix=
  [[ "$target_os" == windows ]] && suffix=.exe
  output="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/sidera-${target_os}-${target_arch}-${profile}${suffix}"
  rm -f "$output"
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
    go build -mod=readonly -trimpath -buildvcs=true \
      -tags "$tags" -ldflags "$ldflags" -o "$output" ./cmd/sidera
  [[ -s "$output" ]] || fail "empty build output: $output"
  rm -f "$output"
}

native_compile() {
  local profile=$1 host_os host_arch tags ldflags suffix output
  host_os=$(go env GOOS)
  host_arch=$(go env GOARCH)
  [[ "$host_os" == linux || "$host_os" == windows ]] || fail "native gate requires Linux or Windows"
  [[ "$host_arch" == amd64 ]] || fail "native gate requires amd64"
  tags=$(profile_tags "$profile" "$host_os" "$host_arch")
  ldflags=$(read_value release/LDFLAGS)

  CGO_ENABLED=0 go test -mod=readonly -run '^$' -count=1 \
    -tags "$tags" -ldflags "$ldflags" ./...

  suffix=
  [[ "$host_os" == windows ]] && suffix=.exe
  output="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/sidera-native-${host_os}-${profile}${suffix}"
  rm -f "$output"
  CGO_ENABLED=0 go build -mod=readonly -trimpath -buildvcs=true \
    -tags "$tags" -ldflags "$ldflags" -o "$output" ./cmd/sidera
  [[ -s "$output" ]] || fail "empty native build output: $output"
  rm -f "$output"
}

literal_low_memory_build() {
  local existed=0
  [[ ! -e dist/sidera-low-memory ]] || existed=1
  [[ $existed -eq 0 ]] || fail "dist/sidera-low-memory already exists"
  mkdir -p dist
  GOTOOLCHAIN=go1.27.3 CGO_ENABLED=0 go build -trimpath \
    -tags "$(cat release/LOW_MEMORY_BUILD_TAGS)" \
    -ldflags "$(cat release/LDFLAGS)" \
    -o dist/sidera-low-memory ./cmd/sidera
  [[ -s dist/sidera-low-memory ]] || fail "literal low-memory build produced no binary"
  rm -f dist/sidera-low-memory
  rmdir dist 2>/dev/null || true
}

config_and_schema_profiles() {
  local tmp full_tags low_tags ldflags
  [[ "$(go env GOOS)" == linux && "$(go env GOARCH)" == amd64 ]] ||
    fail "configuration gate requires linux/amd64"
  tmp=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/sidera-config.XXXXXX")
  trap 'rm -rf "$tmp"' RETURN
  full_tags=$(profile_tags full linux amd64)
  low_tags=$(profile_tags low linux amd64)
  ldflags=$(read_value release/LDFLAGS)

  CGO_ENABLED=0 go run -mod=readonly -tags "$full_tags" -ldflags "$ldflags" \
    ./cmd/sidera check -c release/config/config.json
  CGO_ENABLED=0 go run -mod=readonly -tags "$low_tags" -ldflags "$ldflags" \
    ./cmd/sidera check -c release/config/config.json
  CGO_ENABLED=0 go run -mod=readonly -tags "$full_tags" -ldflags "$ldflags" \
    ./cmd/sidera schema -o "$tmp/full.json"
  CGO_ENABLED=0 go run -mod=readonly -tags "$low_tags" -ldflags "$ldflags" \
    ./cmd/sidera schema -o "$tmp/low.json"
  cmp "$tmp/full.json" "$tmp/low.json" || fail "full/low schema surfaces differ"
  rm -rf "$tmp"
  trap - RETURN
}

generated_drift() {
  local tmp tags ldflags status=0 file
  [[ "$(go env GOOS)" == linux && "$(go env GOARCH)" == amd64 ]] ||
    fail "generated-file gate requires linux/amd64"
  git diff --quiet -- docs/schema.json release/completions ||
    fail "generated files were already modified before the drift gate"

  tmp=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/sidera-generated.XXXXXX")
  for file in docs/schema.json release/completions/sidera.bash \
    release/completions/sidera.fish release/completions/sidera.zsh; do
    mkdir -p "$tmp/$(dirname "$file")"
    cp "$file" "$tmp/$file"
  done
  tags=$(profile_tags full linux amd64)
  ldflags=$(read_value release/LDFLAGS)

  CGO_ENABLED=0 go run -mod=readonly -tags "$tags" -ldflags "$ldflags" \
    ./cmd/sidera schema -o docs/schema.json
  CGO_ENABLED=0 go run -mod=readonly \
    -tags "$tags,generate,generate_completions" -ldflags "$ldflags" ./cmd/sidera

  git diff --exit-code -- docs/schema.json release/completions || status=$?
  for file in docs/schema.json release/completions/sidera.bash \
    release/completions/sidera.fish release/completions/sidera.zsh; do
    cp "$tmp/$file" "$file"
  done
  rm -rf "$tmp"
  [[ $status -eq 0 ]] || fail "schema or completion output drifted"
  git diff --quiet -- docs/schema.json release/completions ||
    fail "generated-file restoration did not return to the original tree"
}

targeted_race() {
  local tags ldflags
  [[ "$(go env GOOS)" == linux && "$(go env GOARCH)" == amd64 ]] ||
    fail "race gate requires linux/amd64"
  tags=$(profile_tags full linux amd64)
  ldflags=$(read_value release/LDFLAGS)
  CGO_ENABLED=1 go test -mod=readonly -p=1 -count=1 -race \
    -tags "$tags" -ldflags "$ldflags" \
    . \
    ./adapter/inbound \
    ./adapter/service \
    ./cmd/sidera \
    ./common/trafficcontrol \
    ./dns/transport \
    ./protocol/anytls \
    ./protocol/vless \
    ./protocol/vless/xrayencryption \
    ./service/api \
    ./transport/v2raygrpclite
}

reproducible_builds() {
  local base profile tags ldflags repro_ldflags first second revision
  [[ "$(go env GOOS)" == linux && "$(go env GOARCH)" == amd64 ]] ||
    fail "reproducibility gate requires linux/amd64"
  base="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/sidera-release-gate"
  rm -rf "$base"
  mkdir -p "$base/out"
  ldflags=$(read_value release/LDFLAGS)
  repro_ldflags="-X github.com/Miku0139oao/sidera-core/constant.Version=release-gate $ldflags -s -w -buildid="
  export SOURCE_DATE_EPOCH
  SOURCE_DATE_EPOCH=$(git show -s --format=%ct HEAD)
  revision=$(git rev-parse HEAD)

  for profile in full low; do
    tags=$(profile_tags "$profile" linux amd64)
    first="$base/out/sidera-linux-amd64-${profile}-a"
    second="$base/out/sidera-linux-amd64-${profile}-b"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOCACHE="$base/cache-${profile}-a" \
      go build -mod=readonly -trimpath -buildvcs=true -tags "$tags" \
        -ldflags "$repro_ldflags" -o "$first" ./cmd/sidera
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOCACHE="$base/cache-${profile}-b" \
      go build -mod=readonly -trimpath -buildvcs=true -tags "$tags" \
        -ldflags "$repro_ldflags" -o "$second" ./cmd/sidera
    cmp "$first" "$second" || fail "$profile binaries are not deterministic"
    go version -m "$first" > "$base/out/sidera-linux-amd64-${profile}.buildinfo"
    grep -Fq $'build\tvcs=git' "$base/out/sidera-linux-amd64-${profile}.buildinfo" ||
      fail "$profile binary omits VCS provenance"
    grep -Fq $'build\tvcs.revision='"$revision" "$base/out/sidera-linux-amd64-${profile}.buildinfo" ||
      fail "$profile binary has the wrong VCS revision"
    grep -Fq $'build\tvcs.modified=false' "$base/out/sidera-linux-amd64-${profile}.buildinfo" ||
      fail "$profile binary was built from a dirty source identity"
  done

  (
    cd "$base/out"
    LC_ALL=C sha256sum sidera-linux-amd64-full-a sidera-linux-amd64-low-a > ../SHA256SUMS
  )
  cat "$base/SHA256SUMS"
}

clean_tree() {
  git diff --check
  git diff --exit-code
  [[ -z "$(git status --porcelain=v1 --untracked-files=all)" ]] || {
    git status --short --untracked-files=all >&2
    fail "working tree is not clean"
  }
}

usage() {
  cat >&2 <<'EOF'
usage: script/ci-release-gate.sh COMMAND [ARGS]
  inputs
  literal-low-memory-build
  config
  generated
  native full|low
  compile linux|windows amd64|arm64|386 full|low
  race
  reproducible
  clean-tree
  all
EOF
  exit 2
}

command=${1:-}
case "$command" in
  inputs) assert_inputs ;;
  literal-low-memory-build) literal_low_memory_build ;;
  config) config_and_schema_profiles ;;
  generated) generated_drift ;;
  native) [[ $# -eq 2 ]] || usage; native_compile "$2" ;;
  compile) [[ $# -eq 4 ]] || usage; compile_target "$2" "$3" "$4" ;;
  race) targeted_race ;;
  reproducible) reproducible_builds ;;
  clean-tree) clean_tree ;;
  all)
    [[ $# -eq 1 ]] || usage
    assert_inputs
    literal_low_memory_build
    config_and_schema_profiles
    generated_drift
    native_compile full
    native_compile low
    for target in linux/amd64 linux/arm64 linux/386 windows/amd64 windows/arm64 windows/386; do
      for profile in full low; do
        compile_target "${target%/*}" "${target#*/}" "$profile"
      done
    done
    targeted_race
    reproducible_builds
    clean_tree
    ;;
  *) usage ;;
esac
