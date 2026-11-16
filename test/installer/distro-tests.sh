#!/usr/bin/env bash
# Runs install.sh's preflight (dry, as a normal user) and the real distro script (as root, for
# shellcheck disable=SC1091,SC2016,SC2012  # test scripts: sourced lib, literal $(…) payloads, ls for display
# fuse and ssh) in containers of the supported distributions. Logs go to $OUT (default: a temp folder).
SRC="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="${OUT:-$(mktemp -d "${TMPDIR:-/tmp}/rmfs-distro-tests.XXXXXX")}"
test_image() {
    local name="$1" image="$2" prep="${3:-true}" log="$OUT/$1.log"
    {
        echo "=== $name: preflight as normal user (--yes --dry)"
        podman run --rm --user 1000:1000 -e HOME=/tmp -v "$SRC:/src:ro,Z" "$image" bash /src/install.sh --yes --dry 2>&1
        echo "exit $?"
        echo "=== $name: real distro script as root"
        podman run --rm -v "$SRC:/src:ro,Z" "$image" sh -c "$prep >/dev/null 2>&1"'
            printf "#!/bin/sh\nexec \"\$@\"\n" > /usr/local/bin/sudo && chmod +x /usr/local/bin/sudo
            . /etc/os-release
            for id in $ID $ID_LIKE; do [ -f /src/release/scripts/$id.sh ] && { echo "using $id.sh"; bash /src/release/scripts/$id.sh fuse ssh >/tmp/install.log 2>&1 || { tail -5 /tmp/install.log; echo INSTALL FAILED; }; break; }; done
            for c in fusermount3 ssh ssh-keygen ssh-copy-id ssh-keyscan; do command -v $c >/dev/null && echo "found $c" || echo "MISSING $c"; done' 2>&1
    } > "$log" 2>&1
}
test_image debian  docker.io/library/debian:12 &
test_image ubuntu  docker.io/library/ubuntu:24.04 &
test_image fedora  registry.fedoraproject.org/fedora:41 &
test_image rocky   docker.io/rockylinux/rockylinux:9 &
# The Arch image ships without package lists; a real Arch system has them.
test_image arch    docker.io/library/archlinux:latest 'pacman -Syu --noconfirm' &
test_image suse    registry.opensuse.org/opensuse/tumbleweed:latest &
wait
echo "Logs in $OUT"
