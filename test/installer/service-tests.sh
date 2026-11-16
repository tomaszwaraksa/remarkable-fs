#!/usr/bin/env bash
# install.sh's service step in a private mount namespace with systemctl/journalctl stand-ins.
# shellcheck disable=SC1091,SC2016,SC2012  # test scripts: sourced lib, literal $(…) payloads, ls for display
source "$(dirname "$0")/lib.sh"
UNIT="$F/.config/systemd/user/remarkable-fs.service"
service() { section 'Background service' '^$NEVER'; }
start_tablet tablet 2222

echo "=== 1. dry run → unit shown, nothing written"; fresh_home accepted; mkdir -p "$SD"; inns true --yes --dry | service
echo; echo "=== 2. fresh install, tablet appears"; inns true --yes | service; echo "calls: $(calls)"
systemd-analyze --user verify "$UNIT" 2>&1 | grep -v 'Failed to prepare filename\|^$' | sed "s|$F|~|g"; echo "verify exit ${PIPESTATUS[0]}"; sed "s|$F|~|g" "$UNIT"
echo; echo "=== 3. re-run, nothing changed, service running → no restart (shows status)"; inns "mount -t tmpfs remarkable-fs $F/remarkable" --yes | head -8; echo "calls: $(calls)"
echo; echo "=== 4. --reconfigure after a settings change → restart"
sed -i 's/^converter:.*/converter: "pdf"/' "$F/.config/remarkable-fs/configuration.yaml"
echo active > "$SD/state"; inns "mount -t tmpfs remarkable-fs $F/remarkable" --yes --reconfigure | service | grep -i 'running\|now in'; echo "calls: $(calls)"
echo; echo "=== 5. service crashes right after start"; rm -f "$SD/state"; printf x >> "$F/.local/bin/remarkable-fs"; SHIM_MODE=crash inns true --yes | service
echo; echo "=== 6. tablet asleep (service runs, folder empty and read-only)"; rm -f "$SD/state"; printf x >> "$F/.local/bin/remarkable-fs"; SHIM_MODE=asleep inns true --yes | service
echo; echo "=== 7. already running by hand in that folder → stops"; rm -f "$SD/state"; inns "mount -t tmpfs byhand $F/remarkable" --yes | service | tail -2
echo; echo "=== 8. odd folder name ($, %, quotes) → valid unit"; fresh_home accepted; ODD='my % $HOME "x" \back'; mkdir -p "$F/$ODD" "$F/.config/remarkable-fs"
python3 -c "import yaml; open('$F/.config/remarkable-fs/configuration.yaml','w').write(yaml.safe_dump({'mount_point': '~/' + r'''$ODD'''}))"
SHIM_MOUNT="$F/$ODD" inns true --yes | grep -i 'now in'; systemd-analyze --user verify "$UNIT" 2>&1 | sed "s|$F|~|g"; echo "verify exit ${PIPESTATUS[0]}"
