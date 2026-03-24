#!/usr/bin/env bash
# scripts/seed-dev-data.sh
# Seeds the local dev environment with realistic demo data across all features.
# Requires `make dev` to be running.
#
# Usage: ./scripts/seed-dev-data.sh [API_URL]

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
warn()    { echo -e "${YELLOW}!${NC} $*"; }
fail()    { echo -e "${RED}✗${NC} $*" >&2; exit 1; }
section() { echo -e "\n${CYAN}── $* ──${NC}"; }

# ── Login ──────────────────────────────────────────────────────────────
echo "Logging in as ${ADMIN_USER}..."
LOGIN_RESP=$(curl -s -X POST "${API}/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}")

TOKEN=$(echo "$LOGIN_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))" 2>/dev/null || true)
[ -z "$TOKEN" ] && fail "Login failed: ${LOGIN_RESP}"
log "Logged in — token obtained"

AUTH="Authorization: Bearer ${TOKEN}"

# Unique suffix so the script is safe to run multiple times
RUN_ID=$(date +%Y%m%d-%H%M%S)

# Helper: POST JSON and return the response
api_post() {
  local path="$1" body="$2"
  curl -s -X POST "${API}${path}" -H 'Content-Type: application/json' -H "$AUTH" -d "$body"
}

# Helper: PUT JSON and return the response
api_put() {
  local path="$1" body="$2"
  curl -s -X PUT "${API}${path}" -H 'Content-Type: application/json' -H "$AUTH" -d "$body"
}

# Helper: GET and return the response
api_get() {
  local path="$1"
  curl -s -X GET "${API}${path}" -H "$AUTH"
}

# Helper: extract .id from JSON
get_id() { python3 -c "import sys,json; print(json.load(sys.stdin)['id'])"; }

# Helper: extract .definition.id from instantiate response
get_def_id() { python3 -c "import sys,json; print(json.load(sys.stdin)['definition']['id'])"; }

# ══════════════════════════════════════════════════════════════════════
# 1. USERS
# ══════════════════════════════════════════════════════════════════════
section "Creating test users"

api_post "/api/v1/auth/register" '{
  "username": "developer",
  "password": "developer",
  "display_name": "Test Developer",
  "role": "user"
}' > /dev/null 2>&1 || true
log "User 'developer' (role: user)"

api_post "/api/v1/auth/register" '{
  "username": "devops",
  "password": "devops",
  "display_name": "Test DevOps Engineer",
  "role": "devops"
}' > /dev/null 2>&1 || true
log "User 'devops' (role: devops)"

api_post "/api/v1/auth/register" '{
  "username": "viewer",
  "password": "viewer",
  "display_name": "Test Viewer",
  "role": "user"
}' > /dev/null 2>&1 || true
log "User 'viewer' (role: user)"

# ══════════════════════════════════════════════════════════════════════
# 2. TEMPLATES (5 categories, each with realistic Helm charts)
# ══════════════════════════════════════════════════════════════════════
section "Creating templates"

# ── Web Category ─────────────────────────────────────────────────────
WEB_ID=$(api_post "/api/v1/templates" '{
  "name": "Full-Stack Web App",
  "description": "NGINX ingress + backend API + frontend. Good for typical web applications.",
  "category": "Web",
  "version": "1.0.0",
  "default_branch": "main"
}' | get_id)
log "Web template: ${WEB_ID}"

api_post "/api/v1/templates/${WEB_ID}/charts" '{
  "chart_name": "backend-api",
  "repository_url": "https://charts.bitnami.com/bitnami",
  "source_repo_url": "https://dev.azure.com/org/project/_git/backend",
  "chart_path": "node",
  "chart_version": "19.1.7",
  "deploy_order": 1,
  "required": true,
  "default_values": "replicaCount: 1\nimage:\n  tag: \"{{.Branch}}\"\nservice:\n  type: ClusterIP\n  port: 3000\nresources:\n  requests:\n    cpu: 100m\n    memory: 128Mi",
  "locked_values": ""
}' > /dev/null

