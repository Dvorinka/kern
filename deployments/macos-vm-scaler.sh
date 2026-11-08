#!/usr/bin/env bash
# macos-vm-scaler — DEPRECATED single-group scaler, kept for the deployed
# Nab9 timer. New deployments should use deployments/vm-scaler.sh, which
# generalizes this to any number of provider-defined groups.
#
# power a Proxmox macOS VM on demand for KERN `runs-on: macos`
# work. Runs on the Proxmox host (Nab9) from a systemd timer, e.g. every minute.
#
#   KERN_MAC_VMID=904            Proxmox VM id
#   KERN_MAC_TRIGGER_LABEL=macos runs-on label that demands the VM
#   KERN_MAC_RUNNER_NAME=macos-904  runner row used for the idle check
#   KERN_MAC_IDLE_MINUTES=15     idle time before shutdown
#   KERN_MAC_WARM_CRON=HH:MM     optional daily pre-warm (empty = off)
#   KERN_MAC_SSH_KEY=/root/.ssh/kern-mac-scaler
#   KERN_MAC_GUEST_USER=tdvorak-dev
#   KERN_MAC_GUEST_IP=10.0.0.53 guest SSH endpoint for poweroff
#   KERN_MAC_DB_EXEC="pct exec 908 -- docker exec -i deployments-postgres-1 psql -U kern -d kern -Atc"
#
# Idle means: no queued jobs matching the trigger label AND no in-progress
# job on the macOS runner. Boot latency is ~2 minutes; queue wait on a cold
# VM is expected.
set -euo pipefail

VMID=${KERN_MAC_VMID:-904}
LABEL=${KERN_MAC_TRIGGER_LABEL:-macos}
RUNNER=${KERN_MAC_RUNNER_NAME:-macos-904}
IDLE_MIN=${KERN_MAC_IDLE_MINUTES:-15}
STATE=${KERN_MAC_STATE:-/var/lib/kern-mac-scaler.idle-since}
DB=${KERN_MAC_DB_EXEC:-pct exec 908 -- docker exec -i deployments-postgres-1 psql -U kern -d kern -Atc}

sql() { $DB "$1"; }

pending() {
  sql "SELECT count(*) FROM jobs j JOIN workflow_runs r ON r.id=j.run_id
       WHERE j.status='queued' AND r.status='in_progress'
         AND j.runs_on && ARRAY['$LABEL']::text[]" | tr -d '[:space:]'
}

active() {
  sql "SELECT count(*) FROM jobs j JOIN runners rn ON rn.id=j.runner_id
       WHERE j.status='in_progress' AND rn.name='$RUNNER'" | tr -d '[:space:]'
}

vm_status() { qm status "$VMID" | awk '{print $2}'; }

# --- warm schedule ---------------------------------------------------------
if [ -n "${KERN_MAC_WARM_CRON:-}" ] && [ "$(date +%H:%M)" = "$KERN_MAC_WARM_CRON" ]; then
  today=$(date +%F)
  if [ "$(cat "$STATE.warm" 2>/dev/null || true)" != "$today" ]; then
    echo "$today" > "$STATE.warm"
    [ "$(vm_status)" = "stopped" ] && { echo "warm cron: starting $VMID"; qm start "$VMID"; exit 0; }
  fi
fi

state=$(vm_status)
need=$(pending)

if [ "$need" != "0" ]; then
  rm -f "$STATE"
  if [ "$state" = "stopped" ]; then
    # Operator opt-outs: org runner_autostart=off is the fleet kill-switch;
    # runners.wake_on_demand=false suppresses this host's start. Idle
    # shutdown below still applies either way.
    autostart=$(sql "SELECT coalesce((SELECT settings->>'runner_autostart' FROM orgs LIMIT 1), 'on')" | tr -d '[:space:]')
    wake=$(sql "SELECT coalesce((SELECT wake_on_demand FROM runners WHERE name='$RUNNER'), true)" | tr -d '[:space:]')
    if [ "$autostart" = "off" ] || [ "$wake" = "false" ]; then
      echo "queued macos work ($need job(s)) but autostart suppressed (org=$autostart runner wake_on_demand=$wake)"
    else
      echo "queued macos work ($need job(s)): starting $VMID"
      qm start "$VMID"
    fi
  fi
  exit 0
fi

# No pending macos work. Busy runner keeps the VM up.
if [ "$(active)" != "0" ]; then
  rm -f "$STATE"
  exit 0
fi

# A stopped VM has no idle clock: clear any stamp left by a previous run so
# a fresh boot gets a full idle window instead of an instant shutdown.
if [ "$state" != "running" ]; then
  rm -f "$STATE"
  exit 0
fi

# Idle: stamp the first idle observation, shut down past the window.
now=$(date +%s)
since=$(cat "$STATE" 2>/dev/null || echo "$now")
echo "$since" > "$STATE"
if [ $((now - since)) -ge $((IDLE_MIN * 60)) ]; then
  echo "idle ${IDLE_MIN}m with no macos work: shutting down $VMID"
  # macOS guests lack qemu-guest-agent, so qm shutdown's ACPI path stalls;
  # SSH poweroff (NOPASSWD /sbin/shutdown sudoers drop-in) is the reliable
  # path. qm shutdown/stop remain as fallback for a wedged guest.
  ssh -i "${KERN_MAC_SSH_KEY:-/root/.ssh/kern-mac-scaler}" \
      -o BatchMode=yes -o ConnectTimeout=5 \
      "${KERN_MAC_GUEST_USER:-tdvorak-dev}@${KERN_MAC_GUEST_IP:-10.0.0.53}" \
      'sudo -n /sbin/shutdown -h now' || qm shutdown "$VMID" --timeout 90 || true
  # Force-stop if still up after 5m.
  ( sleep 300; [ "$(vm_status)" = "running" ] && qm stop "$VMID" ) &
fi
