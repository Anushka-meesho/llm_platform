#!/usr/bin/env bash
#
# dev.sh — run/restart all local servers defined in dev.yaml.
#
# Usage:
#   ./dev.sh                 restart everything (default)
#   ./dev.sh start [name]    start all, or one service
#   ./dev.sh stop  [name]    stop all, or one service
#   ./dev.sh restart [name]  restart all, or one service
#   ./dev.sh status          show what's listening
#   ./dev.sh logs <name>     tail a service's log
#
# Services run detached (nohup) and survive this shell. Logs + pids live in .dev/.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG="$ROOT/dev.yaml"
RUNDIR="$ROOT/.dev"
LOGDIR="$RUNDIR/logs"
mkdir -p "$LOGDIR"

# Parse dev.yaml -> TSV lines: name<TAB>dir<TAB>port<TAB>port_mode<TAB>cmd
parse_config() {
  python3 - "$CONFIG" <<'PY'
import sys
services, cur = {}, None
for raw in open(sys.argv[1]):
    line = raw.rstrip("\n")
    if not line.strip() or line.lstrip().startswith("#"):
        continue
    indent = len(line) - len(line.lstrip(" "))
    s = line.strip()
    if indent == 0:                      # top-level key e.g. "services:"
        cur = None
    elif indent == 2 and s.endswith(":"):  # a service name
        cur = s[:-1]
        services[cur] = {}
    elif indent >= 4 and cur and ":" in s:  # a field
        k, _, v = s.partition(":")
        services[cur][k.strip()] = v.strip().strip('"').strip("'")
for name, d in services.items():
    print("\t".join([name, d.get("dir", ""), d.get("port", ""),
                     d.get("port_mode", "none"), d.get("cmd", "")]))
PY
}

# Find the service row for a given name (or empty).
service_row() { parse_config | awk -F'\t' -v n="$1" '$1==n'; }

free_port() {
  local port="$1" pids
  pids="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null || true)"
  if [ -n "$pids" ]; then
    echo "  freeing port $port (killing: $pids)"
    echo "$pids" | xargs kill 2>/dev/null || true
    sleep 1
    pids="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null || true)"
    [ -n "$pids" ] && echo "$pids" | xargs kill -9 2>/dev/null || true
  fi
}

start_one() {
  local name="$1" dir="$2" port="$3" mode="$4" cmd="$5"
  free_port "$port"
  local full="$cmd" prefix=""
  case "$mode" in
    env)  prefix="PORT=$port " ;;
    flag) full="$cmd -- --port $port --strictPort" ;;
  esac
  ( cd "$ROOT/$dir" && nohup env $prefix bash -c "$full" >"$LOGDIR/$name.log" 2>&1 &
    echo $! >"$RUNDIR/$name.pid" )
  echo "  started $name on :$port  (log: .dev/logs/$name.log)"
}

stop_one() {
  local name="$1" port="$3"
  free_port "$port"
  rm -f "$RUNDIR/$name.pid"
  echo "  stopped $name (:$port)"
}

# Run an action over all services, or just one named service.
for_each() {
  local action="$1" only="${2:-}"
  local found=0
  while IFS=$'\t' read -r name dir port mode cmd; do
    [ -z "$name" ] && continue
    if [ -n "$only" ] && [ "$only" != "$name" ]; then continue; fi
    found=1
    "$action" "$name" "$dir" "$port" "$mode" "$cmd"
  done < <(parse_config)
  if [ -n "$only" ] && [ "$found" = 0 ]; then
    echo "unknown service '$only'. Known: $(parse_config | cut -f1 | paste -sd, -)" >&2
    exit 1
  fi
}

# Wait (up to STARTUP_TIMEOUT secs) for the started services' ports to bind, so
# the status printed afterwards reflects reality rather than the instant after
# launch (Go compiles, Vite boots — neither is listening immediately).
STARTUP_TIMEOUT="${STARTUP_TIMEOUT:-30}"
wait_for_up() {
  local only="${1:-}" ports=() waited=0
  while IFS=$'\t' read -r name dir port mode cmd; do
    [ -z "$name" ] && continue
    if [ -n "$only" ] && [ "$only" != "$name" ]; then continue; fi
    ports+=("$port")
  done < <(parse_config)
  [ "${#ports[@]}" -eq 0 ] && return 0
  printf "  waiting for startup (up to %ss)" "$STARTUP_TIMEOUT"
  while [ "$waited" -lt "$STARTUP_TIMEOUT" ]; do
    local all_up=1
    for p in "${ports[@]}"; do
      lsof -nP -iTCP:"$p" -sTCP:LISTEN -t >/dev/null 2>&1 || all_up=0
    done
    if [ "$all_up" = 1 ]; then printf " ok\n"; return 0; fi
    printf "."; sleep 1; waited=$((waited + 1))
  done
  printf " timed out (check .dev/logs)\n"
}

status() {
  printf "%-10s %-6s %-9s %s\n" SERVICE PORT STATUS URL
  while IFS=$'\t' read -r name dir port mode cmd; do
    [ -z "$name" ] && continue
    if lsof -nP -iTCP:"$port" -sTCP:LISTEN -t >/dev/null 2>&1; then
      printf "%-10s %-6s %-9s http://localhost:%s\n" "$name" "$port" "UP" "$port"
    else
      printf "%-10s %-6s %-9s -\n" "$name" "$port" "down"
    fi
  done < <(parse_config)
}

ACTION="${1:-restart}"
TARGET="${2:-}"
case "$ACTION" in
  start)   echo "▶ starting…";   for_each start_one "$TARGET" ;;
  stop)    echo "■ stopping…";   for_each stop_one  "$TARGET" ;;
  restart) echo "↻ restarting…"; for_each stop_one "$TARGET"; for_each start_one "$TARGET" ;;
  status)  status ;;
  logs)
    [ -z "$TARGET" ] && { echo "usage: ./dev.sh logs <name>" >&2; exit 1; }
    tail -f "$LOGDIR/$TARGET.log" ;;
  *) echo "usage: ./dev.sh {start|stop|restart|status|logs} [service]" >&2; exit 1 ;;
esac

case "$ACTION" in
  status|logs) ;;
  start|restart) wait_for_up "$TARGET"; echo; status ;;
  *)             echo; status ;;
esac