api_post "/api/v1/templates/${WEB_ID}/charts" '{
  "chart_name": "azurite-storage",
  "repository_url": "https://emberstack.github.io/helm-charts",
  "chart_path": "azurite",
  "chart_version": "1.0.19",
  "deploy_order": 2,
  "required": true,
  "default_values": "persistence:\n  enabled: false\nservice:\n  type: ClusterIP",
  "locked_values": ""
}' > /dev/null

api_post "/api/v1/templates/${WEB_ID}/charts" '{
  "chart_name": "frontend-app",
  "repository_url": "https://charts.bitnami.com/bitnami",
  "source_repo_url": "https://dev.azure.com/org/project/_git/frontend",
  "chart_path": "nginx",
  "chart_version": "22.6.5",
  "deploy_order": 3,
  "required": true,
  "default_values": "replicaCount: 1\nservice:\n  type: ClusterIP\n  port: 8080",
  "locked_values": ""
}' > /dev/null
log "  + 3 charts (backend-api, azurite-storage, frontend-app)"

# ── API Category ─────────────────────────────────────────────────────
API_ID=$(api_post "/api/v1/templates" '{
  "name": "REST API Service",
  "description": "A standalone API service with Redis caching. Good for microservices.",
  "category": "API",
  "version": "1.0.0",
  "default_branch": "main"
}' | get_id)
log "API template: ${API_ID}"

api_post "/api/v1/templates/${API_ID}/charts" '{
  "chart_name": "api-service",
  "repository_url": "https://charts.bitnami.com/bitnami",
  "source_repo_url": "https://dev.azure.com/org/project/_git/api-service",
  "chart_path": "node",
  "chart_version": "19.1.7",
  "deploy_order": 1,
  "required": true,
  "default_values": "replicaCount: 2\nimage:\n  tag: \"{{.Branch}}\"\nservice:\n  type: ClusterIP\n  port: 8080\nresources:\n  requests:\n    cpu: 100m\n    memory: 128Mi\n  limits:\n    cpu: 500m\n    memory: 512Mi",
  "locked_values": "containerSecurityContext:\n  runAsNonRoot: true"
}' > /dev/null

api_post "/api/v1/templates/${API_ID}/charts" '{
  "chart_name": "redis",
  "repository_url": "https://charts.bitnami.com/bitnami",
  "chart_path": "redis",
  "chart_version": "20.3.0",
  "deploy_order": 2,
  "required": false,
  "default_values": "architecture: standalone\nauth:\n  enabled: false\nmaster:\n  persistence:\n    size: 1Gi",
  "locked_values": ""
}' > /dev/null
log "  + 2 charts (api-service, redis)"

# ── Data Category ────────────────────────────────────────────────────
DATA_ID=$(api_post "/api/v1/templates" '{
  "name": "Data Pipeline",
  "description": "CloudNativePG PostgreSQL + Redis + a worker service for data processing.",
  "category": "Data",
  "version": "1.0.0",
  "default_branch": "main"
}' | get_id)
log "Data template: ${DATA_ID}"

api_post "/api/v1/templates/${DATA_ID}/charts" '{
  "chart_name": "cnpg-cluster",
  "repository_url": "https://cloudnative-pg.github.io/charts",
  "chart_path": "cluster",
  "chart_version": "0.2.1",
  "deploy_order": 1,
  "required": true,
  "default_values": "name: app-db\ninstances: 1\nstorage:\n  size: 5Gi",
  "locked_values": ""
}' > /dev/null

api_post "/api/v1/templates/${DATA_ID}/charts" '{
  "chart_name": "redis",
  "repository_url": "https://charts.bitnami.com/bitnami",
  "chart_path": "redis",
  "chart_version": "20.3.0",
  "deploy_order": 2,
  "required": true,
  "default_values": "architecture: standalone\nauth:\n  enabled: false",
  "locked_values": ""
}' > /dev/null

api_post "/api/v1/templates/${DATA_ID}/charts" '{
  "chart_name": "worker",
  "repository_url": "https://charts.bitnami.com/bitnami",
  "source_repo_url": "https://dev.azure.com/org/project/_git/data-worker",
  "chart_path": "node",
  "chart_version": "19.1.7",
  "deploy_order": 3,
  "required": true,
  "default_values": "replicaCount: 1\nimage:\n  tag: \"{{.Branch}}\"\ncommand:\n  - node\n  - worker.js",
  "locked_values": ""
}' > /dev/null
log "  + 3 charts (cnpg-cluster, redis, worker)"

