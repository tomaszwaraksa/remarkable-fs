#!/usr/bin/env bash
# --uninstall / --purge in a private namespace with service stand-ins and the fake tablet.
# shellcheck disable=SC1091,SC2016,SC2012  # test scripts: sourced lib, literal $(…) payloads, ls for display
source "$(dirname "$0")/lib.sh"
setup_home() {   # installed and running; our key plus someone else's key on the tablet
    fresh_home; mkdir -p "$F/.local/bin" "$F/.config/systemd/user" "$F/.config/remarkable-fs" "$F/.cache/remarkable-fs/rendered" "$F/remarkable"
    cp "$SRC/release/remarkable-fs-linux-amd64" "$F/.local/bin/remarkable-fs"; echo "# unit" > "$F/.config/systemd/user/remarkable-fs.service"
    printf 'host: "127.0.0.1"\nmount_point: "~/remarkable"\n' > "$F/.config/remarkable-fs/configuration.yaml"
    touch "$F/.cache/remarkable-fs/rendered/doc.pdf" "$F/.ssh/remarkable_ed25519.old-20261112-094139"
    { echo "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOtherKeyOtherKeyOtherKeyOtherKeyOtherKey other@laptop"; cat "$F/.ssh/remarkable_ed25519.pub"; } \
        | podman exec -i tablet sh -c 'mkdir -p /root/.ssh; cat > /root/.ssh/authorized_keys'
    echo active > "$SD/state"
}
MOUNTED="mount -t tmpfs remarkable-fs $F/remarkable"
left() { (cd "$F" && find . -path ./.ssh -prune -o \( -type f -o -type d -empty \) -print | sort | tr '\n' ' '); echo; ls "$F/.ssh" | tr '\n' ' '; echo; }
tablet_keys() { podman exec tablet sh -c 'cut -d" " -f3 /root/.ssh/authorized_keys' | tr '\n' ' '; echo; }
start_tablet tablet 2222

echo "=== 1. --uninstall --dry → 'Would …' only"; setup_home; inns "$MOUNTED" --uninstall --yes --dry | grep -v '^$'; echo "left: $(left)"
echo; echo "=== 2. --uninstall, Ctrl-D at 'Go ahead?' → nothing removed"; setup_home; in_tty --uninstall </dev/null | tail -2
echo; echo "=== 3. --uninstall"; setup_home; inns "$MOUNTED" --uninstall --yes; echo "calls: $(calls)"; echo "left: $(left)"; echo "tablet keys: $(tablet_keys)"
echo; echo "=== 4. --purge, tablet reachable → only our key leaves the tablet"; setup_home; inns "$MOUNTED" --purge --yes | sed -n '/✓\|!\|Kept\|Older\|exit/p'; echo "left: $(left)"; echo "tablet keys: $(tablet_keys)"
echo; echo "=== 5. --purge again: key not on the tablet any more"; setup_home; podman exec tablet sh -c 'grep -v remarkable-fs@ /root/.ssh/authorized_keys > /tmp/k; cat /tmp/k > /root/.ssh/authorized_keys'
cp "$F/.ssh/remarkable_ed25519.pub" "$WORK/pub"; inns "$MOUNTED" --purge --yes | grep "key"
echo; echo "=== 6. --purge, tablet unreachable → manual command matching the exact key"; setup_home; sed -i "s/127.0.0.1/$CLOSED/" "$F/.config/remarkable-fs/configuration.yaml"
WIFI="$CLOSED" USB=127.0.0.8 inns "$MOUNTED" --purge --yes | sed -n '/✓\|!\|ssh \|exit/p' | cut -c1-160
echo; echo "=== 7. --purge with a key the installer didn't make → key kept"; setup_home; ssh-keygen -q -t ed25519 -N "" -C "me@mine" -f "$WORK/mine"; cp "$WORK/mine" "$F/.ssh/remarkable_ed25519"; cp "$WORK/mine.pub" "$F/.ssh/remarkable_ed25519.pub"
inns "$MOUNTED" --purge --yes | grep -i 'kept\|key'; ls "$F/.ssh"
echo; echo "=== 8. --uninstall when nothing is installed"; fresh_home; rm -rf "$F/.ssh"; inns true --uninstall --yes | sed -n '/✓\|!\|✗\|exit/p'
