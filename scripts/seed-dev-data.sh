#!/usr/bin/env bash
# scripts/seed-dev-data.sh
# Seeds the local dev environment with realistic demo data across all features.
# Requires `make dev` to be running.
#
# Usage: ./scripts/seed-dev-data.sh [API_URL]
#
# The script is safe to run again: it looks up each object by name and reuses
# it. A failed step is reported and listed in the summary, and the script then
# exits with status 1. A step that later steps need (login, cluster, templates,
# definitions, instances) stops the script with the API error.

set -euo pipefail

API="${1:-http://localhost:8081}"
ADMIN_USER="${ADMIN_USERNAME:-admin}"
ADMIN_PASS="${ADMIN_PASSWORD:-admin}"

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

log()     { echo -e "${GREEN}✓${NC} $*"; }
warn()    { echo -e "${YELLOW}!${NC} $*" >&2; }
fail()    { echo -e "${RED}✗${NC} $*" >&2; exit 1; }
section() { echo -e "\n${CYAN}── $* ──${NC}"; }

SEED_TMP=$(mktemp -d)
FAILURES="$SEED_TMP/failures"
: > "$FAILURES"
AUTH=""
SUMMARY_DONE=""

# On an early stop, list the failures that were recorded before it.
on_exit() {
  local rc=$?
  if [ "$rc" -ne 0 ] && [ -z "$SUMMARY_DONE" ] && [ -s "$FAILURES" ]; then
    echo -e "\n${RED}Failed steps before the stop:${NC}" >&2
    sed 's/^/  - /' "$FAILURES" >&2
  fi
  rm -rf "$SEED_TMP"
}
trap on_exit EXIT

# ── HTTP helpers ───────────────────────────────────────────────────────
# api METHOD PATH [BODY] [AUTH_HEADER]
# Prints the response body. The HTTP status goes to $SEED_TMP/status, so the
# status survives a command substitution. Status 000 means no response.
# A 429 answer is retried up to 6 attempts. The delay is the Retry-After value
# or 5, 10, 15, 20, 25 s (the backend sends no Retry-After; the delays cover
# its one-minute window).
api() {
  local method="$1" path="$2" body="${3:-}" auth="${4:-$AUTH}"
  local args=(-sS --max-time 60 -X "$method" "${API}${path}" -o "$SEED_TMP/body" -D "$SEED_TMP/headers" -w '%{http_code}')
  [ -n "$auth" ] && args+=(-H "$auth")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' -d "$body")
  local attempt status wait
  for attempt in 1 2 3 4 5 6; do
    # curl writes the files only when data arrives; never show an old body.
    rm -f "$SEED_TMP/body" "$SEED_TMP/headers"
    status=$(curl "${args[@]}") || status=000
    touch "$SEED_TMP/body" "$SEED_TMP/headers"
    [ "$status" != 429 ] && break
    [ "$attempt" -eq 6 ] && break
    wait=$(grep -i '^retry-after:' "$SEED_TMP/headers" | tr -dc '0-9' || true)
    wait=${wait:-$((attempt * 5))}
    warn "Rate limited on ${method} ${path}: retry in ${wait}s (attempt ${attempt} of 6)"
    sleep "$wait"
  done
  echo "$status" > "$SEED_TMP/status"
  cat "$SEED_TMP/body"
}

last_status() { cat "$SEED_TMP/status"; }

is_ok() {
  local s
  s=$(last_status)
  [ "$s" -ge 200 ] && [ "$s" -lt 300 ]
}

# The error message of the last response (the "error" field, else the body).
api_error() {
  if [ "$(last_status)" = 000 ]; then
    echo "no response from ${API}"
    return
  fi
  python3 - "$SEED_TMP/body" <<'EOF'
import json, sys
text = open(sys.argv[1]).read()
try:
    d = json.loads(text)
except ValueError:
    d = None
if isinstance(d, dict) and d.get("error"):
    print(d["error"])
else:
    print(text[:300] or "(empty body)")
EOF
}

# A field of the last response body, or an empty string.
body_field() {
  python3 - "$SEED_TMP/body" "$1" <<'EOF'
import json, sys
try:
    d = json.load(open(sys.argv[1]))
    v = d.get(sys.argv[2], "") if isinstance(d, dict) else ""
    print(v if v is not None else "")
except ValueError:
    print("")
EOF
}

record_failure() {
  local step="$1"
  echo "${step} (HTTP $(last_status): $(api_error))" >> "$FAILURES"
  warn "${step} failed: HTTP $(last_status): $(api_error)"
}

# check STEP [ALSO_OK_STATUS]
# Logs STEP when the last request succeeded (or returned ALSO_OK_STATUS, for
# example 409 for an object that already exists). Else records a failure.
check() {
  local step="$1" also="${2:-}"
  if is_ok; then
    log "$step"
  elif [ -n "$also" ] && [ "$(last_status)" = "$also" ]; then
    log "${step} (exists)"
  else
    record_failure "$step"
  fi
}

