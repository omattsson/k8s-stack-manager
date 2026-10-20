#!/usr/bin/env bash
# Record the memory and CPU use of the load test backend and take pprof
# profiles. See "Measure memory and CPU" in loadtest/README.md.
# Works with bash 3.2 (macOS) and Linux.

set -uo pipefail

usage() {
  cat <<'EOF'
Usage: loadtest/capture-resources.sh [options] [-- command [args...]]

Record the RSS, the CPU use and the goroutine count of the backend every
INTERVAL seconds. Take heap, goroutine and CPU profiles at the start, at the
RSS peak and at the end.

Without a command, the script stops when the backend exits, after -d seconds,
or on Ctrl-C. With a command after "--" (for example a k6 run), the script
runs the command and stops when the command ends.

Options:
  -p PID       Backend PID (default: the running process of
               backend/tmp/loadtest-backend.pid or backend/tmp/loadtest-mysql.pid)
  -a ADDR      pprof address (default: PPROF_ADDR, else 127.0.0.1:6060)
  -i SECONDS   Sample interval (default: 5)
  -c SECONDS   Length of each CPU profile, 0 for no CPU profile (default: 30)
  -m PERCENT   Take the peak profiles again when the RSS passes the RSS of
               the last peak profiles by this margin (default: 10)
  -t SECONDS   Take the middle profiles once at this elapsed time, instead
               of at the RSS peak
  -d SECONDS   Stop after this time (default: no limit)
  -o DIR       Output folder (default: loadtest/results)
  -h           Show this help
EOF
}

script_dir=$(cd "$(dirname "$0")" && pwd)
repo_dir=$(cd "$script_dir/.." && pwd)

pid=""
addr="${PPROF_ADDR:-127.0.0.1:6060}"
interval=5
cpu_seconds=30
margin=10
at_time=""
duration=""
out_dir="$script_dir/results"

warn() { echo "WARNING: $*" >&2; }
die() { echo "ERROR: $*" >&2; exit 1; }
is_uint() { case "$1" in '' | *[!0-9]*) return 1 ;; esac; }

while getopts ":p:a:i:c:m:t:d:o:h" opt; do
  case "$opt" in
    p) pid=$OPTARG ;;
    a) addr=$OPTARG ;;
    i) interval=$OPTARG ;;
    c) cpu_seconds=$OPTARG ;;
    m) margin=$OPTARG ;;
    t) at_time=$OPTARG ;;
    d) duration=$OPTARG ;;
    o) out_dir=$OPTARG ;;
    h) usage; exit 0 ;;
    :) usage >&2; die "option -$OPTARG needs a value" ;;
    *) usage >&2; die "unknown option -$OPTARG" ;;
  esac
done
shift $((OPTIND - 1))

for v in "$interval" "$cpu_seconds" "$margin" ${at_time:+"$at_time"} ${duration:+"$duration"}; do
  is_uint "$v" || die "\"$v\" is not a whole number of seconds or percent"
done
[ "$interval" -gt 0 ] || die "the interval must be 1 second or more"

if [ -z "$pid" ]; then
  for f in "$repo_dir/backend/tmp/loadtest-backend.pid" "$repo_dir/backend/tmp/loadtest-mysql.pid"; do
    if [ -f "$f" ] && kill -0 "$(cat "$f")" 2>/dev/null; then
      pid=$(cat "$f")
      break
    fi
  done
  [ -n "$pid" ] || die "no running load test backend. Start it (make loadtest-start-backend) or give -p PID."
fi
is_uint "$pid" || die "\"$pid\" is not a PID"
kill -0 "$pid" 2>/dev/null || die "process $pid does not run"

case "$addr" in
  :*) addr="127.0.0.1$addr" ;;
  0.0.0.0:*) addr="127.0.0.1:${addr#0.0.0.0:}" ;;
esac
pprof_url="http://$addr/debug/pprof"

ts=$(date +%Y%m%d-%H%M%S)
mkdir -p "$out_dir" || die "cannot create $out_dir"
csv="$out_dir/resources-$ts.csv"
prof_dir="$out_dir/profiles/$ts"

