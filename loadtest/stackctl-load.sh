#!/usr/bin/env bash
# stackctl-load.sh: load the backend with parallel stackctl workers.
#
# Each worker is its own user with its own API key and its own stackctl config
# directory (STACKCTL_CONFIG_DIR), so the config of the person who runs the
# test does not change. Each worker repeats a CLI cycle on a draft stack:
#   stack list --mine, stack create, stack get, override set (env), stack preview,
#   stack extend, override unset, stack delete
# and the script times each command.
#
# Usage:
#   loadtest/stackctl-load.sh [-w WORKERS] [-d SECONDS] [-u API_URL] [-o OUTDIR]
#   (defaults: 10 workers, 120 seconds, http://localhost:8081, loadtest/results/stackctl-<time>)
#
# Needs bash 5 (EPOCHREALTIME), stackctl, curl and jq. Admin login:
# ADMIN_USERNAME / ADMIN_PASSWORD (default admin / admin). The backend must allow
# registration by an admin (the load test backend sets SELF_REGISTRATION=true).
# The stacks are drafts: no cluster access is needed. Deploys are in the
# deploy-path scenario of k6-users.js.
set -euo pipefail
# EPOCHREALTIME uses the decimal mark of the locale (sv_SE: a comma).
export LC_ALL=C

WORKERS=10
DURATION=120
API_URL="${API_URL:-http://localhost:8081}"
OUTDIR=""
while getopts "w:d:u:o:h" opt; do
  case "$opt" in
    w) WORKERS="$OPTARG" ;;
    d) DURATION="$OPTARG" ;;
    u) API_URL="$OPTARG" ;;
    o) OUTDIR="$OPTARG" ;;
    h) sed -n '2,19p' "$0"; exit 0 ;;
    *) exit 2 ;;
  esac
done

[[ -n "${EPOCHREALTIME:-}" ]] || { echo "ERROR: needs bash 5 or later (EPOCHREALTIME)" >&2; exit 1; }
for tool in stackctl curl jq; do
  command -v "$tool" >/dev/null || { echo "ERROR: $tool is not installed" >&2; exit 1; }
done

ADMIN_USERNAME="${ADMIN_USERNAME:-admin}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin}"
RUN_ID="$(date +%d%H%M%S)"
OUTDIR="${OUTDIR:-loadtest/results/stackctl-$(date +%Y%m%d-%H%M%S)}"
WORK="$(mktemp -d)"
mkdir -p "$OUTDIR"
TIMINGS="$OUTDIR/stackctl-timings.tsv"
printf 'worker\tcommand\tseconds\texit\n' > "$TIMINGS"

USER_IDS=()
DEF_ID=""
ADMIN_TOKEN=""

api() { # api METHOD PATH [JSON] [TOKEN] -> body; fails on no answer or HTTP >= 400
  local method="$1" path="$2" body="${3:-}" token="${4-$ADMIN_TOKEN}"
  local args=(-sS -X "$method" -H "Content-Type: application/json" -w '\n%{http_code}')
  [[ -n "$token" ]] && args+=(-H "Authorization: Bearer $token")
  [[ -n "$body" ]] && args+=(--data "$body")
  local out code
  out="$(curl "${args[@]}" "$API_URL$path")" || { echo "ERROR: $method $path: no answer" >&2; return 1; }
  code="${out##*$'\n'}"
  out="${out%$'\n'*}"
  if [[ "$code" == 000 ]] || (( code >= 400 )); then
    echo "ERROR: $method $path answered HTTP $code: $out" >&2
    return 1
  fi
  printf '%s' "$out"
}