# require_id STEP: prints the "id" of the last response, or stops the script.
require_id() {
  local step="$1" id
  is_ok || fail "${step}: HTTP $(last_status): $(api_error)"
  id=$(body_field id)
  [ -n "$id" ] || fail "${step}: the response has no id: $(api_error)"
  echo "$id"
}

urlenc() { python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1]))' "$1"; }

# find_id PATH NAME [AUTH_HEADER]
# Prints the ID of the item named NAME in a list response (a JSON array or
# {"data": [...]}). Returns 1 when there is no such item, and 2 when the
# lookup fails (the message goes to $SEED_TMP/lookup_error). Use lookup.
find_id() {
  local path="$1" name="$2" auth="${3:-$AUTH}" rc=0
  api GET "$path" "" "$auth" > /dev/null
  if ! is_ok; then
    echo "HTTP $(last_status): $(api_error)" > "$SEED_TMP/lookup_error"
    return 2
  fi
  python3 - "$SEED_TMP/body" "$name" <<'EOF' || rc=$?
import json, sys
d = json.load(open(sys.argv[1]))
items = d.get("data") if isinstance(d, dict) else d
ids = [i["id"] for i in (items or []) if isinstance(i, dict) and i.get("name") == sys.argv[2]]
if not ids:
    sys.exit(3)
print(ids[0])
EOF
  case "$rc" in
    0) return 0 ;;
    3) return 1 ;;
    *) echo "the response is not a list: $(api_error)" > "$SEED_TMP/lookup_error"; return 2 ;;
  esac
}

# lookup VAR PATH NAME [AUTH_HEADER]
# Sets VAR to the ID of the item named NAME. Returns 1 when there is no such
# item. Stops the script when the lookup fails: a create after a failed
# lookup could make a duplicate.
lookup() {
  local lk_id="" lk_rc=0
  lk_id=$(find_id "$2" "$3" "${4:-$AUTH}") || lk_rc=$?
  [ "$lk_rc" -eq 2 ] && fail "Look up '$3' ($2): $(cat "$SEED_TMP/lookup_error")"
  printf -v "$1" '%s' "$lk_id"
  return "$lk_rc"
}

# List helpers for find_id. The name filters of these routes match exactly.
template_path()   { echo "/api/v1/templates?pageSize=1&name=$(urlenc "$1")"; }
definition_path() { echo "/api/v1/stack-definitions?pageSize=1&owner=me&name=$(urlenc "$1")"; }
instance_path()   { echo "/api/v1/stack-instances?pageSize=1&owner=me&name=$(urlenc "$1")"; }

# login_body USERNAME PASSWORD: a JSON login body (safe for any characters).
login_body() { python3 -c 'import json, sys; print(json.dumps({"username": sys.argv[1], "password": sys.argv[2]}))' "$1" "$2"; }

# ── Login ──────────────────────────────────────────────────────────────
echo "Logging in as ${ADMIN_USER}..."
api POST "/api/v1/auth/login" "$(login_body "$ADMIN_USER" "$ADMIN_PASS")" "" > /dev/null
is_ok || fail "Login as ${ADMIN_USER} failed: HTTP $(last_status): $(api_error)"
TOKEN=$(body_field token)
ADMIN_ID=$(python3 -c "import json,sys; print((json.load(open(sys.argv[1])).get('user') or {}).get('id', ''))" "$SEED_TMP/body")
[ -n "$TOKEN" ] && [ -n "$ADMIN_ID" ] || fail "Login as ${ADMIN_USER}: the response has no token or user ID"
log "Logged in (user: ${ADMIN_ID})"

AUTH="Authorization: Bearer ${TOKEN}"

# ══════════════════════════════════════════════════════════════════════
# 0. CLUSTER (instances need one)
# ══════════════════════════════════════════════════════════════════════
section "Checking the cluster"

api GET "/api/v1/clusters" > /dev/null
is_ok || fail "List clusters: HTTP $(last_status): $(api_error)"
CLUSTER_ID=$(python3 -c '
import json, sys
clusters = json.load(open(sys.argv[1])) or []
default = [c for c in clusters if c.get("is_default")]
print((default or clusters or [{"id": ""}])[0]["id"])
' "$SEED_TMP/body")

if [ -n "$CLUSTER_ID" ]; then
  log "Cluster: ${CLUSTER_ID}"
else
  # `make dev` has no Kubernetes access, so no cluster is registered. Register
  # a placeholder so that the stack instances can be created.
  api POST "/api/v1/clusters" '{
    "name": "placeholder-cluster",
    "description": "Registered by make seed. Deploys fail until a real cluster is registered.",
    "api_server_url": "https://kubernetes.default.svc",
    "use_in_cluster": true,
    "is_default": true
  }' > /dev/null
  CLUSTER_ID=$(require_id "Register the placeholder cluster")
  warn "No cluster was registered. Registered 'placeholder-cluster' (${CLUSTER_ID}) as the default."
  warn "Deploys fail on it. Use make dev-k8s for a real cluster, and delete the placeholder under Admin > Clusters."
