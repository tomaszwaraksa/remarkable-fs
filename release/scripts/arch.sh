#!/usr/bin/env bash
# Installs what remarkable-fs needs on Arch Linux, Manjaro, EndeavourOS and similar.
# Arguments say what's missing: fuse, ssh. Only those packages are installed.
# Uses the package list already on this computer (no full system upgrade).
# If pacman reports "target not found", update the system first: sudo pacman -Syu
set -euo pipefail
packages=()
for need in "$@"; do
    case "$need" in
        fuse) packages+=(fuse3) ;;
        ssh)  packages+=(openssh) ;;
    esac
done
[ ${#packages[@]} -gt 0 ] || exit 0
sudo pacman -S --needed --noconfirm "${packages[@]}"