cleanup() {
  set +e
  trap - EXIT INT TERM
  local workers
  workers="$(jobs -p)"
  if [[ -n "$workers" ]]; then
    # shellcheck disable=SC2086 # one PID per word
    kill $workers 2>/dev/null
    wait 2>/dev/null
  fi
  echo "--- Cleanup"
  if [[ -n "$DEF_ID" ]]; then
    api GET "/api/v1/stack-instances?definition_id=$DEF_ID&limit=100" \
      | jq -r '(.data // .items // .instances // .)[]?.id' 2>/dev/null \
      | while read -r id; do api DELETE "/api/v1/stack-instances/$id" >/dev/null; done
  fi
  for id in "${USER_IDS[@]}"; do api DELETE "/api/v1/users/$id" >/dev/null; done
  [[ -n "$DEF_ID" ]] && api DELETE "/api/v1/stack-definitions/$DEF_ID" >/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

echo "--- Setup: $WORKERS users with API keys on $API_URL"
ADMIN_TOKEN="$(api POST /api/v1/auth/login "$(jq -nc --arg u "$ADMIN_USERNAME" --arg p "$ADMIN_PASSWORD" '{username:$u,password:$p}')" | jq -r .token)"
[[ -n "$ADMIN_TOKEN" && "$ADMIN_TOKEN" != null ]] || { echo "ERROR: admin login failed" >&2; exit 1; }

# stack create without --cluster needs a default cluster (as lib/cluster.js).
if ! api GET /api/v1/clusters | jq -e 'any(.[]?; .is_default)' >/dev/null; then
  own="$(api GET /api/v1/clusters | jq -r '.[]? | select(.name == "k6-loadtest") | .id' | head -1)"
  if [[ -n "$own" ]]; then
    api POST "/api/v1/clusters/$own/default" >/dev/null
  else
    api POST /api/v1/clusters '{"name":"k6-loadtest","description":"Created by the load tests. No cluster access outside a pod.","api_server_url":"https://kubernetes.default.svc","use_in_cluster":true,"is_default":true}' >/dev/null
  fi
fi

DEF_ID="$(api POST /api/v1/stack-definitions "$(jq -nc --arg n "sc-load-$RUN_ID" '{name:$n,description:"stackctl load test",default_branch:"main"}')" | jq -r .id)"
api POST "/api/v1/stack-definitions/$DEF_ID/charts" \
  '{"chart_name":"app","repository_url":"https://charts.example.com/app","chart_version":"1.0.0","default_values":"replicaCount: 1"}' >/dev/null

for i in $(seq 1 "$WORKERS"); do
  name="sc-$RUN_ID-$i"
  pass="Load-$RUN_ID-$i-$RANDOM$RANDOM"
  uid="$(api POST /api/v1/auth/register "$(jq -nc --arg u "$name" --arg p "$pass" '{username:$u,password:$p,display_name:$u}')" | jq -r '.id // .user.id // empty')"
  # The new user logs in and creates its own key (the key belongs to the user).
  token="$(api POST /api/v1/auth/login "$(jq -nc --arg u "$name" --arg p "$pass" '{username:$u,password:$p}')" "" | jq -r .token)"
  [[ -n "$uid" ]] || uid="$(api GET /api/v1/auth/me "" "$token" | jq -r '.id // empty')"
  [[ -n "$uid" ]] || { echo "ERROR: no user ID for $name" >&2; exit 1; }
  USER_IDS+=("$uid")
  key="$(api POST "/api/v1/users/$uid/api-keys" '{"name":"stackctl-load","expires_in_days":1}' "$token" | jq -r '.raw_key // empty')"
  [[ -n "$key" ]] || { echo "ERROR: no API key for $name" >&2; exit 1; }
  # The key goes into the worker's own config once, never into a command line
  # of the test cycle.
  STACKCTL_CONFIG_DIR="$WORK/cfg-$i" stackctl config use-context load >/dev/null
  STACKCTL_CONFIG_DIR="$WORK/cfg-$i" stackctl config set api-url "$API_URL" >/dev/null
  STACKCTL_CONFIG_DIR="$WORK/cfg-$i" stackctl config set api-key "$key" >/dev/null
done

timed() { # timed WORKER LABEL cmd... -> one TSV row
  local worker="$1" label="$2"; shift 2
  local start="$EPOCHREALTIME" rc=0
  "$@" >/dev/null 2>>"$WORK/err-$worker.log" || rc=$?
  local end="$EPOCHREALTIME"
  printf '%s\t%s\t%s\t%s\n' "$worker" "$label" "$(awk -v a="$start" -v b="$end" 'BEGIN{printf "%.4f", b-a}')" "$rc" >> "$WORK/t-$worker.tsv"
  return 0
}

worker() {
  local i="$1" deadline="$2" n=0
  export STACKCTL_CONFIG_DIR="$WORK/cfg-$i"
  local sc=(stackctl --no-color)
  while (( EPOCHSECONDS < deadline )); do
    n=$((n + 1))
    local s="sc-$i-$n-$RANDOM"
    timed "$i" "stack list --mine" "${sc[@]}" stack list --mine
    timed "$i" "stack create" "${sc[@]}" stack create --name "$s" --definition "$DEF_ID" --ttl 60
    timed "$i" "stack get" "${sc[@]}" stack get "$s"
    timed "$i" "override set" "${sc[@]}" override set "$s" app --set env.LOADTEST=true --set replicaCount=2
    timed "$i" "stack preview" "${sc[@]}" stack preview "$s"
    timed "$i" "stack extend" "${sc[@]}" stack extend "$s" --minutes 30
    timed "$i" "override unset" "${sc[@]}" override unset "$s" app env.LOADTEST
    timed "$i" "stack delete" "${sc[@]}" stack delete "$s" --yes
  done
}

echo "--- Run: $WORKERS workers for $DURATION s"
deadline=$((EPOCHSECONDS + DURATION))
for i in $(seq 1 "$WORKERS"); do worker "$i" "$deadline" & done
wait

cat "$WORK"/t-*.tsv >> "$TIMINGS" 2>/dev/null || true
cat "$WORK"/err-*.log > "$OUTDIR/stackctl-errors.log" 2>/dev/null || true

echo "--- Summary (seconds, successful commands only) -> $OUTDIR/stackctl-summary.md"
{
  printf '| command | count | errors | p50 | p95 | max |\n|---|---|---|---|---|---|\n'
  tail -n +2 "$TIMINGS" | cut -f2 | sort -u | while read -r cmd; do
    awk -F'\t' -v c="$cmd" '$2==c {print $3, $4}' "$TIMINGS" | sort -n | awk -v c="$cmd" '
      { n++; if ($2 != 0) { e++; next } t[++ok]=$1 }
      END { if (n == 0) exit;
            if (ok == 0) { printf "| %s | %d | %d | - | - | - |\n", c, n, e; exit }
            p50=t[int((ok-1)*0.50)+1]; p95=t[int((ok-1)*0.95)+1];
            printf "| %s | %d | %d | %.3f | %.3f | %.3f |\n", c, n, e+0, p50, p95, t[ok] }'
  done
} | tee "$OUTDIR/stackctl-summary.md"
