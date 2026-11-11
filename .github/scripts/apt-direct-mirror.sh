#!/usr/bin/env bash
# The hosted runners point apt at azure.archive.ubuntu.com through a mirror list. When that host stops resolving,
# apt's mirror failover keeps reporting the Azure host instead of falling through to archive.ubuntu.com, so every
# package install fails. When the Azure mirror does not resolve, point apt straight at the Ubuntu archive.
set -euo pipefail
if getent hosts azure.archive.ubuntu.com >/dev/null 2>&1; then
  exit 0
fi
echo "::warning::azure.archive.ubuntu.com does not resolve; apt now uses archive.ubuntu.com directly"
for f in /etc/apt/sources.list.d/ubuntu.sources /etc/apt/sources.list; do
  [ -f "$f" ] || continue
  sudo sed -i \
    -e 's|mirror+file:/etc/apt/apt-mirrors.txt|http://archive.ubuntu.com/ubuntu/|g' \
    -e 's|http://azure\.archive\.ubuntu\.com/ubuntu/\?|http://archive.ubuntu.com/ubuntu/|g' \
    "$f"
done
