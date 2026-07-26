#!/usr/bin/env bash

set -e -o pipefail

prepare_apk_root() {
  # apk mkpkg resolves owner/group names through --root/etc/{passwd,group}.
  APK_ROOT_DIR=$(mktemp -d)
  mkdir -p "$APK_ROOT_DIR/etc"
  cat > "$APK_ROOT_DIR/etc/passwd" <<EOF
root:x:$(id -u):$(id -g):root:/root:/sbin/nologin
EOF
  cat > "$APK_ROOT_DIR/etc/group" <<EOF
root:x:$(id -g):root
EOF
}

ARCHITECTURE="$1"
VERSION="$2"
BINARY_PATH="$3"
OUTPUT_PATH="$4"

if [ -z "$ARCHITECTURE" ] || [ -z "$VERSION" ] || [ -z "$BINARY_PATH" ] || [ -z "$OUTPUT_PATH" ]; then
  echo "Usage: $0 <architecture> <version> <binary_path> <output_path>"
  exit 1
fi

PROJECT=$(cd "$(dirname "$0")/.."; pwd)

# Convert version to APK format:
#   1.13.0-beta.8  -> 1.13.0_beta8-r0
#   1.13.0-rc.3    -> 1.13.0_rc3-r0
#   1.13.0         -> 1.13.0-r0
APK_VERSION=$(echo "$VERSION" | sed -E 's/-([a-z]+)\.([0-9]+)/_\1\2/')
APK_VERSION="${APK_VERSION}-r0"

ROOT_DIR=$(mktemp -d)
prepare_apk_root
trap 'rm -rf "$ROOT_DIR" "$APK_ROOT_DIR"' EXIT

# Binary
install -Dm755 "$BINARY_PATH" "$ROOT_DIR/usr/bin/sidera"

# Config files
install -Dm644 "$PROJECT/release/config/config.json" "$ROOT_DIR/etc/sidera/config.json"
install -Dm755 "$PROJECT/release/config/sidera.initd" "$ROOT_DIR/etc/init.d/sidera"
install -Dm644 "$PROJECT/release/config/sidera.confd" "$ROOT_DIR/etc/conf.d/sidera"

# Service files
install -Dm644 "$PROJECT/release/config/sidera.service" "$ROOT_DIR/usr/lib/systemd/system/sidera.service"
install -Dm644 "$PROJECT/release/config/sidera@.service" "$ROOT_DIR/usr/lib/systemd/system/sidera@.service"

# Completions
install -Dm644 "$PROJECT/release/completions/sidera.bash" "$ROOT_DIR/usr/share/bash-completion/completions/sidera.bash"
install -Dm644 "$PROJECT/release/completions/sidera.fish" "$ROOT_DIR/usr/share/fish/vendor_completions.d/sidera.fish"
install -Dm644 "$PROJECT/release/completions/sidera.zsh" "$ROOT_DIR/usr/share/zsh/site-functions/_sidera"

# License
install -Dm644 "$PROJECT/LICENSE" "$ROOT_DIR/usr/share/licenses/sidera/LICENSE"

# APK metadata
PACKAGES_DIR="$ROOT_DIR/lib/apk/packages"
mkdir -p "$PACKAGES_DIR"

# .conffiles
cat > "$PACKAGES_DIR/.conffiles" <<'EOF'
/etc/conf.d/sidera
/etc/init.d/sidera
/etc/sidera/config.json
EOF

# .conffiles_static (sha256 checksums)
while IFS= read -r conffile; do
  sha256=$(sha256sum "$ROOT_DIR$conffile" | cut -d' ' -f1)
  echo "$conffile $sha256"
done < "$PACKAGES_DIR/.conffiles" > "$PACKAGES_DIR/.conffiles_static"

# .list (all files, excluding lib/apk/packages/ metadata)
(cd "$ROOT_DIR" && find . -type f -o -type l) \
  | sed 's|^\./|/|' \
  | grep -v '^/lib/apk/packages/' \
  | sort > "$PACKAGES_DIR/.list"

# Build APK
apk --root "$APK_ROOT_DIR" mkpkg \
  --info "name:sidera" \
  --info "version:${APK_VERSION}" \
  --info "description:Single-data-plane universal proxy core." \
  --info "arch:${ARCHITECTURE}" \
  --info "license:GPL-3.0-or-later" \
  --info "origin:sidera" \
  --info "url:https://github.com/Miku0139oao/sidera-core" \
  --info "maintainer:Miku0139oao <62833794+Miku0139oao@users.noreply.github.com>" \
  --files "$ROOT_DIR" \
  --output "$OUTPUT_PATH"