# ── Infrastructure Category ──────────────────────────────────────────
INFRA_ID=$(api_post "/api/v1/templates" '{
  "name": "Observability Stack",
  "description": "Grafana + Loki for logging and dashboards. Namespace-safe, no cluster-scoped resources.",
  "category": "Infrastructure",
  "version": "1.0.0",
  "default_branch": "main"
}' | get_id)
log "Infrastructure template: ${INFRA_ID}"

api_post "/api/v1/templates/${INFRA_ID}/charts" '{
  "chart_name": "grafana",
  "repository_url": "https://grafana.github.io/helm-charts",
  "chart_path": "grafana",
  "chart_version": "8.12.1",
  "deploy_order": 1,
  "required": true,
  "default_values": "adminUser: admin\nadminPassword: admin\npersistence:\n  enabled: false\nservice:\n  type: ClusterIP\n  port: 3000",
  "locked_values": ""
}' > /dev/null

api_post "/api/v1/templates/${INFRA_ID}/charts" '{
  "chart_name": "loki",
  "repository_url": "https://grafana.github.io/helm-charts",
  "chart_path": "loki",
  "chart_version": "6.24.1",
  "deploy_order": 2,
  "required": false,
  "default_values": "deploymentMode: SingleBinary\nloki:\n  auth_enabled: false\n  storage:\n    type: filesystem\nsingleBinary:\n  replicas: 1\n  persistence:\n    size: 5Gi",
  "locked_values": ""
}' > /dev/null
log "  + 2 charts (grafana, loki)"

# ── Other Category ───────────────────────────────────────────────────
OTHER_ID=$(api_post "/api/v1/templates" '{
  "name": "Development Tools",
  "description": "Useful dev tools: MinIO for S3-compatible storage, Mailpit for email testing.",
  "category": "Other",
  "version": "1.0.0",
  "default_branch": "main"
}' | get_id)
log "Other template: ${OTHER_ID}"

api_post "/api/v1/templates/${OTHER_ID}/charts" '{
  "chart_name": "minio",
  "repository_url": "https://charts.min.io",
  "chart_path": "minio",
  "chart_version": "5.3.0",
  "deploy_order": 1,
  "required": true,
  "default_values": "mode: standalone\nrootUser: minioadmin\nrootPassword: minioadmin\npersistence:\n  size: 5Gi\nresources:\n  requests:\n    memory: 512Mi",
  "locked_values": ""
}' > /dev/null

api_post "/api/v1/templates/${OTHER_ID}/charts" '{
  "chart_name": "mailpit",
  "repository_url": "https://charts.johansmitsnl.github.io/mailpit",
  "chart_path": "mailpit",
  "chart_version": "0.19.0",
  "deploy_order": 2,
  "required": false,
  "default_values": "service:\n  type: ClusterIP\n  port: 8025\n  smtpPort: 1025",
  "locked_values": ""
}' > /dev/null
log "  + 2 charts (minio, mailpit)"

# ══════════════════════════════════════════════════════════════════════
# 3. PUBLISH TEMPLATES (creates version snapshots automatically)
# ══════════════════════════════════════════════════════════════════════
section "Publishing templates (creates version 1.0.0 snapshots)"

for TID in "$WEB_ID" "$API_ID" "$DATA_ID" "$INFRA_ID" "$OTHER_ID"; do
  curl -s -X POST "${API}/api/v1/templates/${TID}/publish" -H "$AUTH" > /dev/null
done
log "All 5 templates published"

# Bump and re-publish the Web template to create a v1.1.0 version for version diff demo
api_put "/api/v1/templates/${WEB_ID}" '{
  "name": "Full-Stack Web App",
  "description": "NGINX ingress + backend API + frontend. Now with improved defaults.",
  "category": "Web",
  "version": "1.1.0",
  "default_branch": "main"
}' > /dev/null
curl -s -X POST "${API}/api/v1/templates/${WEB_ID}/publish" -H "$AUTH" > /dev/null
log "Web template bumped to v1.1.0 (2 versions for diff demo)"