fi

# ══════════════════════════════════════════════════════════════════════
# 1. USERS
# ══════════════════════════════════════════════════════════════════════
section "Creating test users"

api POST "/api/v1/auth/register" '{
  "username": "developer",
  "password": "developer",
  "display_name": "Test Developer",
  "role": "user"
}' > /dev/null
check "User 'developer' (role: user)" 409

api POST "/api/v1/auth/register" '{
  "username": "devops",
  "password": "devops",
  "display_name": "Test DevOps Engineer",
  "role": "devops"
}' > /dev/null
check "User 'devops' (role: devops)" 409

api POST "/api/v1/auth/register" '{
  "username": "viewer",
  "password": "viewer",
  "display_name": "Test Viewer",
  "role": "user"
}' > /dev/null
check "User 'viewer' (role: user)" 409

# ══════════════════════════════════════════════════════════════════════
# 2. TEMPLATES (5 categories, each with realistic Helm charts)
# ══════════════════════════════════════════════════════════════════════
section "Creating templates"

# load_charts TEMPLATE_ID: stores the chart names of the template working
# copy, so that add_chart adds only the missing charts (a rerun after a
# partial run completes the template).
load_charts() {
  api GET "/api/v1/templates/$1" > /dev/null
  is_ok || fail "Read template $1: HTTP $(last_status): $(api_error)"
  python3 -c '
import json, sys
for c in json.load(open(sys.argv[1])).get("charts") or []:
    print(c.get("chart_name", ""))
' "$SEED_TMP/body" > "$SEED_TMP/charts-$1"
}

# add_chart TEMPLATE_ID CHART_NAME BODY
add_chart() {
  if grep -qx "$2" "$SEED_TMP/charts-$1"; then
    return
  fi
  api POST "/api/v1/templates/$1/charts" "$3" > /dev/null
  check "  + chart $2"
}

# ── Web Category ─────────────────────────────────────────────────────
if lookup WEB_ID "$(template_path 'Full-Stack Web App')" 'Full-Stack Web App'; then
  log "Web template exists: ${WEB_ID}"
else
  api POST "/api/v1/templates" '{
    "name": "Full-Stack Web App",
    "description": "NGINX ingress + backend API + frontend. Good for typical web applications.",
    "category": "Web",
    "version": "1.0.0",
    "default_branch": "main"
  }' > /dev/null
  WEB_ID=$(require_id "Create the Web template")
  log "Web template: ${WEB_ID}"
fi
load_charts "$WEB_ID"

add_chart "$WEB_ID" backend-api '{
  "chart_name": "backend-api",
  "repository_url": "https://charts.bitnami.com/bitnami",
  "source_repo_url": "https://dev.azure.com/org/project/_git/backend",
  "chart_path": "node",
  "chart_version": "19.1.7",
  "deploy_order": 1,
  "required": true,
  "default_values": "replicaCount: 1\nimage:\n  tag: \"{{.Branch}}\"\nservice:\n  type: ClusterIP\n  port: 3000\nresources:\n  requests:\n    cpu: 100m\n    memory: 128Mi",
  "locked_values": ""
}'

add_chart "$WEB_ID" postgresql '{
  "chart_name": "postgresql",
  "repository_url": "https://charts.bitnami.com/bitnami",
  "chart_path": "postgresql",
  "chart_version": "16.4.6",
  "deploy_order": 2,
  "required": true,
  "default_values": "auth:\n  postgresPassword: changeme\n  database: app\nprimary:\n  persistence:\n    size: 1Gi\n  resources:\n    requests:\n      cpu: 100m\n      memory: 256Mi",
  "locked_values": ""
}'

add_chart "$WEB_ID" frontend-app '{
  "chart_name": "frontend-app",
  "repository_url": "https://charts.bitnami.com/bitnami",
  "source_repo_url": "https://dev.azure.com/org/project/_git/frontend",
  "chart_path": "nginx",
  "chart_version": "22.6.5",
  "deploy_order": 3,
  "required": true,
  "default_values": "replicaCount: 1\nservice:\n  type: ClusterIP\n  port: 8080",
  "locked_values": ""
}'

# ── API Category ─────────────────────────────────────────────────────
if lookup API_ID "$(template_path 'REST API Service')" 'REST API Service'; then
  log "API template exists: ${API_ID}"
else
  api POST "/api/v1/templates" '{
    "name": "REST API Service",
    "description": "A standalone API service with Redis caching. Good for microservices.",
    "category": "API",
    "version": "1.0.0",
    "default_branch": "main"
  }' > /dev/null
  API_ID=$(require_id "Create the API template")
  log "API template: ${API_ID}"
fi
load_charts "$API_ID"