# Profiles only when the pprof listener is the backend process (another
# process can hold the port when the backend could not bind it).
pprof_ok=0
if curl -sf --max-time 3 -o /dev/null "$pprof_url/goroutine?debug=1"; then
  pprof_ok=1
  if command -v lsof >/dev/null 2>&1; then
    listener=$(lsof -nP -iTCP:"${addr##*:}" -sTCP:LISTEN -t 2>/dev/null | head -1)
    if [ -n "$listener" ] && [ "$listener" != "$pid" ]; then
      warn "process $listener, not the backend ($pid), listens on $addr. No profiles, no goroutine count."
      pprof_ok=0
    fi
  fi
else
  warn "pprof does not answer on $addr (PPROF_ENABLED=true and PPROF_ADDR, or -a). No profiles, no goroutine count."
fi
[ "$pprof_ok" = 1 ] && mkdir -p "$prof_dir"

clk_tck=$(getconf CLK_TCK 2>/dev/null || echo 100)

now_ms() {
  local t
  t=$(date +%s%N 2>/dev/null)
  case "$t" in
    '' | *[!0-9]*) ;;
    *) echo $((t / 1000000)); return ;;
  esac
  if command -v perl >/dev/null 2>&1; then
    perl -MTime::HiRes=time -e 'printf "%d\n", time() * 1000'
  else
    echo $(($(date +%s) * 1000))
  fi
}

# CPU time (user + system) of the backend in milliseconds. ps prints
# [[dd-]hh:]mm:ss[.cc]; Linux /proc has a finer resolution than its ps.
cpu_ms() {
  if [ -r "/proc/$pid/stat" ]; then
    local stat
    stat=$(cat "/proc/$pid/stat" 2>/dev/null) || return 1
    echo "${stat##*) }" | awk -v hz="$clk_tck" '{ printf "%d\n", ($12 + $13) * 1000 / hz }'
  else
    LC_ALL=C ps -o time= -p "$pid" 2>/dev/null | awk '
      NF {
        t = $1; days = 0
        if (index(t, "-") > 0) { split(t, d, "-"); days = d[1]; t = d[2] }
        n = split(t, p, ":"); s = 0
        for (i = 1; i <= n; i++) s = s * 60 + p[i]
        printf "%d\n", (days * 86400 + s) * 1000
      }'
  fi
}

rss_kib() {
  LC_ALL=C ps -o rss= -p "$pid" 2>/dev/null | tr -d ' '
}

