#!/usr/bin/env bash
# Installs remarkable-fs: shows your reMarkable tablet as a folder on this computer.
#
# Usage: ./install.sh [options]
#
#   (no options)     set up remarkable-fs, or show its status if it is already set up
#   --status         show whether remarkable-fs is running and the tablet is connected
#   --reconfigure    go through the whole setup again (e.g. to change the folder)
#   --uninstall      remove remarkable-fs, keep your settings and tablet key
#   --purge          remove remarkable-fs together with settings, cache and tablet key
#   --yes            accept all suggested answers without asking
#   --dry            dry run: show what would be done, change nothing
#   -h, --help       show this help

set -euo pipefail

# ---------------------------------------------------------------------------
# Locations
# ---------------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE_DIR="$SCRIPT_DIR/release"   # distro scripts, and a developer's own build (./build.sh)
BUILD_DIR="$RELEASE_DIR"            # the program to install: release/, or a downloaded release
DOWNLOAD_DIR=""                     # the temporary folder of a downloaded release, removed at the end

APP_NAME="remarkable-fs"
BIN_PATH="$HOME/.local/bin/$APP_NAME"
UNIT_NAME="$APP_NAME.service"
UNIT_PATH="$HOME/.config/systemd/user/$UNIT_NAME"
CONFIG_DIR="$HOME/.config/$APP_NAME"
CONFIG_FILE="$CONFIG_DIR/configuration.yaml"
KNOWN_HOSTS="$CONFIG_DIR/known_hosts"   # the tablet's identity, shared with the daemon
HOST_KEY_ALIAS="remarkable"              # one identity for all of the tablet's addresses
CACHE_DIR="$HOME/.cache/$APP_NAME"
LOCK_FILE="$CACHE_DIR/lock"              # held by the running copy; names its PID and folder
KEY_PATH="$HOME/.ssh/remarkable_ed25519"
# shellcheck disable=SC2088  # shown to the user as typed; expanded when used
DEFAULT_MOUNT_POINT="~/remarkable"

# The RMFS_TEST_* overrides exist only to test against a fake tablet; users never need them.
WIFI_HOST="${RMFS_TEST_WIFI_HOST:-remarkable.local}"
USB_HOST="${RMFS_TEST_USB_HOST:-10.11.99.1}"
TABLET_PORT="${RMFS_TEST_PORT:-22}"
OUR_MOUNT_TYPE="${RMFS_TEST_MOUNT_TYPE:-fuse.$APP_NAME}"   # what the kernel calls our mounts
TABLET_USER="root"
# Where releases are downloaded from when release/ has no build of its own.
DOWNLOAD_URL="${RMFS_TEST_DOWNLOAD_URL:-https://github.com/tomaszwaraksa/remarkable-fs/releases/latest/download}"

# Host key options come from host_key_options (see "Tablet identity" below).
SSH_OPTIONS=(-o LogLevel=ERROR -p "$TABLET_PORT")

# ---------------------------------------------------------------------------
# Options (set by parse_flags)
# ---------------------------------------------------------------------------

ASSUME_YES=0
DRY_RUN=0
SHOW_STATUS=0
RECONFIGURE=0
UNINSTALL=0
PURGE=0

# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------

if [ -t 1 ]; then
    COLOR_BOLD=$'\e[1m' COLOR_GREEN=$'\e[32m' COLOR_YELLOW=$'\e[33m'
    COLOR_RED=$'\e[31m' COLOR_DIM=$'\e[2m' COLOR_RESET=$'\e[0m'
else
    COLOR_BOLD="" COLOR_GREEN="" COLOR_YELLOW="" COLOR_RED="" COLOR_DIM="" COLOR_RESET=""
fi

say()     { printf '%s\n' "$*"; }
heading() { printf '\n%s%s%s\n' "$COLOR_BOLD" "$*" "$COLOR_RESET"; }
ok()      { printf '%s✓%s %s\n' "$COLOR_GREEN" "$COLOR_RESET" "$*"; }
warn()    { printf '%s!%s %s\n' "$COLOR_YELLOW" "$COLOR_RESET" "$*" >&2; }
die()     { printf '%s✗%s %s\n' "$COLOR_RED" "$COLOR_RESET" "$*" >&2; exit 1; }

usage() {   # prints the comment block at the top of this file
    awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "${BASH_SOURCE[0]}"
}

# indent_dim: indents tool output and shows it dimmed, so it stands apart from our own messages.
indent_dim() {
    sed "s/^/    $COLOR_DIM/; s/\$/$COLOR_RESET/"
}

# ---------------------------------------------------------------------------
# Changes and questions
# ---------------------------------------------------------------------------

# run COMMAND...: the single gate for every change, so --dry never changes anything.
# Read-only checks call their commands directly and also run in a dry run.
run() {
    if [ "$DRY_RUN" -eq 1 ]; then
        printf '%s  [dry]' "$COLOR_DIM"
        printf ' %q' "$@"
        printf '%s\n' "$COLOR_RESET"
    else
        "$@"
    fi
}

# Prompts read from the terminal, not stdin, so the installer can also be fed by a pipe.
has_terminal() {
    { : </dev/tty; } 2>/dev/null
}

# ask QUESTION DEFAULT: prints the answer, or DEFAULT when the user just presses Enter.
ask() {
    local question="$1" default="$2" answer
    if [ "$ASSUME_YES" -eq 1 ]; then
        printf '%s\n' "$default"
        return
    fi
    # Ctrl-D (end of input) stops instead of quietly taking the default.
    read -r -p "$question [$default]: " answer </dev/tty || { printf '\n' >&2; die "Stopped."; }
    printf '%s\n' "${answer:-$default}"
}

# confirm QUESTION: succeeds on yes. Enter means yes; Ctrl-D means no.
confirm() {
    [ "$ASSUME_YES" -eq 1 ] && return 0
    ask_yes_no "$1"
}

# Like confirm, but always asks, also with --yes. For running commands with sudo.
confirm_always() {
    has_terminal || return 1
    ask_yes_no "$1"
}

ask_yes_no() {
    local answer
    read -r -p "$1 [Y/n]: " answer </dev/tty || { printf '\n'; return 1; }
    [[ -z "$answer" || "$answer" =~ ^[Yy] ]]
}

# ---------------------------------------------------------------------------
# Start-up checks
# ---------------------------------------------------------------------------

parse_flags() {
    local flag
    for flag in "$@"; do
        case "$flag" in
            --yes)         ASSUME_YES=1 ;;
            --dry)         DRY_RUN=1 ;;
            --status)      SHOW_STATUS=1 ;;
            --reconfigure) RECONFIGURE=1 ;;
            --uninstall)   UNINSTALL=1 ;;
            --purge)       UNINSTALL=1; PURGE=1 ;;
            -h|--help)     usage; exit 0 ;;
            *)             usage >&2; printf '\nUnknown option: %s\n' "$flag" >&2; exit 2 ;;
        esac
    done
    if [ $((SHOW_STATUS + RECONFIGURE + UNINSTALL)) -gt 1 ]; then
        usage >&2
        printf '\nPlease use only one of --status, --reconfigure, --uninstall and --purge.\n' >&2
        exit 2
    fi
}

require_regular_user() {
    if [ "$(id -u)" -eq 0 ]; then
        die "Please run this as your normal user, not as root or with sudo. remarkable-fs is set up just for you, and the installer asks for your password itself if it ever needs it."
    fi
}

require_linux() {
    [ "$(uname -s)" = "Linux" ] || die "This installer works on Linux only."
}

require_terminal() {
    if [ "$ASSUME_YES" -eq 0 ] && ! has_terminal; then
        die "The installer needs to ask you a few questions, but there is no terminal to ask them in. Run it in a terminal, or add --yes to accept the suggested answers."
    fi
}

# ---------------------------------------------------------------------------
# Step: check what's needed and install missing tools
# ---------------------------------------------------------------------------

# Tools the distro scripts install, as "tool:need"; the need names what to install
# (fuse, ssh), so the script installs only what's missing. FUSE ships either
# fusermount3 or fusermount; either one will do.
INSTALLABLE_COMMANDS=(ssh:ssh ssh-keygen:ssh ssh-copy-id:ssh ssh-keyscan:ssh)
# Tools that are part of every systemd-based Linux; checked, never installed.
BASE_COMMANDS=(systemctl sha256sum findmnt timeout)

missing_commands() {   # prints each missing installable tool as "tool:need", one per line
    local entry
    fusermount_path >/dev/null || echo "fusermount3:fuse"
    for entry in "${INSTALLABLE_COMMANDS[@]}"; do
        command -v "${entry%%:*}" >/dev/null || echo "$entry"
    done
}

fusermount_path() {   # prints the FUSE unmount tool, whichever version is installed
    command -v fusermount3 || command -v fusermount
}

