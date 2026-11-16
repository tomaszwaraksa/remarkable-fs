#!/usr/bin/env bash
# Several computers (or copies) on one tablet: the real daemon, twice, against the fake tablet.
# shellcheck disable=SC1091,SC2016  # test scripts: sourced lib, literal $(…) payloads
source "$(dirname "$0")/lib.sh"
BIN="$WORK/remarkable-fs"
LOCK=/home/root/.cache/remarkable-fs.lock
PIDS=()
cleanup() {
    for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null; done
    sleep 1
    for m in "$WORK"/*/remarkable; do fusermount3 -uz "$m" 2>/dev/null || fusermount -uz "$m" 2>/dev/null; done
    if [ -n "${KEEP_LOGS:-}" ]; then   # for debugging: keep both logs and the tablet's folder
        mkdir -p "$KEEP_LOGS"
        for c in one two; do cp "$WORK/$c/log.txt" "$KEEP_LOGS/$c-log.txt" 2>/dev/null; done
        podman exec tablet ls -la /home/root/.local/share/remarkable/xochitl > "$KEEP_LOGS/tablet.txt" 2>&1
    fi
    podman rm -f tablet >/dev/null 2>&1; rm -rf "$WORK"
}
trap cleanup EXIT

(cd "$SRC" && go build -o "$BIN" ./cmd/remarkable-fs) || exit 1
start_tablet tablet 2222
# The tablet's document folder, and a systemctl that logs screen restarts, taking 2 s each.
podman exec tablet sh -c 'mkdir -p /home/root/.local/share/remarkable/xochitl /root/.ssh &&
    printf "#!/bin/sh\ncase \"\$*\" in *restart*) echo start >> /tmp/restarts; sleep 2; echo end >> /tmp/restarts;; esac\n" > /usr/bin/systemctl &&
    chmod +x /usr/bin/systemctl'

# computer NAME: a home folder with its own key (known to the tablet) and settings.
computer() {
    local home="$WORK/$1"
    mkdir -p "$home/.ssh" "$home/.config/remarkable-fs" "$home/remarkable"
    ssh-keygen -q -t ed25519 -N "" -C "remarkable-fs@$1" -f "$home/.ssh/remarkable_ed25519"
    podman exec -i tablet sh -c 'cat >> /root/.ssh/authorized_keys' < "$home/.ssh/remarkable_ed25519.pub"
    cat > "$home/.config/remarkable-fs/configuration.yaml" <<EOF
host: "127.0.0.1"
port: 2222
user: "root"
ssh_key: "$home/.ssh/remarkable_ed25519"
enable_wifi_ssh: false
mount_point: "$home/remarkable"
log_file: "$home/log.txt"
EOF
}

# daemon NAME [ARGS]: starts remarkable-fs as that computer and waits until its folder shows the tablet.
daemon() {
    local home="$WORK/$1"; shift
    HOME="$home" "$BIN" "$@" >> "$home/out.txt" 2>&1 &
    PIDS+=($!)
    for _ in $(seq 30); do
        [ "$(stat -c %a "$home/remarkable" 2>/dev/null)" = 755 ] && return 0
        sleep 1
    done
    echo "remarkable-fs for $1 didn't show the tablet; its log:"; cat "$home/log.txt"; exit 1
}

# A small PDF, copied in with cp (shell redirection into the folder loses the content: 0.ideas.md C13).
make_pdf() { printf '%%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Count 1/Kids[3 0 R]>>endobj\n3 0 obj<</Type/Page/MediaBox[0 0 595 842]/Parent 2 0 R/Resources<<>>>>endobj\nxref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000052 00000 n \n0000000102 00000 n \ntrailer<</Size 4/Root 1 0 R>>\nstartxref\n187\n%%%%EOF\n' > "$WORK/sample.pdf"; }
documents() { podman exec tablet sh -c 'grep -ho "\"visibleName\": \"[^\"]*\"" /home/root/.local/share/remarkable/xochitl/*.metadata 2>/dev/null' | sed 's/.*: //' | sort | tr '\n' ' '; echo; }

make_pdf
computer one; computer two
daemon one
daemon two

echo "=== 1. a second copy on the same computer, other folder → refuses, names the running one"
mkdir -p "$WORK/one/elsewhere"
HOME="$WORK/one" timeout 10 "$BIN" --mount "$WORK/one/elsewhere" 2>&1 | sed "s|$WORK|…|g"; echo "(exit ${PIPESTATUS[0]})"

echo; echo "=== 2. --status on that computer shows a copy started by hand"
mkdir -p "$SD"
env HOME="$WORK/one" PATH="$HERE/shims:$PATH" SHIM_DIR="$SD" bash -c "source '$SRC/install.sh'; COLOR_RED= COLOR_RESET=; status_other_copy" | sed "s|$WORK|…|g"

echo; echo "=== 3. another computer is in the middle of a change → this one waits for it (about 6 s)"
podman exec -d tablet sh -c "mkdir -p /home/root/.cache && flock $LOCK sleep 6"
sleep 1
start=$(date +%s); cp "$WORK/sample.pdf" "$WORK/one/remarkable/Waited.pdf"; echo "upload took $(( $(date +%s) - start )) s"
grep -o 'Waiting for another computer[^.]*' "$WORK/one/log.txt" | head -1

echo; echo "=== 4. both computers upload at the same time → both arrive, screen restarts one at a time"
sleep 6; podman exec tablet rm -f /tmp/restarts
cp "$WORK/sample.pdf" "$WORK/one/remarkable/From one.pdf" & up1=$!
cp "$WORK/sample.pdf" "$WORK/two/remarkable/From two.pdf" & up2=$!
wait "$up1" "$up2"   # not a bare wait: the daemons run in the background too
sleep 8
echo "documents on the tablet: $(documents)"
podman exec tablet sh -c "ls -l /home/root/.local/share/remarkable/xochitl/*.pdf" | awk '{print "  pdf size", $5}'
echo "screen restarts: $(podman exec tablet cat /tmp/restarts | tr '\n' ' ')"
podman exec tablet cat /tmp/restarts | uniq -d | grep -q . && echo "OVERLAP: two restarts at once" || echo "no overlapping restarts"

echo; echo "=== 5. one computer sees the other's upload in its folder"
cp "$WORK/sample.pdf" "$WORK/two/remarkable/Only two.pdf"
seen=""
for i in $(seq 20); do
    [ -e "$WORK/one/remarkable/Only two.pdf" ] && { seen=$i; break; }
    ls "$WORK/one/remarkable" > /dev/null   # a listing, as a file manager does: it checks the tablet for changes
    sleep 1
done
if [ -n "$seen" ]; then echo "visible on one after about $seen s"; else echo "NOT visible on one after 20 s"; fi

echo; echo "=== 6. no errors in either log"
problems="$(grep -h 'ERROR\|WARN' "$WORK/one/log.txt" "$WORK/two/log.txt" | sed "s|$WORK|…|g")"
echo "${problems:-none}"
