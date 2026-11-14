#!/usr/bin/env bash
# Daily CI runner disk hygiene — conservative: preserves active workspaces,
# _tool caches, and KERN persistent caches. Safe to run while jobs execute.
# Installed on each GHA runner CT as /usr/local/sbin/ci-cleanup.sh behind
# ci-cleanup.timer (04:17 UTC daily, Persistent=true).
set -u
LOG=/var/log/ci-cleanup.log
exec >>"$LOG" 2>&1
echo "=== $(date -Is) cleanup start ==="

docker builder prune -af 2>/dev/null | tail -1
docker container prune -f 2>/dev/null | tail -1
docker image prune -af --filter "until=168h" 2>/dev/null | tail -1
docker volume prune -f 2>/dev/null | tail -1

# Orphaned docker-container buildx builders: drop the container, then its
# named _state volume — `volume prune` never touches named volumes and the
# buildkit cache inside is the largest reclaimable block on a runner CT.
docker ps --filter "name=buildx_buildkit_builder" --format '{{.Names}} {{.RunningFor}}' | while read -r n age; do
  case "$age" in *day*|*hour*) docker rm -f "$n" >/dev/null 2>&1;; esac
done
docker ps -a --filter "name=buildx_buildkit_builder" --format '{{.Names}}' | sort > /tmp/.ci-bx-live
for v in $(docker volume ls -q --filter "name=buildx_buildkit_builder"); do
  base=${v%_state}
  grep -qx "$base" /tmp/.ci-bx-live || docker volume rm "$v" >/dev/null 2>&1
done
rm -f /tmp/.ci-bx-live

days=3
for repo in /opt/gha/*/_work/*/; do
  [ -d "$repo" ] || continue
  inner=$(find "$repo" -mindepth 1 -maxdepth 1 -type d ! -name '_tool' ! -name '_temp' -printf '%T@ %p\n' 2>/dev/null | sort -rn | head -1)
  [ -n "$inner" ] || continue
  ts=${inner%% *}; path=${inner#* }
  if [ $(( $(date +%s) - ${ts%.*} )) -gt $((days*86400)) ]; then
    rm -rf "$path"/* "$path"/.[!.]* 2>/dev/null
  fi
done
find /opt/gha/*/_work/_temp -mindepth 1 -maxdepth 1 -mtime +1 -exec rm -rf {} + 2>/dev/null

find /opt/kern/work -maxdepth 1 -name 'kern-job-*' -mtime +2 -exec rm -rf {} + 2>/dev/null

export PATH=/usr/local/go/bin:/root/go/bin:$PATH
go clean -cache 2>/dev/null
npm cache verify 2>/dev/null | tail -1
rm -rf /root/.cache/pip 2>/dev/null

find /tmp -maxdepth 1 -name 'gha-*' -mtime +1 -exec rm -rf {} + 2>/dev/null

# Stale ployz-build locks quarantine later builds on the host
# (getployz/ployz#4174). A live build never exceeds 30 minutes, so
# age-gated removal cannot race a running one.
find /var/tmp -maxdepth 1 -name 'ployz-build-*' -mmin +30 -exec rm -rf {} + 2>/dev/null

# pnpm content-addressable store corruption breaks imports for every job
# until pruned; verify is cheap, prune only on failure.
if command -v pnpm >/dev/null 2>&1; then
  pnpm store status >/dev/null 2>&1 || pnpm store prune >/dev/null 2>&1
fi

echo "=== $(date -Is) done: $(df -h / | awk 'NR==2{print $4" free ("$5" used)"}') ==="