# release_mount PATH: lazily unmounts PATH, also when the program behind it is gone.
release_mount() {
    run "$(fusermount_path)" -u -z "$1"
}

require_base_commands() {
    local command
    for command in "${BASE_COMMANDS[@]}"; do
        command -v "$command" >/dev/null \
            || die "This computer is missing '$command', a basic tool of Linux systems that use systemd. remarkable-fs needs a systemd-based Linux to run in the background."
    done
}

# Prints the path of the install script for this Linux distribution, or nothing.
# Tries the distro itself (ID) first, then the distros it is based on (ID_LIKE),
# so e.g. Linux Mint finds ubuntu.sh or debian.sh. Adding a distro = adding a file.
find_distro_script() {
    local distro_id
    [ -r /etc/os-release ] || return 0
    # shellcheck source=/dev/null
    for distro_id in $(. /etc/os-release; echo "${ID:-} ${ID_LIKE:-}"); do
        [[ "$distro_id" =~ ^[a-z0-9._-]+$ ]] || continue   # never build odd paths
        if [ -f "$RELEASE_DIR/scripts/$distro_id.sh" ]; then
            echo "$RELEASE_DIR/scripts/$distro_id.sh"
            return 0
        fi
    done
}

stop_install_manually() {   # stop_install_manually TOOL:NEED...
    say ""
    say "Please install these tools yourself, then run ./install.sh again:"
    printf '  - %s\n' "${@%%:*}"
    say "They usually come in packages called fuse3 and openssh (or openssh-client)."
    exit 1
}

# Systems with a read-only base (Fedora Silverblue, Kinoite, ...) can't take packages the usual way.
is_image_based_system() {
    [ -e /run/ostree-booted ]
}

install_missing_tools() {   # install_missing_tools TOOL:NEED...
    local script needs
    say "remarkable-fs needs a few standard tools that aren't on this computer yet:"
    printf '  - %s\n' "${@%%:*}"

    if is_image_based_system; then
        warn "This system's base can't take packages the usual way (it's image-based, like Fedora Silverblue)."
        stop_install_manually "$@"
    fi
    script="$(find_distro_script)"
    [ -n "$script" ] || { warn "I don't know how to install them on this Linux distribution."; stop_install_manually "$@"; }
    verify_release_file "$script"
    mapfile -t needs < <(printf '%s\n' "${@##*:}" | sort -u)

    say ""
    say "I can install them for you with this script (${script#"$SCRIPT_DIR"/}, run for: ${needs[*]}):"
    grep -v '^#!' "$script" | indent_dim
    # Always asked, also with --yes: this changes the system and runs as administrator.
    confirm_always "Install them now? Your computer may ask for your password" || stop_install_manually "$@"

    run bash "$script" "${needs[@]}" || die "Installing the tools didn't work. Please install them yourself (see above), then run ./install.sh again."
}

require_user_services() {
    systemctl --user show-environment >/dev/null 2>&1 \
        || die "remarkable-fs runs in the background as a user service (systemd), but user services aren't available here. Please run the installer from your normal desktop session."
}

require_fuse_device() {
    [ -c /dev/fuse ] \
        || die "This computer can't show the tablet as a folder: FUSE (/dev/fuse) isn't available. It is part of almost every Linux system; you may need to load it with: sudo modprobe fuse"
}

preflight() {
    local missing
    heading "Checking what's needed"

    # These can't be fixed by installing packages, so they're checked first.
    require_base_commands
    require_user_services
    require_fuse_device

    mapfile -t missing < <(missing_commands)
    if [ ${#missing[@]} -gt 0 ]; then
        install_missing_tools "${missing[@]}"
        if [ "$DRY_RUN" -eq 0 ]; then
            mapfile -t missing < <(missing_commands)
            [ ${#missing[@]} -eq 0 ] || stop_install_manually "${missing[@]}"
        fi
    fi
    ok "Everything remarkable-fs needs is in place"
}

# ---------------------------------------------------------------------------
# Step: install the program
# ---------------------------------------------------------------------------

BINARY_CHANGED=0   # set when a new or different program was installed; step 7 restarts the service

machine_arch() {   # prints this computer's kind as named in the release files
    local machine
    machine="$(uname -m)"
    case "$machine" in
        x86_64)        echo amd64 ;;
        aarch64|arm64) echo arm64 ;;
        *) die "remarkable-fs isn't available for this kind of computer ($machine) yet. It runs on 64-bit Intel/AMD and ARM computers." ;;
    esac
}

release_binary() {   # prints the path of the prebuilt program for this computer
    printf '%s\n' "$BUILD_DIR/$APP_NAME-linux-$(machine_arch)"
}

release_version() {
    cat "$BUILD_DIR/VERSION" 2>/dev/null || echo "unknown version"
}

has_local_build() {   # a developer ran ./build.sh: release/ has a program for this computer
    [ -f "$RELEASE_DIR/$APP_NAME-linux-$(machine_arch)" ]
}

fetch() {   # fetch URL [FILE]: downloads URL into FILE, or prints it
    local out="${2:--}"
    if command -v curl >/dev/null; then
        curl -fsSL --connect-timeout 10 -o "$out" "$1"
    elif command -v wget >/dev/null; then
        wget -q -T 10 -O "$out" "$1"
    else
        return 127
    fi
}

latest_version() {   # prints the version of the latest release, or nothing (offline)
    fetch "$DOWNLOAD_URL/VERSION" 2>/dev/null | head -n 1 || true
}

# Downloads the latest release into a temporary folder and uses it instead of release/.
download_release() {
    [ "$BUILD_DIR" = "$RELEASE_DIR" ] || return 0   # already downloaded
    command -v curl >/dev/null || command -v wget >/dev/null \
        || die "Downloading remarkable-fs needs 'curl' or 'wget'. Please install one of them (e.g. sudo apt install curl) and run the installer again."
    local dir file
    dir="$(mktemp -d)"
    DOWNLOAD_DIR="$dir"
    say "Downloading remarkable-fs from GitHub..."
    for file in "$APP_NAME-linux-$(machine_arch)" SHA256SUMS VERSION configuration.yaml; do
        fetch "$DOWNLOAD_URL/$file" "$dir/$file" 2>/dev/null \
            || die "Couldn't download remarkable-fs ($file) from GitHub. Please check the internet connection and run the installer again."
    done
    BUILD_DIR="$dir"
}

# Makes sure a file of the build is complete and exactly what build.sh produced.
verify_release_file() {   # verify_release_file PATH
    local file="$1" name expected again="Run ./build.sh again."
    name="${file#"$BUILD_DIR"/}"
    [ "$BUILD_DIR" = "$RELEASE_DIR" ] || again="Please run the installer again to download it once more."
    [ -f "$file" ] || die "$name is missing from the release folder. Run ./build.sh first, or download the installer again."

    expected="$(awk -v n="$name" 'length($1) == 64 && substr($0, 67) == n' "$BUILD_DIR/SHA256SUMS" 2>/dev/null || true)"
    [ -n "$expected" ] || die "Can't check $name: its checksum is missing. $again"
    (cd "$BUILD_DIR" && printf '%s\n' "$expected" | sha256sum --check --quiet --status) \
        || die "$name looks damaged or changed (checksum mismatch). $again"
}

file_hash() {   # file_hash PATH: prints the SHA-256 of PATH
    sha256sum <"$1" | cut -d' ' -f1
}

# Succeeds when release/ has a program and the installed one is missing or different.
binary_differs() {
    local binary
    binary="$(release_binary)"
    [ -f "$binary" ] || return 1
    [ -f "$BIN_PATH" ] || return 0
    [ "$(file_hash "$binary")" != "$(file_hash "$BIN_PATH")" ]
}

warn_if_not_on_path() {
    case ":$PATH:" in
        *":$HOME/.local/bin:"*) ;;
        *) warn "The folder $HOME/.local/bin isn't in your PATH, so typing 'remarkable-fs' in a terminal won't find it. That's fine: the background service doesn't need it." ;;
    esac
}

install_binary() {
    local binary
    heading "Installing the program"
    has_local_build || download_release
    binary="$(release_binary)"
    verify_release_file "$binary"

    if ! binary_differs; then
        ok "remarkable-fs $(release_version) is already installed"
    else
        [ -e "$BIN_PATH" ] && say "Replacing the installed remarkable-fs with $(release_version)."
        # install(1) replaces the file instead of overwriting it, so this is safe while it runs.
        run install -D -m 755 "$binary" "$BIN_PATH"
        BINARY_CHANGED=1
        if [ "$DRY_RUN" -eq 1 ]; then
            say "Would install remarkable-fs $(release_version) to $BIN_PATH"
        else
            ok "Installed remarkable-fs $(release_version) to $BIN_PATH"
        fi
    fi
    warn_if_not_on_path
}