# ══════════════════════════════════════════════════════════════════════
# 4. STACK DEFINITIONS + INSTANCES
# ══════════════════════════════════════════════════════════════════════
section "Creating stack definitions and instances"

# Instantiate Web template → creates a definition
DEF_ID=$(api_post "/api/v1/templates/${WEB_ID}/instantiate" "{
  \"name\": \"my-web-app-${RUN_ID}\",
  \"description\": \"Test web application stack for local development\"
}" | get_def_id)
log "Stack definition: ${DEF_ID} (from Web template)"

# Create multiple instances for comparison and bulk operations demo
INST1_ID=$(api_post "/api/v1/stack-instances" "{
  \"name\": \"web-dev-${RUN_ID}\",
  \"stack_definition_id\": \"${DEF_ID}\",
  \"branch\": \"main\",
  \"ttl_minutes\": 480
}" | get_id)
log "Instance 1: ${INST1_ID} (branch: main, TTL: 8h)"

INST2_ID=$(api_post "/api/v1/stack-instances" "{
  \"name\": \"web-feature-${RUN_ID}\",
  \"stack_definition_id\": \"${DEF_ID}\",
  \"branch\": \"feature/auth\"
}" | get_id)
log "Instance 2: ${INST2_ID} (branch: feature/auth, no TTL)"

# Instantiate API template for a second definition
API_DEF_ID=$(api_post "/api/v1/templates/${API_ID}/instantiate" "{
  \"name\": \"api-svc-${RUN_ID}\",
  \"description\": \"Microservice API with Redis cache\"
}" | get_def_id)
log "Stack definition: ${API_DEF_ID} (from API template)"

INST3_ID=$(api_post "/api/v1/stack-instances" "{
  \"name\": \"api-staging-${RUN_ID}\",
  \"stack_definition_id\": \"${API_DEF_ID}\",
  \"branch\": \"develop\",
  \"ttl_minutes\": 1440
}" | get_id)
log "Instance 3: ${INST3_ID} (branch: develop, TTL: 24h)"

# ══════════════════════════════════════════════════════════════════════
# 5. VALUE OVERRIDES (per-chart customization)
# ══════════════════════════════════════════════════════════════════════
section "Setting value overrides"

# Get chart configs for the web definition to find chart IDs
WEB_DEF_RESP=$(api_get "/api/v1/stack-definitions/${DEF_ID}")
FIRST_CHART_ID=$(echo "$WEB_DEF_RESP" | python3 -c "
import sys, json
data = json.load(sys.stdin)
charts = data.get('charts', [])
if charts:
    print(charts[0]['id'])
else:
    print('')
" 2>/dev/null || true)

if [ -n "$FIRST_CHART_ID" ]; then
  api_put "/api/v1/stack-instances/${INST1_ID}/overrides/${FIRST_CHART_ID}" '{
    "values": "replicaCount: 3\nresources:\n  requests:\n    cpu: 200m\n    memory: 256Mi"
  }' > /dev/null
  log "Override set on instance 1, chart ${FIRST_CHART_ID}"
else
  warn "Could not find chart ID for overrides"
fi

# ══════════════════════════════════════════════════════════════════════
# 6. BRANCH OVERRIDES (per-chart branch customization)
# ══════════════════════════════════════════════════════════════════════
section "Setting branch overrides"

if [ -n "$FIRST_CHART_ID" ]; then
  api_put "/api/v1/stack-instances/${INST2_ID}/branches/${FIRST_CHART_ID}" '{
    "branch": "feature/new-ui"
  }' > /dev/null
  log "Branch override on instance 2, chart ${FIRST_CHART_ID} → feature/new-ui"
else
  warn "Could not find chart ID for branch overrides"
fi

# ══════════════════════════════════════════════════════════════════════
# 7. FAVORITES (user bookmarks)
# ══════════════════════════════════════════════════════════════════════
section "Adding favorites"

api_post "/api/v1/favorites" "{
  \"entity_type\": \"stack_template\",
  \"entity_id\": \"${WEB_ID}\"
}" > /dev/null 2>&1 || true
log "Favorited Web template"

