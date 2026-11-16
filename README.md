# remarkable-fs

> **Shared as is.** This is a personal project, published so others can use it. It doesn't take contributions: pull requests and issues are switched off, and there's no support. You're welcome to fork it under the [MIT license](LICENSE.md).

A FUSE filesystem implementation in pure Go to browse and open files on a **reMarkable 2** tablet exclusively through your local network, without relying on reMarkable Cloud.

Built with [`github.com/hanwen/go-fuse/v2`](https://github.com/hanwen/go-fuse), it communicates directly with the Linux kernel `/dev/fuse` interface in pure Go, **completely avoiding dependencies on `libfuse` C libraries or CGO**.

---

## Features

- **Bidirectional FUSE Mount**: Mounts reMarkable's document hierarchy into any local folder (`mount_point` in the settings, or `--mount <folder>`) with full support for file browsing, reading, uploading, folder creation, and residue-free deletion.
- **Native Document Browsing**: Accurately maps reMarkable's flat UUID storage (`/home/root/.local/share/remarkable/xochitl/`) into the user's nested folder hierarchy.
- **On-the-Fly TXT & Markdown Conversion**: Automatically converts `.txt` and `.md` files uploaded into the mount into styled, native vector PDF documents formatted to the reMarkable 2 screen aspect ratio (3:4) using embedded Go Fonts and 100% pure Go libraries.
- **On-The-Fly Notebook Conversion**: Transparently converts native reMarkable `.notebook` files to vector `.pdf` documents on demand, rendering both v5 binary and v6 lines format pen strokes.
- **Annotated PDF Doodle Overlay**: Automatically overlays handwritten pen annotations on top of existing PDF documents using vector watermarks.
- **Smart Cache with Automatic Invalidation**: Rendered PDFs are cached locally in `~/.cache/remarkable-fs/rendered/{UUID}_{timestamp}.pdf`. Any file change or new note on the tablet updates its timestamp, immediately invalidating the cache.
- **Seamless Desktop Thumbnails**: Pre-rendered page thumbnails from the tablet are synchronized directly into the FreeDesktop/GNOME thumbnail cache (`~/.cache/thumbnails/large/`) without polluting folders with companion image files.
- **Auto-Discovery & Failover**: Discovers reMarkable tablets via mDNS and DNS (`remarkable.local`), with automatic reconnection and Wi-Fi <-> USB failover if the device sleeps or is re-plugged.
- **Activity Logging**: Logs connections, disconnections, and user actions (directory browsing, file opens/reads) to `~/.config/remarkable-fs/log.txt` (kept across restarts, moved to `log.txt.1` when it passes 5 MB).
- **Pure Go & Portable**: Compiles to a single static binary with zero C library dependencies (no `libfuse`).

---

## Installation

On Linux, an interactive installer sets everything up. It checks that the needed tools are installed and offers to install them, creates a key for the tablet, and connects to the tablet. If needed, it walks you through a one-time USB connection. Finally it runs remarkable-fs as a background service that starts by itself when you log in.

```bash
git clone https://github.com/tomaszwaraksa/remarkable-fs.git
cd remarkable-fs
./install.sh
```

(Or download the source code of the [latest release](https://github.com/tomaszwaraksa/remarkable-fs/releases/latest) and run `./install.sh` in it.) The installer downloads the program for your computer from the latest release and checks it before installing. Run `./install.sh` again later to see the status; it offers newer releases when there are any. If you built the program yourself (see [Build](#build)), the installer uses your build instead.

Your tablet then appears in `~/remarkable` (you can pick another folder during setup).

**Requirements:**
- Linux with systemd, on a 64-bit Intel/AMD or ARM computer
- a reMarkable 2
- Missing tools are installed automatically on Debian, Ubuntu, Fedora, RHEL/Rocky/Alma, Arch, and openSUSE. On other distributions, install `fuse3` and `openssh` yourself first.

| Command | What it does |
|---|---|
| `./install.sh` | Sets up remarkable-fs, or shows its status if it's already set up. If `release/` has a different build than the installed one, it offers to install it. |
| `./install.sh --status` | Shows whether remarkable-fs is running, whether the tablet is connected, and the tablet's model, firmware, storage and battery |
| `./install.sh --reconfigure` | Goes through the setup again, e.g. to change the folder |
| `./install.sh --uninstall` | Removes remarkable-fs and keeps your settings and tablet key |
| `./install.sh --purge` | Removes remarkable-fs together with its settings, cache and tablet key (also from the tablet) |
| `./install.sh --dry` | Dry run: shows what would be done and changes nothing |
| `./install.sh --yes` | Accepts all suggested answers without asking |

**What setup changes on your tablet:**
- It adds a key for this computer to `/home/root/.ssh/authorized_keys`, so remarkable-fs can log in without a password. `--purge` removes it again.
- It switches on SSH over Wi-Fi (`rm-ssh-over-wlan on`, firmware 3.22 and newer), so remarkable-fs works without the cable. remarkable-fs also does this each time it starts; set `enable_wifi_ssh: false` in the settings to leave the tablet's setting alone.
- On each start, remarkable-fs makes sure its key is still on the tablet. That's the key named in `ssh_key`, which the installer sets. Without `ssh_key`, it uses your personal `~/.ssh/id_ed25519.pub` or `~/.ssh/id_rsa.pub`.
- Your documents aren't touched.

**What it puts on your computer:**

| Path | Contents |
|---|---|
| `~/.local/bin/remarkable-fs` | the program |
| `~/.config/systemd/user/remarkable-fs.service` | the background service |
| `~/.config/remarkable-fs/configuration.yaml` | settings (commented, safe to edit) |
| `~/.ssh/remarkable_ed25519` | the tablet key (no passphrase, so the service can use it) |
| `~/.config/remarkable-fs/known_hosts` | the tablet's identity, checked at every connection |

**The tablet's identity:** remarkable-fs remembers your tablet's identity (its SSH host key) and refuses to connect to anything else, so nothing on a network can pose as your tablet. Every tablet software update brings a new identity. remarkable-fs then stops connecting over Wi-Fi until you connect the USB cable once; over the cable it trusts the new identity by itself.

**When it runs:** the service starts whenever you log in, also over SSH or on a text console, and stops when you log out. To keep it running while you're logged out: `loginctl enable-linger`.

**While the tablet sleeps or is away:** the folder stays, but it's empty and read-only, so nothing you save there lands on this computer by mistake. The documents appear by themselves once the tablet is back.

**Several computers:** you can set up remarkable-fs on several computers for the same tablet, e.g. a workstation and a laptop. They take turns when they change documents or refresh the tablet's screen, and each one sees the others' changes the next time it lists a folder. On one computer only one copy runs at a time: a second one, e.g. started by hand next to the service, refuses to start and names the one that's running.

The installer uses the prebuilt programs in `release/`. After changing the code, run `./build.sh`, then `./install.sh` to update.

---

## Architecture

```
remarkable-fs/
├── cmd/
│   └── remarkable-fs/
│       └── main.go           # CLI flags, wiring, signal handling, lifecycle
├── internal/
│   ├── config/               # Load/save ~/.config/remarkable-fs/configuration.yaml
│   ├── discovery/            # Local network tablet auto-detection (mDNS, DNS, ARP)
│   ├── sshclient/            # SSH & SFTP client (agent, keys, password, keepalive)
│   ├── model/                # Domain models (Metadata, Content, FSNode tree)
│   ├── remarkable/           # xochitl parser and virtual hierarchy builder
│   ├── rmv5/                 # Pure-Go parser for reMarkable v5 pen strokes to SVG
│   ├── converter/            # SVG stroke rendering, multi-page PDF generation & overlay
│   ├── thumbnail/            # FreeDesktop thumbnail cache syncer
│   ├── fusefs/               # Pure-Go FUSE filesystem implementation
│   └── logger/               # Activity logger appending to ~/.config/remarkable/log.txt
├── go.mod
└── go.sum
```

---

## Manual Tablet Setup (without the installer)

The installer does these steps for you. To run `remarkable-fs` by hand instead:

1. Connect the tablet with a USB-C data cable. Its root password is under **Settings > Help > About > Copyrights and licenses**.
2. Add your public key for passwordless login, and switch on SSH over Wi-Fi (firmware 3.22 and newer have it off by default):
   ```bash
   ssh-copy-id root@10.11.99.1
   ssh root@10.11.99.1 rm-ssh-over-wlan on
   ```

---

## Build

Compile using Go 1.26+:

```bash
go build -o remarkable-fs ./cmd/remarkable-fs
```

Or build what the installer installs: static binaries for amd64 and arm64, checksums, version, and the config template, all in `release/`. The installer then uses this build instead of downloading a release:

```bash
./build.sh              # runs the tests first; --skip-tests to skip them
```

`remarkable-fs --version` tells which version is installed: the release (e.g. `v0.1.0`), or the git commit it was built from.

---

## Usage

### Basic Usage with Auto-Discovery

If you do not specify `--host`, `remarkable-fs` will discover your tablet on the local Wi-Fi, prompt you to confirm, and save it to `~/.config/remarkable-fs/configuration.yaml`:

```bash
./remarkable-fs --mount ~/remarkable
```

### Specifying Host Explicitly

```bash
./remarkable-fs --mount ~/remarkable --host remarkable.local
```
or with an explicit IP:
```bash
./remarkable-fs --mount ~/remarkable --host 192.168.1.50
```

### All Command-Line Options

```
Usage of ./remarkable-fs:
  -mount string
        Directory to mount the reMarkable filesystem (default: mount_point from the settings)
  -host string
        reMarkable hostname or IP address (e.g. remarkable.local or 192.168.x.x)
  -user string
        SSH username (default: root)
  -port int
        SSH port (default: 22)
  -key string
        Path to SSH private key file (defaults to ~/.ssh/id_ed25519, ~/.ssh/id_rsa, and ssh-agent)
  -password string
        SSH password (prompts securely if keys are not configured)
  -converter string
        Target converter for .txt and .md uploads ('epub' or 'pdf', default: 'epub')
  -max-upload-size-mb int
        Maximum upload size limit in MB (0 = use config, default 200MB, <0 means unlimited)
  -config string
        Custom configuration file path (default: ~/.config/remarkable-fs/configuration.yaml)
  -log string
        Log file path (default: ~/.config/remarkable-fs/log.txt)
  -version
        Print the version and exit
```

---

## Configuration File

Stored at `~/.config/remarkable-fs/configuration.yaml` (auto-generated from the embedded [`default_configuration.yaml`](internal/config/default_configuration.yaml) template):

```yaml
# Target tablet host (e.g. remarkable.local or 192.168.x.x)
host: remarkable.local

# Candidate hosts for automatic reconnection and Wi-Fi <-> USB failover
fallback_hosts:
  - remarkable.local
  - 10.11.99.1

port: 22
user: root
mount_point: ~/remarkable
log_file: ~/.config/remarkable-fs/log.txt
max_upload_size_mb: 200

# Target format for converting plain text (.txt) and Markdown (.md) uploads.
# Options: "epub" (default, reflowable e-reader experience) or "pdf" (fixed vector layout)
converter: "epub"
```

---

## Logging

Activities are logged to `~/.config/remarkable-fs/log.txt`:

```text
[2026-10-18 18:06:07] [INFO         ] Starting remarkable-fs...
[2026-10-18 18:06:08] [INFO         ] Found candidate reMarkable device: remarkable.local (192.168.1.50) via DNS/mDNS lookup
[2026-10-18 18:06:10] [CONNECTION   ] Connected SSH session to remarkable.local:22 (user: root)
[2026-10-18 18:06:10] [CONNECTION   ] Mounted reMarkable read-only FUSE filesystem at /home/user/remarkable
[2026-10-18 18:06:15] [USER_ACTION  ] Browsed folder: "/Work/Projects" (5 items)
[2026-10-18 18:06:18] [USER_ACTION  ] Opened file: "/Work/Projects/Summary.pdf" (1048576 bytes, type: pdf)
[2026-10-18 18:06:22] [USER_ACTION  ] Closed file: "/Work/Projects/Summary.pdf"
[2026-10-18 18:07:00] [DISCONNECTION] Unmounted FUSE filesystem from /home/user/remarkable successfully
```

---

## Unmounting

If you installed it with `./install.sh`, stop or start the background service:

```bash
systemctl --user stop remarkable-fs
systemctl --user start remarkable-fs
```

If you started `remarkable-fs` by hand, press `Ctrl+C` in its terminal, or unmount the folder:

```bash
fusermount -u ~/remarkable
```

---

## License

MIT, see [LICENSE](LICENSE.md).

## Disclaimer

Not affiliated with or endorsed by reMarkable AS. reMarkable is a trademark of reMarkable AS. This software is provided "as is", without warranty of any kind; see the license for details.
