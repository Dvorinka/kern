#!/usr/bin/env bash
# vm-scaler — power runner hosts on demand for queued KERN work.
#
# One file per scalable group in $KERN_SCALER_CONF_DIR (default
# /etc/kern-scaler.d/*.conf). Each file is a bash-sourced env file; see
# deployments/kern-scaler.conf.example. The engine is provider-agnostic:
# start/stop/status are whatever commands the group defines, so Proxmox
# (`qm`), libvirt, cloud CLIs or plain ssh all fit the same contract.
#
# Per group, each tick:
#   1. count queued jobs whose runs-on labels intersect KERN_VM_LABELS
#   2. count in-progress jobs pinned to KERN_VM_RUNNER ("" disables)
#   3. pending work  -> clear idle stamp, start the host if down
#      active work   -> clear idle stamp, keep it up
#      neither       -> stamp first idle tick; past KERN_VM_IDLE_MINUTES
#                       run KERN_VM_STOP_CMD, then KERN_VM_KILL_CMD after
#                       KERN_VM_STOP_GRACE_SECONDS if still up
#
# Env:
#   KERN_SCALER_CONF_DIR   config dir      (default /etc/kern-scaler.d)
#   KERN_SCALER_DB_EXEC    psql invocation (default: local `psql`)
#   KERN_SCALER_STATE_DIR  idle stamps     (default /var/lib/kern-scaler)
#   KERN_SCALER_WATCH      seconds between ticks in watch mode; 0 = single
#                          pass, for a timer. Watch mode wakes scaled-to-zero
#                          hosts near queue time instead of at the next
#                          timer tick — see kern-scaler.service.example.
#
# Runs from a systemd timer, e.g. every minute:
#   * * * * * root /opt/kern/vm-scaler.sh >>/var/log/kern-scaler.log 2>&1
set -uo pipefail