# ---------------------------------------------------------------------------
# Step: the tablet key
# ---------------------------------------------------------------------------

explain_key() {
    say "To reach your tablet, remarkable-fs needs a secure key. We make a new one"
    say "just for this, so your other keys stay untouched. Once the tablet knows the"
    say "key, it lets this computer in without asking for a password, also when"
    say "remarkable-fs runs quietly in the background."
    say ""
}

# A key with a passphrase can't be used by the background service: nobody is there to type it.
key_has_passphrase() {
    ! ssh-keygen -y -P "" -f "$KEY_PATH" >/dev/null 2>&1
}

make_key() {
    run mkdir -p -m 700 "$HOME/.ssh"
    run ssh-keygen -q -t ed25519 -N "" -C "$APP_NAME@$(uname -n)" -f "$KEY_PATH"
    if [ "$DRY_RUN" -eq 1 ]; then
        say "Would make a new tablet key: $KEY_PATH"
    else
        ok "Key made: $KEY_PATH"
    fi
}

# The old key is kept next to the new one under a dated name, never deleted or overwritten.
replace_passphrase_key() {
    local backup
    backup="$KEY_PATH.old-$(date +%Y%m%d-%H%M%S)"
    warn "Your tablet key from an earlier setup is protected by a passphrase. The background service can't type it, so it can't use this key."
    confirm "Make a new key without a passphrase? The old one is kept as $(basename "$backup")" \
        || die "remarkable-fs can't work without a key it can use. Run the installer again when you're ready."
    run mv -n "$KEY_PATH" "$backup"
    if [ -e "$KEY_PATH.pub" ]; then
        run mv -n "$KEY_PATH.pub" "$backup.pub"
    fi
    say "Your old key is kept as $backup"
    make_key
}

ensure_ssh_key() {
    heading "Your tablet key"
    explain_key

    if [ ! -e "$KEY_PATH" ]; then
        confirm "Make the key now?" \
            || die "remarkable-fs can't work without the key. Run the installer again when you're ready."
        make_key
    elif [ ! -r "$KEY_PATH" ]; then
        die "Can't read your tablet key $KEY_PATH. Please check that the file belongs to you, then run ./install.sh again."
    elif key_has_passphrase; then
        replace_passphrase_key
    else
        if [ ! -e "$KEY_PATH.pub" ]; then
            # The public half can always be recreated from the private one.
            run sh -c 'ssh-keygen -y -f "$1" > "$1.pub"' _ "$KEY_PATH"
        fi
        ok "Found your tablet key from an earlier setup, reusing it"
    fi
}

# ---------------------------------------------------------------------------
# Step: connect to the tablet
# ---------------------------------------------------------------------------

TABLET_HOST=""   # the address that worked; step 6 saves it in the config
UPLOAD_LOG=""    # temp file of upload_key, removed on Ctrl-C too

# The tablet's identity (its SSH host key) is remembered in $KNOWN_HOSTS under one name
# for all its addresses, and checked at every connection, the same as the daemon does,
# so nothing else on a network can pose as the tablet. A tablet software update brings
# a new identity; it's trusted again over the USB cable, a direct connection.
has_known_identity() {
    [ -r "$KNOWN_HOSTS" ] && awk -v alias="$HOST_KEY_ALIAS" '$1 == alias { found = 1 } END { exit !found }' "$KNOWN_HOSTS"
}

host_key_options() {   # prints the ssh options that check the tablet's identity, one per line
    local check=yes file="$KNOWN_HOSTS"
    if ! has_known_identity; then
        check=accept-new   # first contact: remember it
        [ "$DRY_RUN" -eq 0 ] || file=/dev/null
    fi
    printf '%s\n' -o "HostKeyAlias=$HOST_KEY_ALIAS" -o HashKnownHosts=no -o "UserKnownHostsFile=$file" -o "StrictHostKeyChecking=$check"
}

identity_lines() {   # identity_lines < known_hosts lines: the tablet's keys, sorted
    awk -v alias="$HOST_KEY_ALIAS" '$1 !~ /^#/ && NF >= 3 { print alias, $2, $3 }' | sort
}

# Succeeds when the tablet's USB address is reached through a USB network adapter, so a
# Wi-Fi network that happens to use the same address can't pass as the cable (as in the daemon).
on_usb_cable() {
    local device
    [ -z "${RMFS_TEST_USB_HOST:-}" ] || return 0   # tests fake the cable
    if command -v ip >/dev/null; then
        device="$(ip -o route get "$USB_HOST" 2>/dev/null | sed -n 's/.* dev \([^ ]*\).*/\1/p')"
        [[ -n "$device" && "$(readlink -f "/sys/class/net/$device/device" 2>/dev/null)" == */usb* ]]
        return
    fi
    for device in /sys/class/net/*; do   # without ip(8): any USB network adapter that is up
        [[ "$(readlink -f "$device/device" 2>/dev/null)" == */usb* && "$(cat "$device/operstate" 2>/dev/null)" != down ]] && return 0
    done
    return 1
}

# Over the USB cable, whatever answers is the tablet itself, so its identity is learned there.
trust_tablet_on_usb() {
    local scanned known updated
    on_usb_cable || return 1
    scanned="$(timeout 10 ssh-keyscan -p "$TABLET_PORT" "$USB_HOST" 2>/dev/null | identity_lines)"
    [ -n "$scanned" ] || return 1
    known="$(awk -v alias="$HOST_KEY_ALIAS" '$1 == alias' "$KNOWN_HOSTS" 2>/dev/null | identity_lines)"
    [ "$scanned" != "$known" ] || return 0
    if [ -n "$known" ]; then
        say "Your tablet has a new identity. That's normal after a software update or a reset,"
        say "and it's connected by cable, so we trust the new one."
    fi
    if [ "$DRY_RUN" -eq 1 ]; then
        say "  [dry] remember the tablet's identity in $(pretty_path "$KNOWN_HOSTS")"
        return 0
    fi
    (umask 077; mkdir -p "$CONFIG_DIR")
    updated="$(mktemp "$CONFIG_DIR/known_hosts.XXXXXX")"
    { awk -v alias="$HOST_KEY_ALIAS" '$1 != alias' "$KNOWN_HOSTS" 2>/dev/null; printf '%s\n' "$scanned"; } >"$updated"
    chmod 600 "$updated" && mv -f "$updated" "$KNOWN_HOSTS"
}

# Logs in with our key only (no password, no other keys) and runs a command on the tablet.
# SSH_TIME_LIMIT caps the whole call, name lookup included, which ConnectTimeout doesn't.
tablet_ssh() {   # tablet_ssh HOST COMMAND
    local host="$1" options
    shift
    mapfile -t options < <(host_key_options)
    timeout "${SSH_TIME_LIMIT:-20}" ssh -i "$KEY_PATH" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=5 \
        "${options[@]}" "${SSH_OPTIONS[@]}" "$TABLET_USER@$host" "$@"
}

tablet_accepts_key() {   # tablet_accepts_key HOST
    SSH_TIME_LIMIT=10 tablet_ssh "$1" true >/dev/null 2>&1 </dev/null
}

# What went wrong, from ssh's error output: identity, rejected or unreachable.
ssh_problem() {   # ssh_problem < ssh error output
    local output
    output="$(cat)"
    case "$output" in
        *"Host key verification failed"*|*"IDENTIFICATION HAS CHANGED"*) echo identity ;;
        *"Permission denied"*) echo rejected ;;
        *) echo unreachable ;;
    esac
}

# The more telling of two problems: identity beats rejected beats unreachable.
worse_problem() {   # worse_problem A B
    case "$1 $2" in
        *identity*) echo identity ;;
        *rejected*) echo rejected ;;
        *) echo unreachable ;;
    esac
}

# Succeeds once the tablet's SSH server greets us, not just when the port opens.
tablet_ssh_ready() {   # tablet_ssh_ready HOST
    # shellcheck disable=SC2016  # $1/$2 expand in the inner shell, on purpose
    timeout 2 bash -c 'exec 3<>"/dev/tcp/$1/$2" && head -c 4 <&3' _ "$1" "$TABLET_PORT" 2>/dev/null \
        | grep -q '^SSH-'
}

configured_host() {   # the host saved by an earlier setup, if any
    config_value host
}

candidate_hosts() {   # the saved address first, then Wi-Fi, then USB; each only once
    printf '%s\n' "$(configured_host)" "$WIFI_HOST" "$USB_HOST" | awk 'NF && !seen[$0]++'
}

# Prints the first address where the tablet accepts our key. Otherwise prints
# why not (identity, rejected or unreachable) and fails.
find_tablet_with_key() {
    local host errors problem=unreachable
    while read -r host; do
        if [ "$host" = "$USB_HOST" ] && tablet_ssh_ready "$host"; then
            trust_tablet_on_usb >&2 || true
        fi
        if errors="$(SSH_TIME_LIMIT=10 tablet_ssh "$host" true 2>&1 >/dev/null </dev/null)"; then
            printf '%s\n' "$host"
            return 0
        fi
        problem="$(worse_problem "$problem" "$(ssh_problem <<<"$errors")")"
    done < <(candidate_hosts)
    printf '%s\n' "$problem"
    return 1
}

