#!/usr/bin/env bash
# Installs what remarkable-fs needs on openSUSE Leap, openSUSE Tumbleweed and SLES.
# Arguments say what's missing: fuse, ssh. Only those packages are installed.
set -euo pipefail
packages=()
for need in "$@"; do
    case "$need" in
        fuse) packages+=(fuse3) ;;
        ssh)  packages+=(openssh-clients) ;;
    esac
done
[ ${#packages[@]} -gt 0 ] || exit 0
sudo zypper --non-interactive install "${packages[@]}"
