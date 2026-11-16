#!/usr/bin/env bash
# install.sh's tablet connection step against a fake tablet. Read the output against the
# shellcheck disable=SC1091,SC2016,SC2012  # test scripts: sourced lib, literal $(…) payloads, ls for display
# expectation after each heading.
source "$(dirname "$0")/lib.sh"
connect() { section 'Connecting to your tablet' 'Folder and settings'; }
keys_on_tablet() { podman exec tablet sh -c 'grep -c . ~/.ssh/authorized_keys 2>/dev/null || echo 0'; }
start_tablet tablet 2222

echo "=== A. dry run + --yes, tablet doesn't know the key → refused (a real --yes can't introduce)"
fresh_home; WIFI="$CLOSED" inns true --yes --dry | connect
echo; echo "=== B. --yes, tablet doesn't know the key → asks to run without --yes"
WIFI="$CLOSED" inns true --yes | connect
echo; echo "=== C. introduce over USB, Wi-Fi not reachable → identity remembered, key added, Wi-Fi switched on"
askpass "$PW"; WIFI="$CLOSED" in_tty </dev/null | connect
echo "keys on tablet: $(keys_on_tablet), wifi switch: $(podman exec tablet cat /tmp/wifi-on 2>&1), identity: $(cut -d' ' -f1,2 "$F/.config/remarkable-fs/known_hosts" | tr '\n' ' ')"
echo; echo "=== D. re-run, key accepted → no questions"
podman exec tablet rm -f /tmp/wifi-on; WIFI="$CLOSED" inns true --yes | connect
echo "wifi switch: $(podman exec tablet cat /tmp/wifi-on 2>&1)"
echo; echo "=== E. enable_wifi_ssh: false in the settings → Wi-Fi left alone"
podman exec tablet rm -f /tmp/wifi-on; echo 'enable_wifi_ssh: false' >> "$F/.config/remarkable-fs/configuration.yaml"
WIFI="$CLOSED" inns true --yes | connect; echo "wifi switch: $(podman exec tablet cat /tmp/wifi-on 2>&1)"
echo; echo "=== E2. plain re-run on a running setup, tablet asleep → status only"
printf 'host: "%s"\n' "$CLOSED" > "$F/.config/remarkable-fs/configuration.yaml"; WIFI="$CLOSED" USB=127.0.0.8 inns true --yes | sed -n '/status/,$p'
echo; echo "=== F. --reconfigure, tablet set up before but asleep, --yes → 'wake it up', no USB introduction"
printf 'host: "%s"\n' "$CLOSED" > "$F/.config/remarkable-fs/configuration.yaml"; WIFI="$CLOSED" USB=127.0.0.8 inns true --yes --reconfigure | connect
echo; echo "=== G. tablet got a software update (new identity), only Wi-Fi → refused, --yes"
start_tablet tablet 2222; podman exec -i tablet sh -c 'mkdir -p /root/.ssh; cat > /root/.ssh/authorized_keys' < "$F/.ssh/remarkable_ed25519.pub"
USB=127.0.0.8 inns true --yes --reconfigure | connect
echo; echo "=== H. same, now on the USB cable → new identity trusted, no password needed"
WIFI="$CLOSED" inns true --yes --reconfigure | connect
echo; echo "=== I. tablet reset (key gone), tty → explains, introduces over USB again"
podman exec tablet rm -f /root/.ssh/authorized_keys; askpass "$PW"; WIFI="$CLOSED" in_tty --reconfigure </dev/null | connect | grep -v '^$'
echo; echo "=== J. ssh-copy-id fails → fallback adds the key once"
mkdir -p "$WORK/broken"; printf '#!/bin/sh\nexit 1\n' > "$WORK/broken/ssh-copy-id"; chmod +x "$WORK/broken/ssh-copy-id"
podman exec tablet rm -f /root/.ssh/authorized_keys
WIFI="$CLOSED" EXTRA_PATH="$WORK/broken" in_tty --reconfigure </dev/null | connect | grep -v '^$' | head -12
echo "keys on tablet: $(keys_on_tablet)"
echo; echo "=== K. waiting for the USB cable (tablet appears after 5 s)"
fresh_home; ( sleep 5; start_tablet tablet2 2223 ) &
WIFI="$CLOSED" PORT=2223 in_tty </dev/null | connect | grep -v '^$' | tail -4
wait
echo; echo "=== L. wrong password → clear failure, no key added"
fresh_home; askpass wrong; WIFI="$CLOSED" in_tty </dev/null | connect | grep -v '^$'; echo "keys on tablet: $(keys_on_tablet)"
