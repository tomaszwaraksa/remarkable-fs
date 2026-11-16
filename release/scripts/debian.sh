#!/usr/bin/env bash
# Installs what remarkable-fs needs on Debian, Ubuntu, Linux Mint, Pop!_OS and similar.
# Arguments say what's missing: fuse, ssh. Only those packages are installed.
set -euo pipefail
packages=()
for need in "$@"; do
    case "$need" in
        fuse) packages+=(fuse3) ;;
        ssh)  packages+=(openssh-client) ;;
    esac
done
[ ${#packages[@]} -gt 0 ] || exit 0
sudo apt-get update
# --no-remove: stop rather than remove any installed package to make room.
sudo apt-get install -y --no-remove "${packages[@]}"