add_chart "$API_ID" api-service '{
  "chart_name": "api-service",
  "repository_url": "https://charts.bitnami.com/bitnami",
  "source_repo_url": "https://dev.azure.com/org/project/_git/api-service",
  "chart_path": "node",
  "chart_version": "19.1.7",
  "deploy_order": 1,
  "required": true,
  "default_values": "replicaCount: 2\nimage:\n  tag: \"{{.Branch}}\"\nservice:\n  type: ClusterIP\n  port: 8080\nresources:\n  requests:\n    cpu: 100m\n    memory: 128Mi\n  limits:\n    cpu: 500m\n    memory: 512Mi",
  "locked_values": "containerSecurityContext:\n  runAsNonRoot: true"
}'

add_chart "$API_ID" redis '{
  "chart_name": "redis",
  "repository_url": "https://charts.bitnami.com/bitnami",
  "chart_path": "redis",
  "chart_version": "20.3.0",
  "deploy_order": 2,
  "required": false,
  "default_values": "architecture: standalone\nauth:\n  enabled: false\nmaster:\n  persistence:\n    size: 1Gi",
  "locked_values": ""
}'

# ── Data Category ────────────────────────────────────────────────────
if lookup DATA_ID "$(template_path 'Data Pipeline')" 'Data Pipeline'; then
  log "Data template exists: ${DATA_ID}"
else
  api POST "/api/v1/templates" '{
    "name": "Data Pipeline",
    "description": "CloudNativePG PostgreSQL + Redis + a worker service for data processing.",
    "category": "Data",
    "version": "1.0.0",
    "default_branch": "main"
  }' > /dev/null
  DATA_ID=$(require_id "Create the Data template")
  log "Data template: ${DATA_ID}"
fi
load_charts "$DATA_ID"

add_chart "$DATA_ID" cnpg-cluster '{
  "chart_name": "cnpg-cluster",
  "repository_url": "https://cloudnative-pg.github.io/charts",
  "chart_path": "cluster",
  "chart_version": "0.2.1",
  "deploy_order": 1,
  "required": true,
  "default_values": "name: app-db\ninstances: 1\nstorage:\n  size: 5Gi",
  "locked_values": ""
}'

add_chart "$DATA_ID" redis '{
  "chart_name": "redis",
  "repository_url": "https://charts.bitnami.com/bitnami",
  "chart_path": "redis",
  "chart_version": "20.3.0",
  "deploy_order": 2,
  "required": true,
  "default_values": "architecture: standalone\nauth:\n  enabled: false",
  "locked_values": ""
}'

add_chart "$DATA_ID" worker '{
  "chart_name": "worker",
  "repository_url": "https://charts.bitnami.com/bitnami",
  "source_repo_url": "https://dev.azure.com/org/project/_git/data-worker",
  "chart_path": "node",
  "chart_version": "19.1.7",
  "deploy_order": 3,
  "required": true,
  "default_values": "replicaCount: 1\nimage:\n  tag: \"{{.Branch}}\"\ncommand:\n  - node\n  - worker.js",
  "locked_values": ""
}'

# ── Infrastructure Category ──────────────────────────────────────────
if lookup INFRA_ID "$(template_path 'Observability Stack')" 'Observability Stack'; then
  log "Infrastructure template exists: ${INFRA_ID}"
else
  api POST "/api/v1/templates" '{
    "name": "Observability Stack",
    "description": "Grafana + Loki for logging and dashboards. Namespace-safe, no cluster-scoped resources.",
    "category": "Infrastructure",
    "version": "1.0.0",
    "default_branch": "main"
  }' > /dev/null
  INFRA_ID=$(require_id "Create the Infrastructure template")
  log "Infrastructure template: ${INFRA_ID}"
fi
load_charts "$INFRA_ID"

add_chart "$INFRA_ID" grafana '{
  "chart_name": "grafana",
  "repository_url": "https://grafana.github.io/helm-charts",
  "chart_path": "grafana",
  "chart_version": "8.12.1",
  "deploy_order": 1,
  "required": true,
  "default_values": "adminUser: admin\nadminPassword: admin\npersistence:\n  enabled: false\nservice:\n  type: ClusterIP\n  port: 3000",
  "locked_values": ""
}'

add_chart "$INFRA_ID" loki '{
  "chart_name": "loki",
  "repository_url": "https://grafana.github.io/helm-charts",
  "chart_path": "loki",
  "chart_version": "6.24.1",
  "deploy_order": 2,
  "required": false,
  "default_values": "deploymentMode: SingleBinary\nloki:\n  auth_enabled: false\n  storage:\n    type: filesystem\nsingleBinary:\n  replicas: 1\n  persistence:\n    size: 5Gi",
  "locked_values": ""
}'

# ── Other Category ───────────────────────────────────────────────────
if lookup OTHER_ID "$(template_path 'Development Tools')" 'Development Tools'; then
  log "Other template exists: ${OTHER_ID}"