goroutines() {
  [ "$pprof_ok" = 1 ] || return 0
  local body first
  body=$(curl -sf --max-time 3 "$pprof_url/goroutine?debug=1" 2>/dev/null) || return 0
  first=${body%%$'\n'*}
  first=${first##* }
  is_uint "$first" && echo "$first"
}

# fetch NAME PATH TIMEOUT: download one profile into the profile folder.
fetch() {
  if curl -sf --max-time "$3" -o "$prof_dir/$1.tmp" "$pprof_url/$2"; then
    mv "$prof_dir/$1.tmp" "$prof_dir/$1"
  else
    rm -f "$prof_dir/$1.tmp"
    warn "could not take $1 (the backend can have stopped)"
  fi
}

# capture LABEL ELAPSED RSS: heap, goroutine and CPU profiles. A new capture
# of the same label replaces the files.
capture() {
  echo "$(date +%Y-%m-%dT%H:%M:%S%z) $1 elapsed_s=$2 rss_kib=$3" >> "$prof_dir/captures.txt"
  fetch "heap-$1.pb.gz" heap 30
  fetch "goroutine-$1.pb.gz" goroutine 30
  if [ "$cpu_seconds" -gt 0 ]; then
    fetch "cpu-$1.pb.gz" "profile?seconds=$cpu_seconds" $((cpu_seconds + 30))
  fi
}

capture_pid=""
capture_running() { [ -n "$capture_pid" ] && kill -0 "$capture_pid" 2>/dev/null; }

start_capture() {
  echo "Taking the $1 profiles (elapsed ${2}s, RSS $3 KiB)..."
  capture "$1" "$2" "$3" &
  capture_pid=$!
}

print_summary() {
  echo ""
  echo "Resource capture summary"
  awk -F, -v csv="$csv" '
    NR == 1 { next }
    {
      n++; last = $2
      if ($3 + 0 > max_rss) { max_rss = $3 + 0; max_rss_at = $2 }
      if (n == 1) first_rss = $3
      end_rss = $3
      if ($4 != "") { cpu_n++; cpu_sum += $4; if ($4 + 0 > max_cpu) max_cpu = $4 + 0 }
      if ($5 != "" && $5 + 0 > max_g) max_g = $5 + 0
    }
    END {
      if (n == 0) { print "  No samples."; exit }
      printf "  Samples:        %d (%d s)\n", n, last
      printf "  RSS:            start %.1f MiB, end %.1f MiB, max %.1f MiB (%d KiB at %d s)\n",
        first_rss / 1024, end_rss / 1024, max_rss / 1024, max_rss, max_rss_at
      if (cpu_n > 0) printf "  CPU (100 = one core): average %.1f %%, max %.1f %%\n", cpu_sum / cpu_n, max_cpu
      if (max_g > 0) printf "  Goroutines:     max %d\n", max_g
      printf "  CSV:            %s\n", csv
    }' "$csv"
  if [ "$pprof_ok" = 1 ]; then
    echo "  Profiles:       $prof_dir"
  fi
}

stop=0

sampler() {
  trap 'stop=1' INT TERM
  local start_ms rss cpu prev_cpu now prev_ms elapsed pct g peak_capture_rss at_done=0 sleep_pid
  echo "time,elapsed_s,rss_kib,cpu_pct,goroutines" > "$csv"
  echo "Recording PID $pid every ${interval}s to $csv (pprof: $addr)"
  start_ms=$(now_ms)
  prev_ms=$start_ms
  prev_cpu=$(cpu_ms)
  rss=$(rss_kib)
  [ -n "$rss" ] || die "cannot read the RSS of process $pid"
  peak_capture_rss=$rss
  echo "$(date +%Y-%m-%dT%H:%M:%S%z),0,$rss,,$(goroutines)" >> "$csv"
  [ "$pprof_ok" = 1 ] && start_capture start 0 "$rss"

  while [ "$stop" = 0 ]; do
    sleep "$interval" &
    sleep_pid=$!
    wait "$sleep_pid" 2>/dev/null
    kill "$sleep_pid" 2>/dev/null
    [ "$stop" = 0 ] || break

    rss=$(rss_kib)
    if [ -z "$rss" ]; then
      kill -0 "$pid" 2>/dev/null && continue
      echo "Process $pid has exited."
      break
    fi
    now=$(now_ms)
    cpu=$(cpu_ms)
    elapsed=$(((now - start_ms) / 1000))
    pct=""
    if [ -n "$cpu" ] && [ -n "$prev_cpu" ] && [ "$now" -gt "$prev_ms" ]; then
      pct=$(awk -v c=$((cpu - prev_cpu)) -v w=$((now - prev_ms)) 'BEGIN { printf "%.1f", c * 100 / w }')
    fi
    prev_cpu=$cpu
    prev_ms=$now
    g=$(goroutines)
    echo "$(date +%Y-%m-%dT%H:%M:%S%z),$elapsed,$rss,$pct,$g" >> "$csv"

    if [ "$pprof_ok" = 1 ] && ! capture_running; then
      if [ -n "$at_time" ]; then
        if [ "$at_done" = 0 ] && [ "$elapsed" -ge "$at_time" ]; then
          start_capture "at-${at_time}s" "$elapsed" "$rss"
          at_done=1
        fi
      elif [ "$rss" -gt $((peak_capture_rss * (100 + margin) / 100)) ]; then
        start_capture peak "$elapsed" "$rss"
        peak_capture_rss=$rss
      fi
    fi

    if [ -n "$duration" ] && [ "$elapsed" -ge "$duration" ]; then
      break
    fi
  done

  if capture_running; then
    echo "Waiting for the running profile capture..."
    wait "$capture_pid" 2>/dev/null
  fi
  if [ "$pprof_ok" = 1 ] && kill -0 "$pid" 2>/dev/null; then
    rss=$(rss_kib)
    elapsed=$((($(now_ms) - start_ms) / 1000))
    if [ "$cpu_seconds" -gt 0 ]; then
      echo "Taking the end profiles (about ${cpu_seconds}s for the CPU profile)..."
    else
      echo "Taking the end profiles..."
    fi
    capture end "$elapsed" "${rss:-0}"
  fi
  print_summary
}

if [ $# -gt 0 ]; then
  # The command runs in the foreground, so Ctrl-C reaches it. The sampler
  # runs in the background and stops on TERM.
  sampler &
  sampler_pid=$!
  trap ':' INT
  "$@"
  status=$?
  kill -TERM "$sampler_pid" 2>/dev/null
  wait "$sampler_pid" 2>/dev/null
  exit "$status"
fi

sampler
