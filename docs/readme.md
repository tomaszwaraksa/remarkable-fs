# remarkable-fs

`remarkable-fs` is a pure Go FUSE filesystem driver that connects your Linux desktop directly to your **reMarkable 2** paper tablet over local network (Wi-Fi or USB cable). It exposes your tablet's documents, notebooks, and folder hierarchy as a standard local folder on your computer—completely independent of the reMarkable Cloud.

Built with [`github.com/hanwen/go-fuse/v2`](https://github.com/hanwen/go-fuse), `remarkable-fs` communicates directly with the Linux kernel `/dev/fuse` character device. It requires **no CGO, no external C libraries (`libfuse` is not needed), and zero runtime binaries on the tablet**.

---

## Key Features

- **Bidirectional Filesystem**:
  - Browse nested folders reconstructed on-the-fly from the tablet's flat UUID database.
  - Read native PDF and EPUB documents directly via streaming SFTP.
  - View handwritten notebooks and doodles rendered transparently on-demand as multi-page vector PDFs.
  - Upload new `.pdf` and `.epub` documents by dragging and dropping in your file manager or copying via shell.
  - Overwrite existing documents in place (`cp`, `rsync`, or editors) with automatic thumbnail and page-count recalculation.
  - Create new folders (`mkdir`) and rename documents or folders across directories (`mv`).
  - Delete files and folders cleanly without leaving orphaned drawing strokes or residual metadata on the tablet.
- **On-the-Fly Vector Rendering**:
  - Pure Go binary parser for reMarkable pen stroke files (`.rm` v5 and v6 lines format).
  - High-precision stroke rendering for ballpoint, fineliner, marker, highlighter, pencil, and calligraphy pens.
  - Transparently composites handwritten doodles directly over base PDF pages with coordinate transformation and scaling.
- **Desktop FreeDesktop Thumbnail Cache**:
  - Synchronizes tablet thumbnails directly into the standard FreeDesktop thumbnail directory (`~/.cache/thumbnails/large/`).
  - GNOME Files (Nautilus), Dolphin, Thunar, and PCManFM display rich document covers and handwritten notes without polluting the tablet folders with companion image files.
- **Smart Local Caching**:
  - Rendered notebook PDFs are cached in `~/.cache/remarkable-fs/rendered/`.
  - Cache entries are keyed by document UUID and tablet modification timestamp, ensuring instantaneous cache invalidation whenever notes are added on the tablet.
- **Zero-Cloud & Privacy First**:
  - Communicates directly over local SSH/SFTP (Wi-Fi or USB).
  - No internet connection, account creation, or cloud sync required.
- **Battery-Friendly On-Demand Connection**:
  - Zero background polling or keepalive pinging when idle, allowing the reMarkable to sleep naturally and preserve full battery life.
  - Seamless automatic reconnection when accessed after waking the device.

---

## Installation

> [!NOTE]
> Automated package installation (such as `.deb`, `.rpm`, `PKGBUILD`, or systemd user service installers) is currently **TBD** as the project evolves. For now, building from source is simple and produces a single self-contained binary.

### Prerequisites

1. **Linux Operating System** with FUSE support enabled in the kernel (`/dev/fuse`).
2. **Go Compiler**: Go 1.22 or newer (1.25+ recommended).
3. **SSH Access to reMarkable**:
   - Developer mode enabled on your tablet (standard out of the box).
   - Root password or SSH public key configured.

### Building from Source

Clone the repository and compile the static binary:

```bash
git clone https://github.com/your-username/remarkable-fs.git
cd remarkable-fs
go build -o remarkable-fs ./cmd/remarkable-fs
```

You can optionally move the compiled binary to your user PATH:

```bash
install -m 755 remarkable-fs ~/.local/bin/remarkable-fs
```

---

## Tablet Setup (One-Time)

### 1. Retrieve SSH Credentials
On your reMarkable tablet, navigate to:
**Settings** > **Help** > **About** > **Copyrights and licenses**

Scroll down to the **GPLv3 Compliance** section at the bottom. You will see:
- The tablet's assigned IP addresses (Wi-Fi and USB).
- The username (`root`).
- The auto-generated root password.

### 2. Enable SSH over Wi-Fi (Firmware 3.22+)
On firmware version 3.22 and newer, the reMarkable operating system disables Wi-Fi SSH listening sockets by default. To enable it:

1. Connect the tablet to your PC using a USB-C cable.
2. Log in over the USB network interface:
   ```bash
   ssh root@10.11.99.1
   ```
3. Run the built-in vendor script to enable SSH over Wi-Fi:
   ```bash
   rm-ssh-over-wlan on
   ```
4. *(Optional but Recommended)* Copy your SSH public key so you never need to type the password again:
   ```bash
   ssh-copy-id -i ~/.ssh/id_ed25519.pub root@10.11.99.1
   ```

*(Note: When you run `remarkable-fs` with password authentication for the first time, it will automatically offer to enable Wi-Fi SSH and install your public key for you).*

---

## Configuration

`remarkable-fs` loads its configuration from:
```bash
~/.config/remarkable-fs/configuration.yaml
```
If the file does not exist on first run, it is automatically created with sensible defaults.

### Configuration File Options

```yaml
# Hostname or IP address of the tablet
host: remarkable.local

# Ordered list of fallback hosts attempted if the primary host is unreachable
fallback_hosts:
  - remarkable.local
  - 192.168.1.50
  - 10.11.99.1

# SSH service port
port: 22

# SSH login user (always "root" on reMarkable)
user: root

# Local directory where the FUSE filesystem will be mounted
mount_point: ~/remarkable

# File path for activity and diagnostic logging (truncated cleanly on each mount)
log_file: ~/.config/remarkable-fs/log.txt

# Maximum allowed file upload size in megabytes (<= 0 for unlimited)
max_upload_size_mb: 200

# Target converter format for plain text (.txt) and Markdown (.md) uploads.
# Options: "epub" (default, reflowable e-reader experience) or "pdf" (fixed vector layout)
converter: epub

# PDF formatting options (active when converter is set to "pdf")
pdf:
  # Preset page dimensions: "remarkable" (default: 158x210mm), "a4", "a5", "letter", "legal", or "custom"
  page_size: "remarkable"
  # Page orientation: "portrait" (default) or "landscape"
  orientation: "portrait"
  # Margins in millimeters
  margins:
    top: 18.0
    bottom: 16.0
    left: 16.0
    right: 16.0
  # Typography settings
  font_size: 10.0      # Base body font size in points (default: 10.0)
  line_height: 5.2    # Body line height in millimeters (default: 5.2)
  code_font_size: 8.5 # Monospace font size in points (default: 8.5)
```

### Configuration Precedence

Settings are resolved in the following order:
1. **Command-line flags** (highest precedence).
2. **`configuration.yaml` file values**.
3. **Internal defaults** (lowest precedence).

### Command-Line Reference

```bash
./remarkable-fs [flags]
```

| Flag | Description | Default |
| :--- | :--- | :--- |
| `-mount <path>` | Directory path to mount the reMarkable filesystem | `~/remarkable` (or config value) |
| `-host <host>` | Tablet hostname or IP address | Auto-discovered / config |
| `-port <port>` | SSH service port | `22` |
| `-user <user>` | SSH username | `root` |
| `-password <pass>` | SSH password (skipped if SSH key/agent works) | Empty |
| `-key <file>` | Path to private SSH key file | `~/.ssh/id_ed25519`, `~/.ssh/id_rsa` |
| `-max-upload-size-mb <int>` | Maximum upload size in MB (`<= 0` for unlimited) | `200` |
| `-converter <format>` | Target format for .txt and .md uploads (`epub` or `pdf`) | `epub` (or config value) |
| `-pdf-page-size <preset>` | PDF page size preset (`remarkable`, `a4`, `a5`, `letter`, `legal`, `custom`) | `remarkable` |
| `-pdf-orientation <orient>` | PDF page orientation (`portrait` or `landscape`) | `portrait` |
| `-config <file>` | Path to alternative configuration YAML | `~/.config/remarkable-fs/configuration.yaml` |

---

## Usage Examples

### Mount with Auto-Discovery
```bash
./remarkable-fs --mount ~/remarkable
```

### Unmounting Cleanly
Press `Ctrl+C` in the terminal where `remarkable-fs` is running, or run:
```bash
fusermount -u ~/remarkable
```

---

## Documentation Index

- [FUSE Filesystem Architecture & Capabilities](file-system.md)
- [Application Architecture & Component Overview](architecture.md)
- [reMarkable Hardware, OS & Storage Internals](remarkable.md)