CONF_DIR=${KERN_SCALER_CONF_DIR:-/etc/kern-scaler.d}
STATE_DIR=${KERN_SCALER_STATE_DIR:-/var/lib/kern-scaler}
WATCH=${KERN_SCALER_WATCH:-0}
shopt -s nullglob
confs=("$CONF_DIR"/*.conf)
[ ${#confs[@]} -eq 0 ] && { echo "vm-scaler: no configs in $CONF_DIR"; exit 0; }

DB=${KERN_SCALER_DB_EXEC:-}
if [ -z "$DB" ]; then
  : "${KERN_DATABASE_URL:?vm-scaler: set KERN_SCALER_DB_EXEC or KERN_DATABASE_URL}"
  DB="psql \"$KERN_DATABASE_URL\" -Atc"
fi

sql() { $DB "$1" 2>/dev/null | tr -d '[:space:]'; }

tick() {
for conf in "${confs[@]}"; do
  # shellcheck disable=SC1090
  . "$conf"
  : "${KERN_VM_ID:?missing}" "${KERN_VM_LABELS:?missing}"
  : "${KERN_VM_STATE_CMD:?missing}" "${KERN_VM_UP_RE:?missing}"
  : "${KERN_VM_START_CMD:?missing}" "${KERN_VM_STOP_CMD:?missing}"
  KERN_VM_RUNNER=${KERN_VM_RUNNER:-}
  KERN_VM_IDLE_MINUTES=${KERN_VM_IDLE_MINUTES:-15}
  KERN_VM_STOP_GRACE_SECONDS=${KERN_VM_STOP_GRACE_SECONDS:-300}
  KERN_VM_KILL_CMD=${KERN_VM_KILL_CMD:-}
  KERN_VM_WARM_CRON=${KERN_VM_WARM_CRON:-}
  # Seconds a start command is treated as in flight — bounds watch-mode
  # re-fires while the host's state word still reads down.
  KERN_VM_START_COOLDOWN=${KERN_VM_START_COOLDOWN:-180}
  KERN_VM_DB=${KERN_VM_DB:-$DB}

  id=$KERN_VM_ID
  state_file="$STATE_DIR/$id.idle-since"
  mkdir -p "$STATE_DIR"

  vsql() { $KERN_VM_DB "$1" 2>/dev/null | tr -d '[:space:]'; }

  # labels -> postgres array literal; jobs match on ANY overlap
  IFS=',' read -ra LBL <<< "$KERN_VM_LABELS"
  arr="{"; for l in "${LBL[@]}"; do arr+="\"$(echo "$l" | xargs)\","; done; arr="${arr%,}}"

  pending=$($KERN_VM_DB "SELECT count(*) FROM jobs j JOIN workflow_runs r ON r.id=j.run_id
       WHERE j.status='queued' AND r.status='in_progress'
         AND j.runs_on && '$arr'::text[]" | tr -d '[:space:]')

  active=0
  if [ -n "$KERN_VM_RUNNER" ]; then
    active=$($KERN_VM_DB "SELECT count(*) FROM jobs j JOIN runners rn ON rn.id=j.runner_id
         WHERE j.status='in_progress' AND rn.name='$KERN_VM_RUNNER'" | tr -d '[:space:]')
  fi

  state=$(eval "$KERN_VM_STATE_CMD" | head -1)
  is_up=0
  [[ "$state" =~ $KERN_VM_UP_RE ]] && is_up=1
  [ $is_up -eq 1 ] && rm -f "$STATE_DIR/$id.start-since"

  # warm schedule: pre-boot once per day at HH:MM
  if [ -n "$KERN_VM_WARM_CRON" ] && [ "$(date +%H:%M)" = "$KERN_VM_WARM_CRON" ]; then
    today=$(date +%F)
    if [ "$(cat "$STATE_DIR/$id.warm" 2>/dev/null || true)" != "$today" ]; then
      echo "$today" > "$STATE_DIR/$id.warm"
      if [ $is_up -eq 0 ]; then echo "vm-scaler[$id]: warm cron, starting"; eval "$KERN_VM_START_CMD"; fi
    fi
  fi

  if [ "${pending:-0}" != "0" ]; then
    rm -f "$state_file"
    if [ $is_up -eq 0 ]; then
      # Operator opt-outs: the org runner_autostart setting is the fleet
      # kill-switch; runners.wake_on_demand is the per-host flag. Idle
      # shutdown below still applies either way.
      autostart=$(vsql "SELECT coalesce((SELECT settings->>'runner_autostart' FROM orgs LIMIT 1), 'on')")
      wake=true
      if [ -n "$KERN_VM_RUNNER" ]; then
        wake=$(vsql "SELECT coalesce((SELECT wake_on_demand FROM runners WHERE name='$KERN_VM_RUNNER'), true)")
      fi
      if [ "$autostart" = "off" ] || [ "$wake" = "false" ]; then
        echo "vm-scaler[$id]: autostart suppressed (org=$autostart runner wake_on_demand=$wake)"
      else
        # Watch mode ticks far faster than a VM boots — without a
        # cooldown a 10s tick re-fires START_CMD until the state word
        # flips up (double-boot storm on providers that don't no-op).
        start_stamp="$STATE_DIR/$id.start-since"
        started_recently=0
        if [ -f "$start_stamp" ] && [ $(( $(date +%s) - $(cat "$start_stamp") )) -lt "$KERN_VM_START_COOLDOWN" ]; then
          started_recently=1
        fi
        if [ $started_recently -eq 1 ]; then
          echo "vm-scaler[$id]: start already issued, waiting for up state"
        else
          echo "vm-scaler[$id]: $pending queued job(s) matching [$KERN_VM_LABELS], starting"
          date +%s > "$start_stamp"
          eval "$KERN_VM_START_CMD" || true
        fi
      fi
    fi
    continue
  fi

  if [ "${active:-0}" != "0" ]; then
    rm -f "$state_file"
    continue
  fi

  # A down host has no idle clock: clear stale stamps so a fresh boot gets a
  # full idle window instead of an instant shutdown.
  if [ $is_up -eq 0 ]; then
    rm -f "$state_file"
    continue
  fi

  now=$(date +%s)
  since=$(cat "$state_file" 2>/dev/null || echo "$now")
  echo "$since" > "$state_file"
  idle_for=$((now - since))

  if [ $idle_for -ge $((KERN_VM_IDLE_MINUTES * 60)) ]; then
    echo "vm-scaler[$id]: idle ${KERN_VM_IDLE_MINUTES}m, stopping"
    eval "$KERN_VM_STOP_CMD" || true
    if [ -n "$KERN_VM_KILL_CMD" ]; then
      ( sleep "$KERN_VM_STOP_GRACE_SECONDS"
        eval "$KERN_VM_STATE_CMD" | head -1 | grep -qE "$KERN_VM_UP_RE" \
          && { echo "vm-scaler[$id]: still up after ${KERN_VM_STOP_GRACE_SECONDS}s, killing"; eval "$KERN_VM_KILL_CMD"; } ) &
    fi
    rm -f "$state_file"
  fi
done
}

# Watch mode ticks continuously (start latency = WATCH seconds at worst —
# a tagged release that hits a scaled-to-zero host no longer waits out a
# full timer interval); 0 keeps the single-pass timer contract.
if [ "$WATCH" -gt 0 ] 2>/dev/null; then
  echo "vm-scaler: watch mode, tick every ${WATCH}s"
  while :; do tick; sleep "$WATCH"; done
else
  tick
fi