api_post "/api/v1/favorites" "{
  \"entity_type\": \"stack_instance\",
  \"entity_id\": \"${INST1_ID}\"
}" > /dev/null 2>&1 || true
log "Favorited instance web-dev"

api_post "/api/v1/favorites" "{
  \"entity_type\": \"stack_template\",
  \"entity_id\": \"${DATA_ID}\"
}" > /dev/null 2>&1 || true
log "Favorited Data Pipeline template"

# ══════════════════════════════════════════════════════════════════════
# 8. CLEANUP POLICIES (cron-based automation)
# ══════════════════════════════════════════════════════════════════════
section "Creating cleanup policies"

api_post "/api/v1/admin/cleanup-policies" '{
  "name": "Stop idle stacks (7 days)",
  "action": "stop",
  "condition": "idle_days:7",
  "schedule": "0 2 * * *",
  "cluster_id": "all",
  "enabled": true,
  "dry_run": false
}' > /dev/null 2>&1 || true
log "Policy: Stop idle stacks (7 days) — daily at 02:00"

api_post "/api/v1/admin/cleanup-policies" '{
  "name": "Delete stopped stacks (14 days)",
  "action": "delete",
  "condition": "status:stopped,age_days:14",
  "schedule": "0 3 * * 0",
  "cluster_id": "all",
  "enabled": true,
  "dry_run": false
}' > /dev/null 2>&1 || true
log "Policy: Delete stopped stacks (14 days) — weekly Sunday 03:00"

api_post "/api/v1/admin/cleanup-policies" '{
  "name": "Clean expired TTL stacks (dry run)",
  "action": "clean",
  "condition": "ttl_expired",
  "schedule": "0 4 * * *",
  "cluster_id": "all",
  "enabled": true,
  "dry_run": true
}' > /dev/null 2>&1 || true
log "Policy: Clean expired TTL (dry run) — daily at 04:00"

# ══════════════════════════════════════════════════════════════════════
# 9. CLUSTER + SHARED VALUES + RESOURCE QUOTAS
# ══════════════════════════════════════════════════════════════════════
section "Configuring cluster resources"

# Get the default cluster ID
CLUSTER_ID=$(api_get "/api/v1/clusters" | python3 -c "
import sys, json
clusters = json.load(sys.stdin)
if isinstance(clusters, list) and len(clusters) > 0:
    # Prefer the default cluster
    default = [c for c in clusters if c.get('is_default')]
    print(default[0]['id'] if default else clusters[0]['id'])
else:
    print('')
" 2>/dev/null || true)

if [ -n "$CLUSTER_ID" ]; then
  log "Found cluster: ${CLUSTER_ID}"

  # Shared values — applied to all instances in the cluster
  api_post "/api/v1/clusters/${CLUSTER_ID}/shared-values" '{
    "name": "Global Labels",
    "description": "Labels applied to all stack deployments",
    "values": "global:\n  labels:\n    managed-by: k8s-stack-manager\n    environment: development",
    "priority": 1
  }' > /dev/null 2>&1 || true
  log "Shared values: Global Labels (priority 1)"

  api_post "/api/v1/clusters/${CLUSTER_ID}/shared-values" '{
    "name": "Resource Defaults",
    "description": "Default resource requests for all stacks",
    "values": "resources:\n  requests:\n    cpu: 50m\n    memory: 64Mi",
    "priority": 10
  }' > /dev/null 2>&1 || true
  log "Shared values: Resource Defaults (priority 10)"

  # Resource quotas — per-namespace limits
  api_put "/api/v1/clusters/${CLUSTER_ID}/quotas" '{
    "cpu_request": "4",
    "cpu_limit": "8",
    "memory_request": "4Gi",
    "memory_limit": "8Gi",
    "storage_limit": "50Gi",
    "pod_limit": 50
  }' > /dev/null 2>&1 || true
  log "Resource quotas: 8 CPU / 8Gi memory / 50 pods per namespace"
else
  warn "No cluster found — skipping shared values and resource quotas"
fi