else
  api POST "/api/v1/templates" '{
    "name": "Development Tools",
    "description": "Useful dev tools: MinIO for S3-compatible storage, Mailpit for email testing.",
    "category": "Other",
    "version": "1.0.0",
    "default_branch": "main"
  }' > /dev/null
  OTHER_ID=$(require_id "Create the Other template")
  log "Other template: ${OTHER_ID}"
fi
load_charts "$OTHER_ID"

add_chart "$OTHER_ID" minio '{
  "chart_name": "minio",
  "repository_url": "https://charts.min.io",
  "chart_path": "minio",
  "chart_version": "5.3.0",
  "deploy_order": 1,
  "required": true,
  "default_values": "mode: standalone\nrootUser: minioadmin\nrootPassword: minioadmin\npersistence:\n  size: 5Gi\nresources:\n  requests:\n    memory: 512Mi",
  "locked_values": ""
}'

add_chart "$OTHER_ID" mailpit '{
  "chart_name": "mailpit",
  "repository_url": "https://charts.johansmitsnl.github.io/mailpit",
  "chart_path": "mailpit",
  "chart_version": "0.19.0",
  "deploy_order": 2,
  "required": false,
  "default_values": "service:\n  type: ClusterIP\n  port: 8025\n  smtpPort: 1025",
  "locked_values": ""
}'

# ══════════════════════════════════════════════════════════════════════
# 3. PUBLISH TEMPLATES (creates version snapshots automatically)
# ══════════════════════════════════════════════════════════════════════
section "Publishing templates (creates version 1.0.0 snapshots)"

# Publish every seed template. A publish of an unchanged working copy creates
# no snapshot, so a rerun is safe and completes a partial run.
for TID in "$WEB_ID" "$API_ID" "$DATA_ID" "$INFRA_ID" "$OTHER_ID"; do
  api POST "/api/v1/templates/${TID}/publish" > /dev/null
  check "Published template ${TID}" 409
done

# Bump and re-publish the Web template to create a v1.1.0 version for version diff demo
api GET "/api/v1/templates/${WEB_ID}" > /dev/null
if is_ok && [ "$(body_field version)" = "1.0.0" ]; then
  api PUT "/api/v1/templates/${WEB_ID}" '{
    "name": "Full-Stack Web App",
    "description": "NGINX ingress + backend API + frontend. Now with improved defaults.",
    "category": "Web",
    "version": "1.1.0",
    "default_branch": "main"
  }' > /dev/null
  check "Web template working copy set to v1.1.0"
  api POST "/api/v1/templates/${WEB_ID}/publish" > /dev/null
  check "Web template published as v1.1.0 (2 versions for diff demo)"
else
  log "Web template is already at v1.1.0"
fi

# ══════════════════════════════════════════════════════════════════════
# 4. STACK DEFINITIONS + INSTANCES
# ══════════════════════════════════════════════════════════════════════
section "Creating stack definitions and instances"

# ensure_definition TEMPLATE_ID NAME DESCRIPTION: prints the definition ID.
ensure_definition() {
  local id
  if lookup id "$(definition_path "$2")" "$2"; then
    echo "$id"
    return
  fi
  api POST "/api/v1/templates/$1/instantiate" "{\"name\": \"$2\", \"description\": \"$3\"}" > /dev/null
  require_id "Create definition $2 from template $1"
}

# ensure_instance NAME DEFINITION_ID BRANCH TTL_MINUTES [AUTH_HEADER]: prints the instance ID.
ensure_instance() {
  local id auth="${5:-$AUTH}"
  if lookup id "$(instance_path "$1")" "$1" "$auth"; then
    echo "$id"
    return
  fi
  api POST "/api/v1/stack-instances" "{
    \"name\": \"$1\",
    \"stack_definition_id\": \"$2\",
    \"cluster_id\": \"${CLUSTER_ID}\",
    \"branch\": \"$3\",
    \"ttl_minutes\": $4
  }" "$auth" > /dev/null
  require_id "Create instance $1"
}

DEF_ID=$(ensure_definition "$WEB_ID" "my-web-app" "Test web application stack for local development")
log "Stack definition my-web-app: ${DEF_ID} (from Web template)"

# Several instances for the comparison and bulk operation demos
INST1_ID=$(ensure_instance "web-dev" "$DEF_ID" "main" 480)
log "Instance web-dev: ${INST1_ID} (branch: main, TTL: 8h)"

INST2_ID=$(ensure_instance "web-feature" "$DEF_ID" "feature/auth" 0)
log "Instance web-feature: ${INST2_ID} (branch: feature/auth, no TTL)"

API_DEF_ID=$(ensure_definition "$API_ID" "api-svc" "Microservice API with Redis cache")
log "Stack definition api-svc: ${API_DEF_ID} (from API template)"

