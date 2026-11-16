#!/usr/bin/env bash
# Installing without a build of our own: the installer downloads the latest release (here from
# a folder standing in for GitHub), checks it, installs it, and offers newer releases later.
# No tablet needed: the installer's functions are called directly.
# shellcheck disable=SC1091,SC2016  # test scripts: sourced lib, literal $(…) payloads
source "$(dirname "$0")/lib.sh"
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
GH="$WORK/github"            # stands in for https://github.com/.../releases/latest/download
EMPTY="$WORK/clone/release"  # release/ of a fresh clone: only the distro scripts
mkdir -p "$EMPTY/scripts"

# publish VERSION: a "release" whose program says that version.
publish() {
    rm -rf "$GH"; mkdir -p "$GH"
    printf '#!/bin/sh\n[ "$1" = --version ] && echo "remarkable-fs %s"\n' "$1" > "$GH/remarkable-fs-linux-$ARCH"
    echo "$1" > "$GH/VERSION"; echo "# settings $1" > "$GH/configuration.yaml"
    (cd "$GH" && sha256sum "remarkable-fs-linux-$ARCH" configuration.yaml > SHA256SUMS)
}
# installed VERSION|old|none: what's installed in the fake home ("old" can't tell its version).
installed() {
    rm -rf "$F"; mkdir -p "$F/.local/bin"
    case "$1" in
        none) ;;
        old) printf '#!/bin/sh\nexit 2\n' > "$F/.local/bin/remarkable-fs" ;;
        *) printf '#!/bin/sh\necho "remarkable-fs %s"\n' "$1" > "$F/.local/bin/remarkable-fs" ;;
    esac
    chmod +x "$F/.local/bin/remarkable-fs" 2>/dev/null || true
}
# inst URL COMMANDS: runs installer functions in the fake home, release/ without a build.
inst() {
    env HOME="$F" PATH="$HERE/shims:/usr/bin:/bin" SHIM_DIR="$SD" RMFS_TEST_DOWNLOAD_URL="$1" bash -c "
        source '$SRC/install.sh'; COLOR_RED= COLOR_GREEN= COLOR_YELLOW= COLOR_DIM= COLOR_BOLD= COLOR_RESET=
        RELEASE_DIR='$EMPTY'; BUILD_DIR='$EMPTY'; ASSUME_YES=1
        trap '[ -z \"\$DOWNLOAD_DIR\" ] || rm -rf \"\$DOWNLOAD_DIR\"' EXIT
        $2" 2>&1 | sed "s|$WORK|…|g"
    return "${PIPESTATUS[0]}"
}
mkdir -p "$SD"

echo "=== a. fresh install, no build of our own → downloads, checks, installs v0.2.0"
publish v0.2.0; installed none
inst "file://$GH" 'install_binary; echo "BUILD_DIR is a download: $([ "$BUILD_DIR" != "$RELEASE_DIR" ] && echo yes)"'
echo "installed program says: $("$F/.local/bin/remarkable-fs" --version)"
ls "$WORK"/tmp.* >/dev/null 2>&1 && echo "LEFT BEHIND: download folder" || echo "download folder removed"

echo; echo "=== b. the settings template comes from the download too"
inst "file://$GH" 'download_release; verify_release_file "$BUILD_DIR/configuration.yaml" && cat "$BUILD_DIR/configuration.yaml"'

echo; echo "=== c. a damaged download is refused, nothing installed"
publish v0.2.0; installed none; echo junk >> "$GH/remarkable-fs-linux-$ARCH"
inst "file://$GH" 'install_binary'; echo "(exit $?)"
[ -e "$F/.local/bin/remarkable-fs" ] && echo "INSTALLED ANYWAY" || echo "nothing installed"

echo; echo "=== d. GitHub not reachable → says so"
installed none
inst "file://$WORK/nowhere" 'install_binary'

echo; echo "=== e. --status without a build of our own shows the installed version, no download"
publish v0.2.0; installed v0.1.0
inst "file://$GH" 'status_program'
installed old
inst "file://$GH" 'status_program'

echo; echo "=== f. update offer from GitHub: v0.1.0 installed, v0.2.0 out → offered and installed"
publish v0.2.0; installed v0.1.0; mkdir -p "$F/remarkable"
inst "file://$GH" 'saved_mount_point() { echo "$HOME/remarkable"; }; wait_for_mount() { :; }; show_status() { :; }; offer_update'
echo "installed program says: $("$F/.local/bin/remarkable-fs" --version)"

echo; echo "=== g. no offer: same version / a developer build / offline"
publish v0.2.0
for have in v0.2.0 a502b82-dirty; do
    installed "$have"; printf '%s: ' "$have"; out=$(inst "file://$GH" 'offer_update'); echo "${out:-no offer}"
done
installed v0.1.0; printf 'offline: '; out=$(inst "file://$WORK/nowhere" 'offer_update'); echo "${out:-no offer}"

echo; echo "=== h. a program too old to tell its version → offered"
installed old
inst "file://$GH" 'confirm() { return 1; }; offer_update'

echo; echo "=== i. version comparison"
inst "" 'for p in "v0.10.0 v0.9.0" "v0.9.0 v0.10.0" "v1.0.0 v1.0.0" "v1.0.0 abc123" "v1.0.0 " "abc v0.1.0"; do
    set -- $p; is_newer_release "$1" "${2:-}" && r=newer || r=no; echo "  $1 vs ${2:-(unknown)}: $r"; done'
