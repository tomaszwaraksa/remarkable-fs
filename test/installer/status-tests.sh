#!/usr/bin/env bash
# --status, "already set up → status", the update offer, and what the tablet sends being only data.
# shellcheck disable=SC1091,SC2016,SC2012,SC2034  # test scripts: sourced lib, literal $(…) payloads, ls for display
source "$(dirname "$0")/lib.sh"
LOG="$F/.config/remarkable-fs/log.txt"
setup_home() {   # installed and running: program, unit, settings, log; key accepted by the fake tablet
    fresh_home accepted; mkdir -p "$F/.local/bin" "$F/.config/systemd/user" "$F/.config/remarkable-fs" "$F/remarkable"
    cp "$SRC/release/remarkable-fs-linux-amd64" "$F/.local/bin/remarkable-fs"
    echo "# test unit" > "$F/.config/systemd/user/remarkable-fs.service"
    printf 'host: "%s"\nssh_key: "~/.ssh/remarkable_ed25519"\nmount_point: "~/remarkable"\n' "${1:-127.0.0.1}" > "$F/.config/remarkable-fs/configuration.yaml"
    printf '[2026-11-12 09:30:12] [CONNECTION   ] Connected SSH session to 127.0.0.1:2222 (user: root)\n' > "$LOG"
    echo active > "$SD/state"
}
MOUNTED="mount -t tmpfs remarkable-fs $F/remarkable"
start_tablet tablet 2222

echo "=== a. nothing installed"; fresh_home; inns true --status
echo; echo "=== b. running, mounted, tablet answers"; setup_home; inns "$MOUNTED" --status
echo; echo "=== c. tablet not reachable → asleep hint with other reasons"; setup_home "$CLOSED"; WIFI="$CLOSED" USB=127.0.0.8 inns "$MOUNTED" --status | grep -A1 'Tablet'
echo; echo "=== d. log: identity changed / key rejected / lost"
for line in "[ERROR        ] Refusing to connect to 192.168.1.5: tablet identity changed. If you updated" \
            "[ERROR        ] The tablet at 192.168.1.5 rejected the key: it doesn't accept" \
            "[DISCONNECTION] Connection to reMarkable at 192.168.1.5:22 lost (tablet may have gone to sleep)."; do
    setup_home; echo "[2026-11-12 10:15:03] $line" >> "$LOG"; inns "$MOUNTED" --status | grep Connection
done
echo; echo "=== d2. log: connected yesterday, then restarted with the tablet asleep → not connected since the start"
setup_home; echo "[2026-11-12 10:15:03] [INFO         ] Starting remarkable-fs..." >> "$LOG"; inns "$MOUNTED" --status | grep Connection
echo; echo "=== d3. folder mounted but still empty and read-only (tablet not reached yet)"
setup_home; inns "mount -t tmpfs -o mode=555 remarkable-fs $F/remarkable" --status | grep Folder
echo; echo "=== e. tablet has a new identity (software update) → explained"; setup_home; inns "$MOUNTED" --status >/dev/null
start_tablet tablet 2222; podman exec -i tablet sh -c 'mkdir -p /root/.ssh; cat > /root/.ssh/authorized_keys' < "$F/.ssh/remarkable_ed25519.pub"
USB=127.0.0.8 inns "$MOUNTED" --status | grep -A1 Tablet
echo; echo "=== f. service stopped, folder empty"; setup_home; echo failed > "$SD/state"; inns true --status | grep -v 'Tablet\|Storage\|Battery\|Up since'
echo; echo "=== g. plain ./install.sh on a running setup = status only (also when not mounted)"; setup_home; inns true --yes | grep -v 'Storage\|Battery\|Up since'; echo "calls: $(calls)"
echo; echo "=== h. different program in release/ → offer (--yes)"; setup_home; printf x >> "$F/.local/bin/remarkable-fs"
inns "$MOUNTED" --yes | sed -n '/different version/,$p' | grep -v 'Storage\|Battery\|Up since'; echo "calls: $(calls)"
echo; echo "=== i. bad data from the 'tablet' is shown as text, never run"
( source "$SRC/install.sh"; COLOR_GREEN="" COLOR_RED="" COLOR_RESET=""
  printf '%s\n' 'model=evil$(touch /tmp/rmfs-pwned)' 'firmware=1'$'\e''[31mred' 'disk_total=x[$(touch /tmp/rmfs-pwned)]' \
      'disk_used=5' 'uptime_seconds=a[$(touch /tmp/rmfs-pwned)]' 'battery=50' | print_tablet_details 1.2.3.4
  printf '%s\n' 'disk_total=0' 'disk_used=5' | print_tablet_details 1.2.3.4 )
[ -e /tmp/rmfs-pwned ] && { echo "FAIL: command ran"; rm -f /tmp/rmfs-pwned; } || echo "ok: nothing ran"
echo; echo "=== j. conflicting options"; inns true --status --purge | tail -2