INST3_ID=$(ensure_instance "api-staging" "$API_DEF_ID" "develop" 1440)
log "Instance api-staging: ${INST3_ID} (branch: develop, TTL: 24h)"

# ══════════════════════════════════════════════════════════════════════
# 5. VALUE OVERRIDES (per-chart customization)
# ══════════════════════════════════════════════════════════════════════
section "Setting value overrides"

# Get chart configs for the web definition to find chart IDs
api GET "/api/v1/stack-definitions/${DEF_ID}" > /dev/null
FIRST_CHART_ID=""
if is_ok; then
  FIRST_CHART_ID=$(python3 -c "
import json, sys
charts = json.load(open(sys.argv[1])).get('charts') or []
print(charts[0]['id'] if charts else '')
" "$SEED_TMP/body")
fi

if [ -n "$FIRST_CHART_ID" ]; then
  api PUT "/api/v1/stack-instances/${INST1_ID}/overrides/${FIRST_CHART_ID}" '{
    "values": "replicaCount: 3\nresources:\n  requests:\n    cpu: 200m\n    memory: 256Mi"
  }' > /dev/null
  check "Override set on web-dev, chart ${FIRST_CHART_ID}"
else
  record_failure "Find a chart of definition my-web-app for the overrides"
fi

# ══════════════════════════════════════════════════════════════════════
# 6. BRANCH OVERRIDES (per-chart branch customization)
# ══════════════════════════════════════════════════════════════════════
section "Setting branch overrides"

if [ -n "$FIRST_CHART_ID" ]; then
  api PUT "/api/v1/stack-instances/${INST2_ID}/branches/${FIRST_CHART_ID}" '{
    "branch": "feature/new-ui"
  }' > /dev/null
  check "Branch override on web-feature, chart ${FIRST_CHART_ID} → feature/new-ui"
else
  warn "No chart ID: skipped the branch override"
fi

# ══════════════════════════════════════════════════════════════════════
# 7. FAVORITES (user bookmarks)
# ══════════════════════════════════════════════════════════════════════
section "Adding favorites"

# add_favorite ENTITY_TYPE ENTITY_ID LABEL [AUTH_HEADER] (entity types: definition, instance, template)
add_favorite() {
  api POST "/api/v1/favorites" "{\"entity_type\": \"$1\", \"entity_id\": \"$2\"}" "${4:-$AUTH}" > /dev/null
  check "Favorited $3" 409
}

add_favorite template "$WEB_ID" "Web template"
add_favorite instance "$INST1_ID" "instance web-dev"
add_favorite template "$DATA_ID" "Data Pipeline template"

# ══════════════════════════════════════════════════════════════════════
# 8. CLEANUP POLICIES (cron-based automation)
# ══════════════════════════════════════════════════════════════════════
section "Creating cleanup policies"

# ensure_policy NAME BODY LABEL
ensure_policy() {
  if lookup _seen "/api/v1/admin/cleanup-policies" "$1"; then
    log "$3 (exists)"
    return
  fi
  api POST "/api/v1/admin/cleanup-policies" "$2" > /dev/null
  check "$3"
}

ensure_policy "Stop idle stacks (7 days)" '{
  "name": "Stop idle stacks (7 days)",
  "action": "stop",
  "condition": "idle_days:7",
  "schedule": "0 2 * * *",
  "cluster_id": "all",
  "enabled": true,
  "dry_run": false
}' "Policy: Stop idle stacks (7 days), daily at 02:00"

ensure_policy "Delete stopped stacks (14 days)" '{
  "name": "Delete stopped stacks (14 days)",
  "action": "delete",
  "condition": "status:stopped,age_days:14",
  "schedule": "0 3 * * 0",
  "cluster_id": "all",
  "enabled": true,
  "dry_run": false
}' "Policy: Delete stopped stacks (14 days), weekly on Sunday at 03:00"

ensure_policy "Clean expired TTL stacks (dry run)" '{
  "name": "Clean expired TTL stacks (dry run)",
  "action": "clean",
  "condition": "ttl_expired",
  "schedule": "0 4 * * *",
  "cluster_id": "all",
  "enabled": true,
  "dry_run": true
}' "Policy: Clean expired TTL (dry run), daily at 04:00"

# ══════════════════════════════════════════════════════════════════════
# 9. CLUSTER SHARED VALUES + RESOURCE QUOTAS
# ══════════════════════════════════════════════════════════════════════
section "Configuring cluster resources"

# ensure_shared_values NAME BODY LABEL
ensure_shared_values() {
  if lookup _seen "/api/v1/clusters/${CLUSTER_ID}/shared-values" "$1"; then
    log "$3 (exists)"
    return
  fi
  api POST "/api/v1/clusters/${CLUSTER_ID}/shared-values" "$2" > /dev/null
  check "$3"
}

# Shared values — applied to all instances in the cluster
ensure_shared_values "Global Labels" '{
  "name": "Global Labels",
  "values": "global:\n  labels:\n    managed-by: k8s-stack-manager\n    environment: development",
  "priority": 1
}' "Shared values: Global Labels (priority 1)"

