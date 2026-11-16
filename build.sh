#!/usr/bin/env bash
# Builds static remarkable-fs release binaries into ./release
#
# Usage: ./build.sh [--skip-tests] [arch...]
#   arch: amd64, arm64 (default: both)
#
# Output:
#   release/remarkable-fs-linux-<arch>   static, stripped binaries
#   release/SHA256SUMS                   checksums of everything the installer uses
#   release/VERSION                      git version the binaries were built from
#   release/configuration.yaml           commented config template (used by the installer)
#
# Building only some architectures removes the binaries of the others, so release/
# never mixes builds of different versions.

set -euo pipefail

cd "$(dirname "$0")"

RELEASE_DIR="release"
PACKAGE="./cmd/remarkable-fs"
NAME="remarkable-fs"

run_tests=1
arches=()
for arg in "$@"; do
    case "$arg" in
        --skip-tests) run_tests=0 ;;
        -h|--help) awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "$0"; exit 0 ;;
        amd64|arm64) arches+=("$arg") ;;
        *) echo "Unknown argument: $arg" >&2; exit 1 ;;
    esac
done
[ ${#arches[@]} -eq 0 ] && arches=(amd64 arm64)

command -v go >/dev/null || { echo "Go is not installed" >&2; exit 1; }

version="$(git describe --tags --always --dirty 2>/dev/null || echo unknown)"
echo "Building $NAME $version"

if [ "$run_tests" -eq 1 ]; then
    echo "Running tests..."
    go test ./...
fi

mkdir -p "$RELEASE_DIR"

for arch in "${arches[@]}"; do
    out="$RELEASE_DIR/$NAME-linux-$arch"
    echo "Building $out"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
        go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$out" "$PACKAGE"
done

for old in "$RELEASE_DIR/$NAME"-linux-*; do
    [ -e "$old" ] || continue
    case " ${arches[*]} " in
        *" ${old##*-} "*) ;;
        *) echo "Removing $old (not part of this build)"; rm -f "$old" ;;
    esac
done

echo "$version" > "$RELEASE_DIR/VERSION"
cp internal/config/default_configuration.yaml "$RELEASE_DIR/configuration.yaml"
(cd "$RELEASE_DIR" && sha256sum "$NAME"-linux-* configuration.yaml scripts/*.sh > SHA256SUMS)

echo "Done:"
ls -lh "$RELEASE_DIR"