was_set_up_before() {
    [ -e "$KEY_PATH" ] && [ -n "$(configured_host)" ]
}

spinner() {   # spinner STEP: one frame of a progress spinner, only on a terminal
    local frames="|/-\\"
    [ -t 1 ] && printf '\r  Waiting for the tablet %s ' "${frames:$1%4:1}"
    return 0
}

clear_spinner() {
    [ -t 1 ] && printf '\r%40s\r' ""
    return 0
}

any_tablet_answers() {
    local host
    while read -r host; do
        tablet_ssh_ready "$host" && return 0
    done < <(candidate_hosts)
    return 1
}

# The tablet was set up before but doesn't answer: most likely asleep, so wait instead of
# starting the introduction over USB again.
wait_for_sleeping_tablet() {
    local waited=0
    say "Your tablet doesn't answer right now. Most likely it's asleep: press its power"
    say "button. Other reasons can be:"
    say "  - it's on another Wi-Fi network than this computer, or its Wi-Fi is off"
    say "  - it was reset or got a software update, and needs the USB cable once"
    say "Waiting for it, or connect it with the USB cable. (Press Ctrl-C to stop.)"
    until any_tablet_answers; do
        spinner "$waited"
        waited=$((waited + 1))
        sleep 1
    done
    clear_spinner
}

wait_for_usb() {
    local waited=0
    say ""
    say "We need to introduce this computer to your tablet once. Connect the tablet"
    say "with a USB cable and wake up its screen. (Press Ctrl-C to stop.)"
    until tablet_ssh_ready "$USB_HOST"; do
        spinner "$waited"
        waited=$((waited + 1))
        if [ "$waited" -eq 30 ]; then
            clear_spinner
            say "  Still waiting. Check that the cable carries data (some only charge) and that the tablet is awake."
        fi
        sleep 1
    done
    clear_spinner
    ok "Tablet found on the USB cable"
}

# The tablet asks for its root password once; the installer never sees or stores it.
# Tool output is kept quiet and only shown when both ways of adding the key fail.
# Keys from a key agent are left out (PubkeyAuthentication=no): with several of them the
# tablet would give up ("Too many authentication failures") before asking for the password.
upload_key() {
    local options
    mapfile -t options < <(host_key_options)
    options+=(-o PubkeyAuthentication=no)
    UPLOAD_LOG="$(mktemp)"
    say ""
    say "Your tablet will ask for its password once, to accept the key. You find it"
    say "on the tablet under Settings > Help > About > Copyrights and licenses"
    say "(scroll to the bottom)."
    say ""
    if ssh-copy-id -i "$KEY_PATH.pub" "${options[@]}" "${SSH_OPTIONS[@]}" "$TABLET_USER@$USB_HOST" </dev/tty >"$UPLOAD_LOG" 2>&1; then
        rm -f "$UPLOAD_LOG"
        return 0
    fi
    warn "That didn't work. Let's try once more in a simpler way."
    if ssh "${options[@]}" "${SSH_OPTIONS[@]}" "$TABLET_USER@$USB_HOST" \
        'key=$(cat); mkdir -p -m 700 ~/.ssh && touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && { grep -qxF "$key" ~/.ssh/authorized_keys || echo "$key" >> ~/.ssh/authorized_keys; }' \
        <"$KEY_PATH.pub" >>"$UPLOAD_LOG" 2>&1; then
        rm -f "$UPLOAD_LOG"
        return 0
    fi
    say "What the tablet said:"
    indent_dim <"$UPLOAD_LOG"
    rm -f "$UPLOAD_LOG"
    return 1
}

# Firmware 3.22+ blocks SSH over Wi-Fi until this is switched on; older firmware has no such command.
enable_wifi_access() {   # enable_wifi_access HOST
    if [ "$(config_value enable_wifi_ssh)" = false ]; then
        say "Leaving the tablet's Wi-Fi access as it is (enable_wifi_ssh: false in your settings)."
        return 0
    fi
    say "Switching on Wi-Fi access on the tablet, so remarkable-fs also works without the cable."
    run tablet_ssh "$1" 'if command -v rm-ssh-over-wlan >/dev/null 2>&1; then rm-ssh-over-wlan on >/dev/null 2>&1; fi; true' </dev/null \
        || warn "Couldn't switch on Wi-Fi access. remarkable-fs will still work over the USB cable."
}

# After setup over USB, use Wi-Fi when it answers; the daemon switches between both by itself anyway.
prefer_wifi() {
    local _
    for _ in 1 2 3; do
        if tablet_accepts_key "$WIFI_HOST"; then
            TABLET_HOST="$WIFI_HOST"
            ok "Your tablet also answers over Wi-Fi"
            return 0
        fi
        sleep 2
    done
    TABLET_HOST="$USB_HOST"
    say "Your tablet doesn't answer over Wi-Fi right now. That's fine: remarkable-fs"
    say "will use Wi-Fi by itself as soon as the tablet is on the same network."
}

# Dry run: show the introduction steps without waiting for a cable or asking for a password.
connect_tablet_dry() {
    say "Would ask you to connect the tablet with a USB cable, remember its identity, then add the key to it:"
    run ssh-copy-id -i "$KEY_PATH.pub" "${SSH_OPTIONS[@]}" "$TABLET_USER@$USB_HOST"
    say "Would switch on Wi-Fi access on the tablet:"
    run tablet_ssh "$USB_HOST" 'rm-ssh-over-wlan on'
    TABLET_HOST="$WIFI_HOST"
}

explain_problem() {   # explain_problem PROBLEM: why we need the cable again
    case "$1" in
        identity)
            say "Your tablet answers, but identifies itself differently than before. That's"
            say "normal after a software update or a reset. Connecting it by cable lets us"
            say "confirm it's really your tablet." ;;
        rejected)
            say "Your tablet doesn't accept this computer's key any more, probably because it"
            say "was reset. Let's introduce this computer to it again." ;;
    esac
}

# Introduces this computer to the tablet over the USB cable: identity, key, Wi-Fi access.
introduce_over_usb() {
    wait_for_usb
    trust_tablet_on_usb || die "Couldn't read your tablet's identity over the cable. Please unplug it, plug it in again and run ./install.sh again."
    if tablet_accepts_key "$USB_HOST"; then
        ok "Your tablet already knows this computer"
    else
        upload_key || true
        tablet_accepts_key "$USB_HOST" \
            || die "The tablet still doesn't accept the key. Please check the password and run ./install.sh again."
        ok "Your tablet now knows this computer"
    fi
    enable_wifi_access "$USB_HOST"
    prefer_wifi
}

connect_tablet() {
    local result problem=unreachable
    heading "Connecting to your tablet"
    [ "$DRY_RUN" -eq 1 ] || (umask 077; mkdir -p "$CONFIG_DIR")

    if [ -e "$KEY_PATH" ] && result="$(find_tablet_with_key)"; then
        TABLET_HOST="$result"
        ok "Your tablet lets us in ($TABLET_HOST)"
        enable_wifi_access "$TABLET_HOST"
        return 0
    fi
    [ -e "$KEY_PATH" ] && problem="$result"

    if [ "$ASSUME_YES" -eq 1 ]; then
        [ "$problem" = unreachable ] && was_set_up_before \
            && die "Your tablet doesn't answer. Wake it up (press its power button) and run the installer again."
        [ "$problem" = identity ] \
            && die "Your tablet identifies itself differently than before, which is normal after a software update. Connect it with the USB cable and run the installer again."
        die "Your tablet needs to be introduced to this computer, which needs the tablet's password. Please run ./install.sh once without --yes."
    fi
    if [ "$DRY_RUN" -eq 1 ]; then
        connect_tablet_dry
        return 0
    fi

    if [ "$problem" = unreachable ] && was_set_up_before; then
        wait_for_sleeping_tablet
        if result="$(find_tablet_with_key)"; then
            TABLET_HOST="$result"
            ok "Your tablet lets us in ($TABLET_HOST)"
            enable_wifi_access "$TABLET_HOST"
            return 0
        fi
        problem="$result"
    fi
    was_set_up_before && explain_problem "$problem"
    introduce_over_usb
}

# ---------------------------------------------------------------------------
# Step: folder and settings
# ---------------------------------------------------------------------------

MOUNT_POINT=""   # absolute path of the folder; step 7 uses it in the service