ensure_shared_values "Resource Defaults" '{
  "name": "Resource Defaults",
  "values": "resources:\n  requests:\n    cpu: 50m\n    memory: 64Mi",
  "priority": 10
}' "Shared values: Resource Defaults (priority 10)"

# Resource quotas — per-namespace limits. A quota save writes to every stack
# namespace of the cluster, so keep an existing quota.
api GET "/api/v1/clusters/${CLUSTER_ID}/quotas" > /dev/null
if is_ok && [ -n "$(body_field id)" ]; then
  log "Resource quotas exist on the cluster (kept)"
else
  api PUT "/api/v1/clusters/${CLUSTER_ID}/quotas" '{
    "cpu_request": "4",
    "cpu_limit": "8",
    "memory_request": "4Gi",
    "memory_limit": "8Gi",
    "storage_limit": "50Gi",
    "pod_limit": 50
  }' > /dev/null
  check "Resource quotas: 8 CPU / 8Gi memory / 50 pods per namespace"
fi

# ══════════════════════════════════════════════════════════════════════
# 10. NOTIFICATIONS
# ══════════════════════════════════════════════════════════════════════
section "Notifications"
log "Notifications are generated automatically on deploy/stop/clean events"
log "(Deploy instances via the UI to see notifications appear)"

# ══════════════════════════════════════════════════════════════════════
# 11. DEFINITION EXPORT/IMPORT DEMO
# ══════════════════════════════════════════════════════════════════════
section "Import/export demo"

if lookup IMPORT_ID "$(definition_path imported-svc)" imported-svc; then
  log "Imported definition exists: ${IMPORT_ID}"
else
  api POST "/api/v1/stack-definitions/import" '{
    "schema_version": "1.0",
    "definition": {
      "name": "imported-svc",
      "description": "Imported via seed script to demo import/export",
      "default_branch": "main"
    },
    "charts": [
      {
        "chart_name": "backend",
        "repository_url": "https://charts.bitnami.com/bitnami",
        "chart_path": "node",
        "chart_version": "19.1.7",
        "deploy_order": 1,
        "default_values": "replicaCount: 1\nimage:\n  tag: latest"
      },
      {
        "chart_name": "database",
        "repository_url": "https://charts.bitnami.com/bitnami",
        "chart_path": "postgresql",
        "chart_version": "16.4.6",
        "deploy_order": 2,
        "default_values": "auth:\n  postgresPassword: changeme\nprimary:\n  persistence:\n    size: 5Gi"
      }
    ]
  }' > /dev/null
  check "Imported definition imported-svc (2 charts: backend, database)"
fi

# ══════════════════════════════════════════════════════════════════════
# 12. API KEYS (for Profile page demo)
# ══════════════════════════════════════════════════════════════════════
section "Creating API keys"

# ensure_api_key NAME BODY LABEL
ensure_api_key() {
  if lookup _seen "/api/v1/users/${ADMIN_ID}/api-keys" "$1"; then
    log "$3 (exists)"
    return
  fi
  api POST "/api/v1/users/${ADMIN_ID}/api-keys" "$2" > /dev/null
  check "$3"
}

ensure_api_key ci-pipeline '{"name": "ci-pipeline", "expires_in_days": 90}' "API key 'ci-pipeline' (expires in 90 days)"
ensure_api_key local-dev '{"name": "local-dev", "expires_in_days": 365}' "API key 'local-dev' (expires in 365 days)"

# ══════════════════════════════════════════════════════════════════════
# 13. INSTANCE QUOTA OVERRIDES
# ══════════════════════════════════════════════════════════════════════
section "Setting instance quota overrides"

api PUT "/api/v1/stack-instances/${INST1_ID}/quota-overrides" '{
  "cpu_request": "500m",
  "cpu_limit": "2000m",
  "memory_request": "256Mi",
  "memory_limit": "1Gi",
  "storage_limit": "10Gi",
  "pod_limit": 20
}' > /dev/null
check "Quota override on web-dev: 2 CPU / 1Gi mem / 20 pods"

# ══════════════════════════════════════════════════════════════════════
# 14. NOTIFICATION PREFERENCES
# ══════════════════════════════════════════════════════════════════════
section "Setting notification preferences"

api PUT "/api/v1/notifications/preferences" '[
  {"event_type": "deployment.success", "enabled": true},
  {"event_type": "deployment.stopped", "enabled": true},
  {"event_type": "clean.completed", "enabled": false}
]' > /dev/null
check "Admin: deployment.success=on, deployment.stopped=on, clean.completed=off"

# ══════════════════════════════════════════════════════════════════════
# 15. TEMPLATE + INSTANCE CLONE
# ══════════════════════════════════════════════════════════════════════
section "Cloning template and instance"

