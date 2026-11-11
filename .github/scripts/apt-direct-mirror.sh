#!/usr/bin/env bash
# The hosted runners point apt at azure.archive.ubuntu.com through a mirror list. When ubuntu.com hosts stop
# resolving on the runners, apt's mirror failover keeps reporting the Azure host and every package install fails.
# When the Azure mirror does not resolve, point apt at the first official Ubuntu mirror that does. apt checks every
# index against the Ubuntu archive signing key, so the mirror does not need to be trusted.
set -euo pipefail
if getent hosts azure.archive.ubuntu.com >/dev/null 2>&1; then
  exit 0
fi
mirror=""
for candidate in archive.ubuntu.com/ubuntu mirrors.edge.kernel.org/ubuntu mirrors.mit.edu/ubuntu; do
  if getent hosts "${candidate%%/*}" >/dev/null 2>&1; then
    mirror="http://$candidate/"
    break
  fi
done
if [ -z "$mirror" ]; then
  echo "::warning::no Ubuntu mirror resolves; leaving the apt sources as they are"
  exit 0
fi
echo "::warning::azure.archive.ubuntu.com does not resolve; apt now uses $mirror"
for f in /etc/apt/sources.list.d/ubuntu.sources /etc/apt/sources.list; do
  [ -f "$f" ] || continue
  sudo sed -i -E \
    -e "s#mirror\+file:/etc/apt/apt-mirrors\.txt#$mirror#g" \
    -e "s#https?://(azure\.archive|archive|security)\.ubuntu\.com/ubuntu/?#$mirror#g" \
    "$f"
done