pretty_path() {   # shows paths under the home folder as ~/...
    # shellcheck disable=SC2088  # a literal ~ is the point here
    case "$1" in
        "$HOME")   printf '~\n' ;;
        "$HOME"/*) printf '~/%s\n' "${1#"$HOME"/}" ;;
        *)         printf '%s\n' "$1" ;;
    esac
}

# Turns what the user typed into an absolute path. Plain names are taken as inside the home folder.
expand_folder() {
    local folder="$1"
    # shellcheck disable=SC2088  # matching the literal ~ the user typed
    case "$folder" in
        "~")   folder="$HOME" ;;
        "~/"*) folder="$HOME/${folder#"~/"}" ;;
        /*)    ;;
        *)     folder="$HOME/$folder" ;;
    esac
    printf '%s\n' "${folder%/}"
}

# Prints the type of what is mounted at PATH ("fuse.remarkable-fs" for ours), or nothing.
# Reads the kernel's mount table, so it also works for a stuck mount that can't be looked at.
mount_type() {   # mount_type PATH
    # The path goes in through the environment: awk -v would turn "\b" in a name into a backspace.
    MOUNT_PATH="$1" awk '
        function unescape(s,    out, i) {   # the table writes " " as \040 and so on
            out = ""
            for (i = 1; i <= length(s); i++) {
                if (substr(s, i, 1) == "\\" && substr(s, i + 1, 3) ~ /^[0-7][0-7][0-7]$/) {
                    out = out sprintf("%c", (substr(s, i + 1, 1) * 64) + (substr(s, i + 2, 1) * 8) + substr(s, i + 3, 1))
                    i += 3
                } else out = out substr(s, i, 1)
            }
            return out
        }
        {
            for (sep = 7; sep <= NF && $sep != "-"; sep++) ;
            if (unescape($5) == ENVIRON["MOUNT_PATH"]) type = $(sep + 1)
        }
        END { if (type != "") print type }' /proc/self/mountinfo
}

is_mounted() {   # is_mounted PATH
    [ -n "$(mount_type "$1")" ]
}

is_our_mount() {   # is_our_mount PATH: mounted by remarkable-fs
    [ "$(mount_type "$1")" = "$OUR_MOUNT_TYPE" ]
}

# Mounted and actually answering. A crashed remarkable-fs leaves a mount that fails with
# "Transport endpoint is not connected"; that one is mounted but not alive.
mount_alive() {   # mount_alive PATH
    is_our_mount "$1" && timeout 3 stat "$1" >/dev/null 2>&1
}

# Showing the documents. Until it reaches the tablet, remarkable-fs shows the folder empty
# and read-only (mode 555); once the documents are loaded it becomes writable.
tablet_in_folder() {   # tablet_in_folder PATH
    is_our_mount "$1" && [ "$(timeout 3 stat -c %a "$1" 2>/dev/null)" != 555 ] && mount_alive "$1"
}

# The folder must be in the home folder and empty (or new), or already be ours.
folder_is_usable() {   # folder_is_usable PATH: explains and fails when the folder can't be used
    local folder="$1"
    case "$folder" in
        "$HOME"/*) ;;
        *) warn "Please pick a folder inside your home folder ($(pretty_path "$HOME")/...)."; return 1 ;;
    esac
    if is_our_mount "$folder"; then
        return 0   # remarkable-fs from an earlier setup is using it
    elif is_mounted "$folder"; then
        warn "$(pretty_path "$folder") is already used by something else (a drive or network folder). Please pick another one."
    elif [ -e "$folder" ] && [ ! -d "$folder" ]; then
        warn "$(pretty_path "$folder") is a file, not a folder. Please pick another name."
    elif [ -d "$folder" ] && [ -n "$(ls -A "$folder" 2>/dev/null)" ]; then
        warn "$(pretty_path "$folder") already has files in it. Your tablet needs an empty or new folder."
    else
        return 0
    fi
    return 1
}

choose_mount_point() {
    local default answer
    default="$(config_value mount_point)"
    default="${default:-$DEFAULT_MOUNT_POINT}"
    while :; do
        answer="$(ask "Where should your tablet appear on this computer?" "$default")"
        MOUNT_POINT="$(expand_folder "$answer")"
        folder_is_usable "$MOUNT_POINT" && break
        [ "$ASSUME_YES" -eq 0 ] || die "Please run ./install.sh without --yes and pick another folder."
    done
    # A stuck mount from a crashed run fails every check on it; the service step releases it.
    if [ ! -d "$MOUNT_POINT" ] && ! is_mounted "$MOUNT_POINT"; then
        run mkdir -p "$MOUNT_POINT"
    fi
    ok "Your tablet will appear in $(pretty_path "$MOUNT_POINT")"
}

config_value() {   # config_value KEY: prints a top-level setting from the config, if set
    [ -r "$CONFIG_FILE" ] || return 0
    awk -v key="$1" '
        function unquote(value,    result, i, c) {
            if (value ~ /^".*"$/) {          # "double quoted": undo \" and \\
                value = substr(value, 2, length(value) - 2)
                for (i = 1; i <= length(value); i++) {
                    c = substr(value, i, 1)
                    if (c == "\\" && i < length(value)) { i++; c = substr(value, i, 1) }
                    result = result c
                }
                return result
            }
            if (value ~ /^'"'"'.*'"'"'$/) {  # '\''single quoted'\'': '\'''\'' means one quote
                value = substr(value, 2, length(value) - 2)
                gsub(/'"'"''"'"'/, "'"'"'", value)
                return value
            }
            sub(/[ \t]+#.*$/, "", value)     # plain: drop a trailing comment
            return value
        }
        index($0, key ":") == 1 {
            value = substr($0, length(key) + 2)
            sub(/^[ \t]+/, "", value)
            if (value !~ /^["'"'"']/) sub(/[ \t]+#.*$/, "", value)
            else if (match(value, /^"([^"\\]|\\.)*"/) || match(value, /^'"'"'([^'"'"']|'"'"''"'"')*'"'"'/)) value = substr(value, 1, RLENGTH)
            sub(/[ \t]+$/, "", value)
            print unquote(value); exit
        }' "$CONFIG_FILE"
}

# Sets one top-level setting, leaving every other line (and comment) as it was.
# Replaces "key:" if present, else the commented-out "# key:" from the template, else appends.
set_config_key() {   # set_config_key KEY VALUE
    local key="$1" value="$2" quoted pattern updated
    quoted="${value//\\/\\\\}"
    quoted="\"${quoted//\"/\\\"}\""
    if [ "$DRY_RUN" -eq 1 ]; then
        printf '%s  [dry] set %s: %s in %s%s\n' "$COLOR_DIM" "$key" "$quoted" "$(pretty_path "$CONFIG_FILE")" "$COLOR_RESET"
        return 0
    fi
    pattern="^$key:"
    grep -q "$pattern" "$CONFIG_FILE" || pattern="^# ?$key:"
    updated="$(mktemp "$CONFIG_FILE.XXXXXX")"
    LINE="$key: $quoted" PATTERN="$pattern" awk '
        !done && $0 ~ ENVIRON["PATTERN"] { print ENVIRON["LINE"]; done = 1; next }
        { print }
        END { if (!done) print ENVIRON["LINE"] }' "$CONFIG_FILE" > "$updated"
    cat "$updated" > "$CONFIG_FILE"   # keeps the file's permissions
    rm -f "$updated"
}

CONFIG_CHANGED=0   # set when write_config changed the settings; the service then restarts

write_config() {
    local before=""
    if [ -e "$CONFIG_FILE" ]; then
        before="$(file_hash "$CONFIG_FILE")"
    else
        verify_release_file "$BUILD_DIR/configuration.yaml"
        run mkdir -p -m 700 "$CONFIG_DIR"
        run install -m 600 "$BUILD_DIR/configuration.yaml" "$CONFIG_FILE"
    fi
    set_config_key host "$TABLET_HOST"
    set_config_key ssh_key "$(pretty_path "$KEY_PATH")"
    set_config_key mount_point "$(pretty_path "$MOUNT_POINT")"
    if [ "$DRY_RUN" -eq 1 ]; then
        say "Would save the settings in $(pretty_path "$CONFIG_FILE")"
        return 0
    fi
    [ "$(file_hash "$CONFIG_FILE")" = "$before" ] || CONFIG_CHANGED=1
    ok "Settings saved in $(pretty_path "$CONFIG_FILE")"

    if [ -n "$(config_value password)" ]; then
        say "Your settings still contain the tablet password. With the key it isn't needed"
        say "any more; you can delete the 'password:' line from that file."
    fi
}

configure() {
    heading "Folder and settings"
    choose_mount_point
    write_config
}

# ---------------------------------------------------------------------------
# Step: background service
# ---------------------------------------------------------------------------

MOUNT_WAIT_SECONDS="${RMFS_TEST_MOUNT_WAIT:-60}"   # override only for tests

service_active() {
    systemctl --user is-active --quiet "$UNIT_NAME" 2>/dev/null
}

# Quotes one argument for a systemd unit line: "…" with \ and " escaped, % and $ doubled.
systemd_quote() {
    local value="$1"
    value="${value//\\/\\\\}"
    value="${value//\"/\\\"}"
    value="${value//%/%%}"
    value="${value//\$/\$\$}"
    printf '"%s"' "$value"
}

# The folder isn't in the unit: remarkable-fs reads mount_point from the settings, so
# editing the settings is enough. PATH starts with the folder of the FUSE tool, which some
# systems (NixOS) keep outside the usual places.
unit_content() {
    local fuse_dir search_path="/usr/local/bin:/usr/bin:/bin"
    fuse_dir="$(dirname "$(fusermount_path || echo /usr/bin/fusermount3)")"
    case ":$search_path:" in *":$fuse_dir:"*) ;; *) search_path="$fuse_dir:$search_path" ;; esac
    cat <<UNIT
# Written by remarkable-fs install.sh. Run ./install.sh --reconfigure to change it.
[Unit]
Description=reMarkable tablet as a folder (remarkable-fs)

[Service]
ExecStart=$(systemd_quote "$BIN_PATH")
Environment=$(systemd_quote "PATH=$search_path")
Restart=on-failure
RestartSec=5
TimeoutStopSec=10

[Install]
WantedBy=default.target
UNIT
}

# Writes the unit only when it changed. Succeeds when it did change.
write_unit() {
    local content
    content="$(unit_content)"
    if [ -r "$UNIT_PATH" ] && [ "$(cat "$UNIT_PATH")" = "$content" ]; then
        return 1
    fi
    if [ "$DRY_RUN" -eq 1 ]; then
        say "  [dry] write $(pretty_path "$UNIT_PATH"):"
        printf '%s\n' "$content" | indent_dim
        return 0
    fi
    # Called in "write_unit && ...", where set -e is off, so failures are caught here.
    if ! { mkdir -p "$(dirname "$UNIT_PATH")" && printf '%s\n' "$content" > "$UNIT_PATH"; }; then
        die "Couldn't write the service file $(pretty_path "$UNIT_PATH")."
    fi
}

# A stuck mount left by a crashed remarkable-fs is released; one started by hand blocks the service.
# While the service runs, its mount is left alone: it may just be busy, and the restart handles it.
require_folder_free() {
    is_our_mount "$MOUNT_POINT" && ! service_active || return 0
    if ! mount_alive "$MOUNT_POINT"; then
        say "An earlier remarkable-fs stopped without cleaning up, so $(pretty_path "$MOUNT_POINT") is stuck. Releasing it."
        release_mount "$MOUNT_POINT" \
            || die "Couldn't release $(pretty_path "$MOUNT_POINT"). Logging out and back in will; then run ./install.sh again."
    else
        die "remarkable-fs is already running in $(pretty_path "$MOUNT_POINT"), started by hand. Please stop it first (Ctrl-C in its window, or: fusermount3 -u \"$MOUNT_POINT\"), then run ./install.sh again."
    fi
}

show_service_problem() {
    say ""
    say "Here's what remarkable-fs reported:"
    journalctl --user -u "$UNIT_NAME" -n 20 --no-pager -o cat 2>/dev/null | indent_dim
    say ""
    say "You can follow it live with: journalctl --user -u $UNIT_NAME -f"
}

wait_for_mount() {
    local waited=0
    say "Waiting for your tablet to appear in $(pretty_path "$MOUNT_POINT")..."
    while [ "$waited" -lt "$MOUNT_WAIT_SECONDS" ]; do
        if tablet_in_folder "$MOUNT_POINT"; then
            ok "Your tablet is now in $(pretty_path "$MOUNT_POINT")"
            return 0
        fi
        if ! service_active; then
            show_service_problem
            die "remarkable-fs stopped right after starting."
        fi
        sleep 1
        waited=$((waited + 1))
    done
    say "remarkable-fs is running but hasn't reached your tablet yet. The tablet is"
    say "probably asleep: press its power button and it will appear by itself."
    say "Check any time with: ./install.sh --status"
}

install_service() {
    local unit_changed=0
    heading "Background service"
    require_folder_free

    write_unit && unit_changed=1
    run systemctl --user daemon-reload
    run systemctl --user enable --quiet "$UNIT_NAME"

    # --reconfigure always restarts, so hand-made changes to the settings are picked up too.
    if service_active && mount_alive "$MOUNT_POINT" && [ "$RECONFIGURE" -eq 0 ] \
        && [ "$unit_changed" -eq 0 ] && [ "$BINARY_CHANGED" -eq 0 ] && [ "$CONFIG_CHANGED" -eq 0 ]; then
        ok "remarkable-fs is already running in the background"
        return 0
    fi
    run systemctl --user restart "$UNIT_NAME"
    ok "remarkable-fs runs in the background and starts by itself when you log in"

    [ "$DRY_RUN" -eq 1 ] || wait_for_mount
}

print_summary() {
    heading "All set"
    if [ "$DRY_RUN" -eq 1 ]; then
        say "Dry run finished. Nothing was changed."
        return 0
    fi
    say "Your tablet:     $(pretty_path "$MOUNT_POINT")"
    say "Check status:    ./install.sh --status"
    say "Settings:        $(pretty_path "$CONFIG_FILE")"
    say "Remove it again: ./install.sh --uninstall"
}

# ---------------------------------------------------------------------------
# Status
# ---------------------------------------------------------------------------

status_line() {   # status_line LABEL good|bad|info TEXT
    local mark=" "
    case "$2" in
        good) mark="${COLOR_GREEN}✓${COLOR_RESET}" ;;
        bad)  mark="${COLOR_RED}✗${COLOR_RESET}" ;;
    esac
    printf '%-10s %s %s\n' "$1" "$mark" "$3"
}

saved_mount_point() {   # absolute folder from the settings, or the default
    local folder
    folder="$(config_value mount_point)"
    expand_folder "${folder:-$DEFAULT_MOUNT_POINT}"
}

saved_key() {   # the key from the settings, or the installer's own
    local key
    key="$(config_value ssh_key)"
    if [ -n "$key" ]; then expand_folder "$key"; else printf '%s\n' "$KEY_PATH"; fi
}

log_file() {   # the log remarkable-fs writes, as set in the settings
    local file
    file="$(config_value log_file)"
    expand_folder "${file:-~/.config/$APP_NAME/log.txt}"
}

# The last connection event remarkable-fs logged, as "<state>|<date time>|<address>".
# state: connected, lost, identity (the tablet identified itself differently), rejected (our key)
# refused (SSH switched off on the tablet) or started (no connection since the last start;
# the log is kept across restarts, so older connections don't count).
last_connection_event() {
    local log
    log="$(log_file)"
    [ -r "$log" ] || return 0
    sed -n -E \
        -e 's/^\[([0-9-]+ [0-9:]+)\].*(Connected SSH session to|Successfully reconnected to reMarkable at) ([^ ]+).*/connected|\1|\3/p' \
        -e 's/^\[([0-9-]+ [0-9:]+)\].*Connection to reMarkable at ([^ ]+) lost.*/lost|\1|\2/p' \
        -e 's/^\[([0-9-]+ [0-9:]+)\].*Refusing to connect to ([^ :]+).*tablet identity changed.*/identity|\1|\2/p' \
        -e 's/^\[([0-9-]+ [0-9:]+)\].*The tablet at ([^ ]+) rejected the key.*/rejected|\1|\2/p' \
        -e 's/^\[([0-9-]+ [0-9:]+)\].*The tablet at ([^ ]+) refused the connection.*/refused|\1|\2/p' \
        -e 's/^\[([0-9-]+ [0-9:]+)\].*Starting remarkable-fs\.\.\..*/started|\1|/p' \
        "$log" | tail -n 1
}