if lookup CLONE_TMPL_ID "$(template_path 'REST API Service (Copy)')" 'REST API Service (Copy)'; then
  log "Cloned API template exists: ${CLONE_TMPL_ID}"
else
  api POST "/api/v1/templates/${API_ID}/clone" '{"name": "REST API Service (Copy)"}' > /dev/null
  check "Cloned API template as 'REST API Service (Copy)' (unpublished draft)"
fi

if lookup CLONE_INST_ID "$(instance_path web-dev-copy)" web-dev-copy; then
  log "Cloned instance exists: ${CLONE_INST_ID}"
else
  api POST "/api/v1/stack-instances/${INST1_ID}/clone" '{"name": "web-dev-copy"}' > /dev/null
  check "Cloned web-dev as web-dev-copy (draft status)"
fi

# ══════════════════════════════════════════════════════════════════════
# 16. MULTI-USER DATA (developer creates own instance + favorite)
# ══════════════════════════════════════════════════════════════════════
section "Creating developer-owned data"

api POST "/api/v1/auth/login" '{"username":"developer","password":"developer"}' "" > /dev/null
if is_ok; then
  DEV_AUTH="Authorization: Bearer $(body_field token)"

  # Developer creates own instance
  DEV_INST_ID=$(ensure_instance "dev-sandbox" "$DEF_ID" "feature/my-work" 240 "$DEV_AUTH")
  log "Developer instance dev-sandbox: ${DEV_INST_ID} (branch: feature/my-work, TTL: 4h)"

  # Developer favorites their own instance
  add_favorite instance "$DEV_INST_ID" "dev-sandbox (developer)" "$DEV_AUTH"

  # Developer sets notification preferences
  api PUT "/api/v1/notifications/preferences" '[
    {"event_type": "deployment.success", "enabled": true},
    {"event_type": "deployment.stopped", "enabled": false},
    {"event_type": "clean.completed", "enabled": false}
  ]' "$DEV_AUTH" > /dev/null
  check "Developer: deployment.success=on, deployment.stopped=off, clean.completed=off"
else
  record_failure "Log in as developer (developer-owned data skipped)"
fi

# ══════════════════════════════════════════════════════════════════════
# SUMMARY (counts come from the API, not from this script)
# ══════════════════════════════════════════════════════════════════════
# count PATH: the number of items in a list response, or "?".
count() {
  api GET "$1" > /dev/null
  if ! is_ok; then
    echo "?"
    return
  fi
  python3 -c "
import json, sys
d = json.load(open(sys.argv[1]))
print(d.get('total', len(d.get('data') or [])) if isinstance(d, dict) else len(d or []))
" "$SEED_TMP/body"
}

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "Data in ${API} (all owners):"
echo "  Templates:          $(count '/api/v1/templates?pageSize=1')"
echo "  Stack definitions:  $(count '/api/v1/stack-definitions?pageSize=1')"
echo "  Stack instances:    $(count '/api/v1/stack-instances?pageSize=1')"
echo "  Cleanup policies:   $(count '/api/v1/admin/cleanup-policies')"
echo "  Shared values:      $(count "/api/v1/clusters/${CLUSTER_ID}/shared-values") (cluster ${CLUSTER_ID})"
echo "  Admin API keys:     $(count "/api/v1/users/${ADMIN_ID}/api-keys")"
echo "  Admin favorites:    $(count '/api/v1/favorites')"
echo ""
echo "Seed objects: templates Full-Stack Web App, REST API Service, Data Pipeline,"
echo "  Observability Stack, Development Tools, REST API Service (Copy);"
echo "  definitions my-web-app, api-svc, imported-svc;"
echo "  instances web-dev, web-feature, api-staging, web-dev-copy, dev-sandbox (developer)."
echo ""
echo "Users:"
echo "  admin/admin         (admin)"
echo "  devops/devops       (devops)"
echo "  developer/developer (user)   — owns dev-sandbox"
echo "  viewer/viewer       (user)"
echo ""
echo "Try:"
echo "  - Template version diff: Templates → Full-Stack Web App → Version History"
echo "  - Instance comparison: Stack Instances → Compare"
echo "  - Bulk operations: Stack Instances → select multiple → Bulk Actions"
echo "  - Import/Export: Stack Definitions → Import / Edit → Export"
echo "  - Notifications: deploy an instance to see notifications appear"
echo "  - Profile: view API keys under Profile page"
echo "  - Clone: cloned template appears as unpublished draft"
echo "  - Multi-user: switch to developer/developer to see their data"
echo "═══════════════════════════════════════════════════════════════"

SUMMARY_DONE=1
if [ -s "$FAILURES" ]; then
  echo ""
  echo -e "${RED}Seed finished with $(wc -l < "$FAILURES" | tr -d ' ') failed step(s):${NC}"
  sed 's/^/  - /' "$FAILURES"
  exit 1
fi
echo -e "${GREEN}Seed finished without failures.${NC}"