# ══════════════════════════════════════════════════════════════════════
# 10. NOTIFICATIONS (seed some for the admin user)
# ══════════════════════════════════════════════════════════════════════
section "Notifications"
log "Notifications are generated automatically on deploy/stop/clean events"
log "(Deploy instances via the UI to see notifications appear)"

# ══════════════════════════════════════════════════════════════════════
# 11. DEFINITION EXPORT/IMPORT DEMO
# ══════════════════════════════════════════════════════════════════════
section "Import/export demo"

IMPORT_RESP=$(api_post "/api/v1/stack-definitions/import" "{
  \"schema_version\": \"1.0\",
  \"definition\": {
    \"name\": \"imported-svc-${RUN_ID}\",
    \"description\": \"Imported via seed script to demo import/export\",
    \"default_branch\": \"main\"
  },
  \"charts\": [
    {
      \"chart_name\": \"backend\",
      \"repository_url\": \"https://charts.bitnami.com/bitnami\",
      \"chart_path\": \"node\",
      \"chart_version\": \"19.1.7\",
      \"deploy_order\": 1,
      \"default_values\": \"replicaCount: 1\\nimage:\\n  tag: latest\"
    },
    {
      \"chart_name\": \"database\",
      \"repository_url\": \"https://charts.bitnami.com/bitnami\",
      \"chart_path\": \"postgresql\",
      \"chart_version\": \"16.4.6\",
      \"deploy_order\": 2,
      \"default_values\": \"auth:\\n  postgresPassword: changeme\\nprimary:\\n  persistence:\\n    size: 5Gi\"
    }
  ]
}" 2>/dev/null)

IMPORT_ID=$(echo "$IMPORT_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || true)
if [ -n "$IMPORT_ID" ]; then
  log "Imported definition: ${IMPORT_ID} (2 charts: backend, database)"
else
  warn "Import may have failed (definition might already exist)"
fi

# ══════════════════════════════════════════════════════════════════════
# SUMMARY
# ══════════════════════════════════════════════════════════════════════
echo ""
echo "═══════════════════════════════════════════════════════════════"
echo -e "${GREEN}Seed data created successfully!${NC}"
echo ""
echo "Templates (5, all published):"
echo "  Web:            Full-Stack Web App (3 charts, 2 versions)"
echo "  API:            REST API Service (2 charts)"
echo "  Data:           Data Pipeline (3 charts)"
echo "  Infrastructure: Observability Stack (2 charts)"
echo "  Other:          Development Tools (2 charts)"
echo ""
echo "Stack Definitions (3):"
echo "  my-web-app-${RUN_ID}    (from Web template)"
echo "  api-svc-${RUN_ID}       (from API template)"
echo "  imported-svc-${RUN_ID}  (from import)"
echo ""
echo "Stack Instances (3):"
echo "  web-dev-${RUN_ID}       (main, TTL: 8h, has value overrides)"
echo "  web-feature-${RUN_ID}   (feature/auth, has branch override)"
echo "  api-staging-${RUN_ID}   (develop, TTL: 24h)"
echo ""
echo "Cleanup Policies (3):"
echo "  Stop idle (7d)     — daily 02:00"
echo "  Delete stopped (14d) — weekly Sunday 03:00"
echo "  Clean TTL expired  — daily 04:00 (dry run)"
echo ""
if [ -n "$CLUSTER_ID" ]; then
echo "Cluster Resources:"
echo "  Shared values: Global Labels (priority 1), Resource Defaults (priority 10)"
echo "  Resource quotas: 8 CPU / 8Gi memory / 50 pods per namespace"
echo ""
fi
echo "Favorites: Web template, Data Pipeline template, web-dev instance"
echo ""
echo "Users:"
echo "  admin/admin         (admin)"
echo "  devops/devops       (devops)"
echo "  developer/developer (user)"
echo "  viewer/viewer       (user)"
echo ""
echo "Try:"
echo "  - Template version diff: Templates → Full-Stack Web App → Version History"
echo "  - Instance comparison: Stack Instances → Compare"
echo "  - Bulk operations: Stack Instances → select multiple → Bulk Actions"
echo "  - Import/Export: Stack Definitions → Import / Edit → Export"
echo "  - Notifications: deploy an instance to see notifications appear"
echo "═══════════════════════════════════════════════════════════════"