installed_version() {   # prints the installed program's version, or nothing (older programs can't tell)
    "$BIN_PATH" --version 2>/dev/null | awk 'NR == 1 { print $2 }' || true
}

status_program() {
    local found
    if [ ! -x "$BIN_PATH" ]; then
        status_line "Program" bad "not installed"
    elif ! has_local_build; then
        local version
        version="$(installed_version)"
        [ -n "$version" ] || version="(an older version that can't tell its number)"
        status_line "Program" good "remarkable-fs $version"
    elif binary_differs; then
        status_line "Program" info "installed; release/ has a different version ($(release_version))"
    else
        status_line "Program" good "remarkable-fs $(release_version)"
    fi
    found="$(command -v "$APP_NAME" 2>/dev/null || true)"
    if [ -n "$found" ] && [ "$found" != "$BIN_PATH" ]; then
        status_line "" info "Note: typing '$APP_NAME' in a terminal runs another copy, $found. The service isn't affected."
    fi
}

status_service() {
    local since state
    if [ ! -f "$UNIT_PATH" ]; then
        status_line "Service" bad "not set up"
    elif service_active; then
        since="$(systemctl --user show -p ActiveEnterTimestamp --value "$UNIT_NAME" 2>/dev/null)"
        status_line "Service" good "running${since:+ since $since}"
    else
        state="$(systemctl --user is-active "$UNIT_NAME" 2>/dev/null || true)"
        status_line "Service" bad "not running${state:+ ($state)}"
    fi
}

