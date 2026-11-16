# Installer tests

Scripts that run `install.sh` against a fake tablet and stand-ins for systemd, so it can be tested without a real tablet and without touching this computer's services. Each script prints what the installer said in each scenario, with the expected outcome in the scenario's heading; read the output against it.

| Script | What it covers |
|---|---|
| `connect-tests.sh` | tablet connection: first introduction over USB, password, key already known, Wi-Fi switch, sleeping tablet, new identity after a software update, reset tablet, waiting for the cable |
| `settings-tests.sh` | folder choice and the settings file |
| `service-tests.sh` | the systemd unit, restarts, crash, tablet asleep, odd folder names |
| `status-tests.sh` | `--status`, re-runs on a working setup, the update offer, data from the tablet treated as data |
| `uninstall-tests.sh` | `--uninstall` and `--purge`, only our key leaving the tablet |
| `several-computers-tests.sh` | the real daemon, twice, against one fake tablet: a second copy on one computer refused (and shown by `--status`), waiting for another computer's change, two computers uploading at once, seeing the other computer's upload. Builds the daemon (needs Go) and mounts with FUSE; `KEEP_LOGS=<folder>` keeps both logs |
| `download-tests.sh` | installing without a build of our own: download of the latest release, damaged download, GitHub not reachable, `--status` with the installed version, the update offer (newer, same, developer build, offline, a program too old to tell its version). No tablet needed |
| `distro-tests.sh` | the preflight and the per-distro install scripts in Debian, Ubuntu, Fedora, Rocky, Arch and openSUSE containers |

Needs `podman`, `unshare` (user namespaces), `script` (util-linux), `python3` with PyYAML, and network access for the container images.

How it works:
- **Fake tablet:** an Alpine container with OpenSSH on `127.0.0.1:2222`, root password `tabletpw`, and a fake `rm-ssh-over-wlan`. Every start makes new host keys, like a tablet software update.
- **Private mount namespace** (`unshare -rm`): the scripts in `shims/` stand in for `systemctl` and `journalctl`, and record their calls. "Starting the service" mounts a tmpfs where the tablet would appear.
- **Test-only settings** in `install.sh`, never needed by users: `RMFS_TEST_WIFI_HOST`, `RMFS_TEST_USB_HOST`, `RMFS_TEST_PORT`, `RMFS_TEST_MOUNT_WAIT`, `RMFS_TEST_MOUNT_TYPE`, `RMFS_TEST_DOWNLOAD_URL` (a `file://` folder standing in for GitHub's latest release). With `RMFS_TEST_USB_HOST` set, the USB address counts as "on the cable".
- **Sourcing:** `install.sh` only defines its functions when sourced, so single functions can be tested directly (see `status-tests.sh`, scenario i).

Everything is created in a temporary folder and removed at the end, containers included.
