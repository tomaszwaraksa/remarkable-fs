#!/usr/bin/env bash
# install.sh's folder and settings step (fake tablet, so the connection step passes).
# shellcheck disable=SC1091,SC2016,SC2012  # test scripts: sourced lib, literal $(…) payloads, ls for display
source "$(dirname "$0")/lib.sh"
CFG="$F/.config/remarkable-fs/configuration.yaml"
settings() { section 'Folder and settings' 'Background service'; }
yaml() { python3 -c "import yaml,sys; c=yaml.safe_load(open('$CFG')); print({k: c.get(k) for k in sys.argv[1:]})" "$@"; }
start_tablet tablet 2222

echo "=== 1. dry run, fresh home → nothing created"
fresh_home accepted; inns true --yes --dry | settings; ls "$F/.config" 2>&1 | head -1; ls -d "$F/remarkable" 2>&1 | head -1
echo; echo "=== 2. new settings, default folder → template copied (600), three keys set"
inns true --yes >/dev/null
stat -c '%a %n' "$F/.config/remarkable-fs" "$CFG" | sed "s|$F|~|"; ls -d "$F/remarkable" | sed "s|$F|~|"
diff "$SRC/release/configuration.yaml" "$CFG"; yaml host ssh_key mount_point converter | cut -c1-150
echo; echo "=== 3. existing settings with own values and a password → only the three keys change, password hint"
cat > "$CFG" <<'Y'
host: "192.168.1.50"
password: "old-secret"
mount_point: "~/remarkable"
converter: "pdf"   # I like pdf
pdf:
  page_size: "a5"
Y
cp "$CFG" "$WORK/before.yaml"; inns true --yes --reconfigure | settings; diff "$WORK/before.yaml" "$CFG"
echo; echo "=== 4. non-empty folder, then a file, then outside home, then an odd name (tty)"
mkdir -p "$F/full"; touch "$F/full/x" "$F/afile"
printf 'full\nafile\n/tmp/elsewhere\nmy tablet & notes \\"x\\" | back\\\\slash\n' | in_tty --reconfigure | settings
python3 -c "import yaml,os; v=yaml.safe_load(open('$CFG'))['mount_point']; p=v.replace('~','$F',1); print('saved:', repr(v)); print('folder exists with that exact name:', os.path.isdir(p))"
echo; echo "=== 5. --yes, saved folder now has files → stops"
printf 'mount_point: "~/full"\n' > "$CFG"; inns true --yes --reconfigure | settings | tail -2
echo; echo "=== 6. folder used by another mount (tmpfs as 'a drive') → refused"
printf 'mount_point: "~/drive"\n' > "$CFG"; mkdir -p "$F/drive"
RMFS_TEST_MOUNT_TYPE=x inns "mount -t ramfs drive $F/drive" --yes --reconfigure | settings | tail -3