# A copy of remarkable-fs running outside the service (started by hand), as "PID|folder".
# The running copy writes both into its lock file; the PID must still be a remarkable-fs.
other_copy() {
    local pid="" folder="" name main
    [ -r "$LOCK_FILE" ] || return 0
    { read -r pid; read -r folder; } < "$LOCK_FILE" || true
    [[ "$pid" =~ ^[0-9]+$ ]] || return 0
    name="$(cat "/proc/$pid/comm" 2>/dev/null || true)"
    [[ "$name" == "$APP_NAME"* ]] || return 0
    main="$(systemctl --user show -p MainPID --value "$UNIT_NAME" 2>/dev/null || true)"
    [ "$pid" != "$main" ] || return 0
    printf '%s|%s\n' "$pid" "$folder"
}

status_other_copy() {
    local copy pid
    copy="$(other_copy)"
    [ -n "$copy" ] || return 0
    pid="${copy%%|*}"
    status_line "" bad "Another copy of remarkable-fs runs on this computer, started by hand (process $pid, folder $(pretty_path "${copy#*|}")). Only one copy can run at a time, so the service waits until it stops. To stop it: kill $pid"
}

status_folder() {   # status_folder FOLDER
    if tablet_in_folder "$1"; then
        status_line "Folder" good "your tablet is in $(pretty_path "$1")"
    elif mount_alive "$1"; then
        status_line "Folder" info "$(pretty_path "$1") is ready; your documents appear there once the tablet is reached"
    elif is_mounted "$1"; then
        status_line "Folder" bad "$(pretty_path "$1") is stuck (an earlier remarkable-fs stopped without cleaning up). Run ./install.sh to fix it."
    else
        status_line "Folder" bad "$(pretty_path "$1") isn't showing your tablet"
    fi
}

# What remarkable-fs itself last reported about the tablet connection.
status_connection() {
    local event state when address
    service_active || return 0
    event="$(last_connection_event)"
    [ -n "$event" ] || return 0
    IFS='|' read -r state when address <<<"$event"
    case "$state" in
        connected) status_line "Connection" good "connected to $address since $when" ;;
        identity)  status_line "Connection" bad "refused at $when: $(problem_hint identity)" ;;
        rejected)  status_line "Connection" bad "refused at $when: $(problem_hint rejected)" ;;
        refused)   status_line "Connection" bad "refused at $when: SSH may be switched off on the tablet. Connect the USB cable once and remarkable-fs switches it on." ;;
        started)   status_line "Connection" bad "not connected since remarkable-fs started at $when. $(problem_hint unreachable)" ;;
        *)         status_line "Connection" bad "lost at $when. $(problem_hint unreachable)" ;;
    esac
}

problem_hint() {   # problem_hint identity|rejected|unreachable: what it means and what to do
    case "$1" in
        identity) printf '%s' "the tablet identifies itself differently than before. That's normal after a software update or a reset: connect it with the USB cable once and remarkable-fs trusts it again. If neither happened, something else on your network may be posing as your tablet." ;;
        rejected) printf '%s' "the tablet doesn't accept this computer's key any more, probably because it was reset. Run ./install.sh --reconfigure to introduce this computer again (needs the USB cable once)." ;;
        *)        printf '%s' "Most likely your tablet is asleep: press its power button and it reconnects by itself. Otherwise it may be on another Wi-Fi network than this computer, have its Wi-Fi off, or need the USB cable once after a reset or software update." ;;
    esac
}

