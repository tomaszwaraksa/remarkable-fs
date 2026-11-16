# Shared helpers for the installer tests. Sourced by each *-tests.sh, not run on its own.
# shellcheck shell=bash disable=SC2034  # variables are used by the scripts that source this
# Needs: podman (fake tablet: Alpine + OpenSSH), unshare (private mount namespace), script.

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HERE="$SRC/test/installer"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/rmfs-installer-tests.XXXXXX")"
F="$WORK/home"          # the fake home folder the installer works in
SD="$WORK/svc-state"    # state of the systemctl stand-in
PW=tabletpw             # the fake tablet's root password
CLOSED=127.0.0.9        # nothing listens here: "tablet not reachable"
trap 'podman rm -f tablet tablet2 >/dev/null 2>&1; rm -rf "$WORK"' EXIT

# start_tablet NAME PORT: a fake tablet (sshd with root password login and a fake
# rm-ssh-over-wlan). Each start makes new host keys, like a tablet software update.
start_tablet() {
    podman rm -f "$1" >/dev/null 2>&1
    podman run -d --name "$1" -p "127.0.0.1:$2:22" docker.io/library/alpine:latest sh -c "
        apk add -q openssh >/dev/null && ssh-keygen -A >/dev/null && echo root:$PW | chpasswd 2>/dev/null &&
        printf '#!/bin/sh\necho on > /tmp/wifi-on\n' > /usr/bin/rm-ssh-over-wlan && chmod +x /usr/bin/rm-ssh-over-wlan &&
        sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin yes/; s/^#\?PasswordAuthentication.*/PasswordAuthentication yes/' /etc/ssh/sshd_config &&
        echo 'PerSourcePenalties no' >> /etc/ssh/sshd_config &&
        exec /usr/sbin/sshd -D -e" >/dev/null
    for _ in $(seq 60); do
        timeout 2 bash -c "exec 3<>/dev/tcp/127.0.0.1/$2; head -c 4 <&3" 2>/dev/null | grep -q SSH- && return
        sleep 1
    done
    echo "fake tablet $1 did not start"; exit 1
}

# fresh_home [accepted]: an empty fake home with a tablet key; "accepted" puts the key on the tablet.
fresh_home() {
    rm -rf "$F" "$SD"; mkdir -p "$F/.ssh" "$SD"
    ssh-keygen -q -t ed25519 -N "" -C "remarkable-fs@test" -f "$F/.ssh/remarkable_ed25519"
    if [ "${1:-}" = accepted ]; then
        podman exec -i tablet sh -c 'mkdir -p /root/.ssh; cat > /root/.ssh/authorized_keys' < "$F/.ssh/remarkable_ed25519.pub"
    fi
}

askpass() { printf '#!/bin/sh\necho %s\n' "$1" > "$WORK/askpass"; chmod +x "$WORK/askpass"; }

# inns PRE ARGS...: runs install.sh in a private mount namespace with the systemctl
# stand-in. PRE is a shell snippet run there first (e.g. a mount). Settings via env:
# WIFI, USB, PORT, SHIM_MODE (mount|crash|asleep).
inns() {
    local pre="$1"; shift
    : > "$SD/calls"
    setsid -w unshare -rm env HOME="$F" PATH="$HERE/shims:$PATH" SHIM_DIR="$SD" SHIM_MODE="${SHIM_MODE:-mount}" \
        SHIM_MOUNT="${SHIM_MOUNT:-$F/remarkable}" RMFS_TEST_MOUNT_TYPE=tmpfs RMFS_TEST_MOUNT_WAIT=4 \
        RMFS_TEST_WIFI_HOST="${WIFI:-127.0.0.1}" RMFS_TEST_USB_HOST="${USB:-127.0.0.1}" RMFS_TEST_PORT="${PORT:-2222}" \
        bash -c "$pre; \"$SRC/install.sh\" $*; echo \"(exit \$?)\"" </dev/null 2>&1 | tr -d '\r' | sed "s|$F|~|g"
}

# in_tty ARGS...: like inns, but with a terminal (script) and SSH_ASKPASS answering the
# password prompt; answers to the installer's questions come on stdin. EXTRA_PATH goes first in PATH.
in_tty() {
    : > "$SD/calls"
    setsid -w unshare -rm env HOME="$F" PATH="${EXTRA_PATH:+$EXTRA_PATH:}$HERE/shims:$PATH" SHIM_DIR="$SD" \
        SHIM_MODE="${SHIM_MODE:-mount}" SHIM_MOUNT="${SHIM_MOUNT:-$F/remarkable}" RMFS_TEST_MOUNT_TYPE=tmpfs RMFS_TEST_MOUNT_WAIT=4 \
        RMFS_TEST_WIFI_HOST="${WIFI:-127.0.0.1}" RMFS_TEST_USB_HOST="${USB:-127.0.0.1}" RMFS_TEST_PORT="${PORT:-2222}" \
        SSH_ASKPASS="$WORK/askpass" SSH_ASKPASS_REQUIRE=force DISPLAY=:0 \
        script -qec "\"$SRC/install.sh\" $*" /dev/null 2>&1 | tr -d '\r' | sed "s|$F|~|g"
}

# section START END: the lines of one installer step.
section() { sed -n "/$1/,/$2/p" | grep -v "$2"; }

calls() { grep -v 'show-environment\|is-active\|show -p' "$SD/calls" | sed "s|$F|~|g" | tr '\n' ';'; echo; }