# Collects the tablet's details in one read-only ssh call, as key=value lines.
tablet_details_script() {
    cat <<'REMOTE'
echo "model=$(cat /sys/devices/soc0/machine 2>/dev/null)"
v=$(. /etc/os-release 2>/dev/null; echo "$IMG_VERSION")
[ -n "$v" ] || v=$(sed -n 's/^REMARKABLE_RELEASE_VERSION=//p' /usr/share/remarkable/update.conf 2>/dev/null)
[ -n "$v" ] || v=$(cat /etc/version 2>/dev/null)
echo "firmware=$v"
df -Pk /home 2>/dev/null | awk 'NR == 2 { print "disk_total=" $2; print "disk_used=" $3 }'
echo "documents=$(ls /home/root/.local/share/remarkable/xochitl 2>/dev/null | grep -c '\.metadata$')"
for d in /sys/class/power_supply/*; do
    if [ "$(cat "$d/type" 2>/dev/null)" = Battery ] && [ -r "$d/capacity" ]; then
        echo "battery=$(cat "$d/capacity")"; echo "battery_state=$(cat "$d/status" 2>/dev/null)"; break
    fi
done
echo "uptime_seconds=$(cut -d. -f1 /proc/uptime 2>/dev/null)"
set -- $SSH_CONNECTION; echo "address=${3#::ffff:}"
REMOTE
}

# Values from the tablet are only data: numbers must be digits only, and text loses
# control characters, so nothing the tablet sends can run here or mess up the terminal.
number_or_empty() {   # number_or_empty VALUE
    [[ "$1" =~ ^[0-9]{1,18}$ ]] && printf '%s' "$1"
    return 0
}

plain_text() {   # plain_text VALUE: printable characters only, at most 80
    local value="${1//[^[:print:]]/}"
    printf '%s' "${value:0:80}"
}

gigabytes() {   # gigabytes KILOBYTES
    awk -v kb="$1" 'BEGIN { printf "%.1f GB", kb / 1048576 }'
}

print_tablet_details() {   # print_tablet_details HOST: key=value lines on stdin
    local host="$1" key value model="" firmware="" disk_total="" disk_used="" documents=""
    local battery="" battery_state="" uptime_seconds="" address="" link
    while IFS='=' read -r key value; do
        case "$key" in
            disk_total|disk_used|documents|battery|uptime_seconds)
                printf -v "$key" '%s' "$(number_or_empty "$value")" ;;
            model|firmware|battery_state|address)
                printf -v "$key" '%s' "$(plain_text "$value")" ;;
        esac
    done
    if [ "$host" = "$USB_HOST" ]; then link="over the USB cable"; else link="over Wi-Fi (${address:-$host})"; fi
    status_line "Tablet" good "${model:-reMarkable} · firmware ${firmware:-unknown} · connected $link"
    if [ -n "$disk_total" ] && [ -n "$disk_used" ] && [ "$disk_total" -gt 0 ]; then
        status_line "Storage" info "$(gigabytes "$disk_used") used of $(gigabytes "$disk_total") ($((disk_used * 100 / disk_total)) %) · ${documents:-?} documents"
    fi
    [ -n "$battery" ] && status_line "Battery" info "$battery %${battery_state:+ · ${battery_state,,}}"
    if [ -n "$uptime_seconds" ]; then
        status_line "Up since" info "$(date -d "@$(( $(date +%s) - uptime_seconds ))" '+%Y-%m-%d %H:%M')"
    fi
}

status_tablet() {
    local host key details errors problem=unreachable
    key="$(saved_key)"
    errors="$(mktemp)"
    while read -r host; do
        if details="$(KEY_PATH="$key" tablet_ssh "$host" "$(tablet_details_script)" </dev/null 2>"$errors")"; then
            rm -f "$errors"
            print_tablet_details "$host" <<<"$details"
            return 0
        fi
        problem="$(worse_problem "$problem" "$(ssh_problem <"$errors")")"
    done < <(candidate_hosts)
    rm -f "$errors"
    if [ "$problem" = unreachable ]; then
        status_line "Tablet" info "not reachable right now. $(problem_hint unreachable)"
    else
        status_line "Tablet" bad "answers, but $(problem_hint "$problem")"
    fi
}

show_status() {
    local folder
    heading "remarkable-fs status"
    if [ ! -x "$BIN_PATH" ] && [ ! -f "$UNIT_PATH" ]; then
        say "remarkable-fs isn't set up on this computer yet. Run ./install.sh to set it up."
        return 1
    fi
    folder="$(saved_mount_point)"
    status_program
    status_service
    status_other_copy
    status_folder "$folder"
    status_connection
    status_tablet
    service_active && mount_alive "$folder"
}

# Installed and running. A sleeping or unreachable tablet doesn't count against it:
# the status explains what's wrong, and --reconfigure goes through the setup again.
is_fully_set_up() {
    [ -x "$BIN_PATH" ] && [ -f "$UNIT_PATH" ] && [ -r "$CONFIG_FILE" ] && service_active
}

# On a working setup: offer the program from release/ when it differs from the installed one,
# or, without a build of our own, the latest release when it's newer than the installed one.
offer_update() {
    if has_local_build; then
        binary_differs || return 0
        say ""
        say "release/ has a different version of remarkable-fs ($(release_version)) than the installed one."
    else
        local latest installed
        latest="$(latest_version)"
        installed="$(installed_version)"
        is_newer_release "$latest" "$installed" || return 0
        say ""
        say "A newer version of remarkable-fs is available: $latest (you have ${installed:-an older one})."
    fi
    if [ "$ASSUME_YES" -eq 0 ] && ! has_terminal; then
        say "Run ./install.sh in a terminal to update."
        return 0
    fi
    confirm "Install it now?" || return 0
    MOUNT_POINT="$(saved_mount_point)"
    install_binary
    run systemctl --user restart "$UNIT_NAME"
    if [ "$DRY_RUN" -eq 0 ]; then
        wait_for_mount
        show_status || true
    fi
}

# is_newer_release LATEST INSTALLED: LATEST is a release (v1.2.3) newer than INSTALLED. A
# developer build installed (a git commit, "dev") is never replaced by a release this way;
# a program too old to tell its version is.
is_newer_release() {
    local release='^v[0-9]+(\.[0-9]+)*$'
    [[ "$1" =~ $release ]] || return 1
    [ -n "$2" ] || return 0
    [[ "$2" =~ $release ]] || return 1
    [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n 1)" = "$1" ]
}

# ---------------------------------------------------------------------------
# Uninstall and purge
# ---------------------------------------------------------------------------

list_removals() {   # list_removals FOLDER
    say "This removes:"
    say "  - the background service"
    say "  - the program $(pretty_path "$BIN_PATH")"
    if [ "$PURGE" -eq 1 ]; then
        say "  - your settings and log in $(pretty_path "$CONFIG_DIR")"
        say "  - saved previews in $(pretty_path "$CACHE_DIR")"
        say "  - the folder $(pretty_path "$1"), if it's empty"
        if key_is_ours; then
            say "  - the tablet key $(pretty_path "$KEY_PATH"), from this computer and from the tablet"
        fi
    fi
    say "Nothing on your tablet's documents is touched."
    say ""
}

stop_service() {   # stop_service FOLDER
    [ -e "$BIN_PATH" ] || [ -e "$UNIT_PATH" ] || is_our_mount "$1" || return 0
    if [ -f "$UNIT_PATH" ]; then
        run systemctl --user disable --now --quiet "$UNIT_NAME" 2>/dev/null \
            || warn "The background service was already stopped."
    fi
    if is_our_mount "$1"; then
        release_mount "$1" 2>/dev/null || warn "Couldn't release $(pretty_path "$1"). Logging out and back in will."
    fi
    if [ -f "$UNIT_PATH" ]; then
        run rm -f "$UNIT_PATH"
        run systemctl --user daemon-reload || true
    fi
    run rm -f "$BIN_PATH"
    if [ "$DRY_RUN" -eq 1 ]; then say "Would stop and remove remarkable-fs"; else ok "remarkable-fs is stopped and removed"; fi
}

# Only a key the installer made is removed: its comment starts with "remarkable-fs@".
key_is_ours() {
    [ -r "$KEY_PATH.pub" ] && [[ "$(awk '{ print $3 }' "$KEY_PATH.pub")" == "$APP_NAME@"* ]]
}

# Removes exactly our key line from authorized_keys on the tablet. Exits 0 when removed,
# 3 when it wasn't there, 4 when the file couldn't be rewritten (then it's left untouched).
# shellcheck disable=SC2016  # expands on the tablet
REMOVE_KEY_SCRIPT='key=$(cat); f=~/.ssh/authorized_keys
[ -f "$f" ] && grep -qxF "$key" "$f" || exit 3
grep -vxF "$key" "$f" > "$f.new"; [ $? -le 1 ] || { rm -f "$f.new"; exit 4; }
cat "$f.new" > "$f" && rm -f "$f.new" || exit 4'

# Takes the installer's key off the tablet, if it's reachable. Other keys stay.
remove_key_from_tablet() {
    local public_key host status
    [ -r "$KEY_PATH.pub" ] || return 0
    public_key="$(cat "$KEY_PATH.pub")"
    while read -r host; do
        tablet_accepts_key "$host" || continue
        status=0
        run tablet_ssh "$host" "$REMOVE_KEY_SCRIPT" <<<"$public_key" || status=$?
        case "$status" in
            0) if [ "$DRY_RUN" -eq 1 ]; then say "Would remove the installer's key from the tablet"
               else ok "The tablet no longer knows the installer's key. Other keys on it are left alone."; fi
               return 0 ;;
            3) ok "The installer's key wasn't on the tablet any more"; return 0 ;;
        esac
        warn "Couldn't change the tablet's key list (is its storage full?). It was left as it was."
        break
    done < <(candidate_hosts)
    warn "Couldn't log in to your tablet with the installer's key: it isn't reachable, or it doesn't know the key any more. If it still knows it, connect the USB cable and run this to remove it:"
    say "    ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null root@$USB_HOST \"sed -i '\\|$(awk '{ print $2 }' <<<"$public_key")|d' ~/.ssh/authorized_keys\""
}

purge_data() {   # purge_data FOLDER
    local removed="Settings, saved previews and the installer's key"
    if key_is_ours; then
        remove_key_from_tablet
        run rm -f "$KEY_PATH" "$KEY_PATH.pub"
    elif [ -e "$KEY_PATH" ]; then
        say "Kept $(pretty_path "$KEY_PATH"): the installer didn't make it."
        removed="Settings and saved previews"
    fi
    run rm -rf "$CONFIG_DIR" "$CACHE_DIR"
    if [ -d "$1" ] && ! is_mounted "$1"; then
        run rmdir "$1" 2>/dev/null || say "Kept $(pretty_path "$1") because it has files in it."
    fi
    if [ "$DRY_RUN" -eq 1 ]; then
        say "Would remove: ${removed,}"
    else
        ok "$removed are removed"
    fi
    if compgen -G "$KEY_PATH.old-*" >/dev/null; then
        say "Older tablet keys you had are still in $(pretty_path "$HOME/.ssh") (remarkable_ed25519.old-*)."
    fi
}

nothing_to_remove() {
    [ ! -e "$BIN_PATH" ] && [ ! -e "$UNIT_PATH" ] || return 1
    [ "$PURGE" -eq 1 ] || return 0
    [ ! -e "$CONFIG_DIR" ] && [ ! -e "$CACHE_DIR" ] && [ ! -e "$KEY_PATH" ] && [ ! -e "$KEY_PATH.pub" ]
}

uninstall() {
    local folder
    folder="$(saved_mount_point)"
    if nothing_to_remove; then
        ok "remarkable-fs isn't installed on this computer. Nothing to remove."
        [ "$PURGE" -eq 1 ] || [ ! -e "$CONFIG_DIR" ] || say "Your settings are still there; ./install.sh --purge removes them."
        return 0
    fi
    if [ "$PURGE" -eq 1 ]; then heading "Removing remarkable-fs and everything it saved"; else heading "Removing remarkable-fs"; fi
    # Without user services (e.g. a text console) the service can't be stopped cleanly.
    [ ! -f "$UNIT_PATH" ] || require_user_services
    list_removals "$folder"
    confirm "Go ahead?" || die "Nothing was removed."

    stop_service "$folder"
    if [ "$PURGE" -eq 1 ]; then
        purge_data "$folder"
    else
        say "Your settings and tablet key are kept, so setting it up again is quick."
        say "To remove them too: ./install.sh --purge"
    fi
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

main() {
    parse_flags "$@"
    require_linux
    require_regular_user
    trap '[ -z "$DOWNLOAD_DIR" ] || rm -rf "$DOWNLOAD_DIR"' EXIT
    trap 'printf "\n"; [ -z "$UPLOAD_LOG" ] || rm -f "$UPLOAD_LOG"; die "Stopped. Whatever was done so far stays; run the installer again any time to continue."' INT

    if [ "$SHOW_STATUS" -eq 1 ]; then
        show_status
        return
    fi

    if [ "$UNINSTALL" -eq 0 ] && [ "$RECONFIGURE" -eq 0 ] && is_fully_set_up; then
        show_status
        offer_update
        return
    fi

    require_terminal
    [ "$DRY_RUN" -eq 1 ] && warn "Dry run: nothing will be changed."

    if [ "$UNINSTALL" -eq 1 ]; then
        uninstall
        return
    fi

    preflight
    install_binary
    ensure_ssh_key
    connect_tablet
    configure
    install_service
    print_summary
}

# Sourcing the file (tests do) only defines the functions.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    main "$@"
fi
