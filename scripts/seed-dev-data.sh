#!/usr/bin/env bash
# scripts/seed-dev-data.sh
# Seeds the local dev environment with one stack template per category,
# each with realistic Helm charts. Requires `make dev` to be running.
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
NC='\033[0m'

log()  { echo -e "${GREEN}✓${NC} $*"; }
warn() { echo -e "${YELLOW}!${NC} $*"; }
fail() { echo -e "${RED}✗${NC} $*" >&2; exit 1; }

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

# Helper: extract .id from JSON
get_id() { python3 -c "import sys,json; print(json.load(sys.stdin)['id'])"; }

# Helper: extract .definition.id from instantiate response
get_def_id() { python3 -c "import sys,json; print(json.load(sys.stdin)['definition']['id'])"; }

# ── 1. Web Category ───────────────────────────────────────────────────
echo ""
echo "Creating templates..."

WEB_ID=$(api_post "/api/v1/templates" '{
  "name": "Full-Stack Web App",
  "description": "NGINX ingress + backend API + frontend. Good for typical web applications.",
  "category": "Web",
  "version": "1.0.0",
  "default_branch": "main"
}' | get_id)
log "Web template: ${WEB_ID}"

api_post "/api/v1/templates/${WEB_ID}/charts" '{
  "chart_name": "ingress-nginx",
  "repository_url": "https://kubernetes.github.io/ingress-nginx",
  "chart_path": "ingress-nginx",
  "chart_version": "4.11.3",
  "deploy_order": 1,
  "required": true,
  "default_values": "controller:\n  replicaCount: 1\n  service:\n    type: ClusterIP",
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
log "  + 3 charts (ingress-nginx, azurite-storage, frontend-app)"

# ── 2. API Category ──────────────────────────────────────────────────
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

# ── 3. Data Category ─────────────────────────────────────────────────
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

# ── 4. Infrastructure Category ────────────────────────────────────────
INFRA_ID=$(api_post "/api/v1/templates" '{
  "name": "Cluster Infrastructure",
  "description": "Shared infra components: cert-manager, monitoring stack.",
  "category": "Infrastructure",
  "version": "1.0.0",
  "default_branch": "main"
}' | get_id)
log "Infrastructure template: ${INFRA_ID}"

api_post "/api/v1/templates/${INFRA_ID}/charts" '{
  "chart_name": "cert-manager",
  "repository_url": "https://charts.jetstack.io",
  "chart_path": "cert-manager",
  "chart_version": "1.16.2",
  "deploy_order": 1,
  "required": true,
  "default_values": "crds:\n  enabled: true\nreplicaCount: 1",
  "locked_values": "crds:\n  enabled: true"
}' > /dev/null

api_post "/api/v1/templates/${INFRA_ID}/charts" '{
  "chart_name": "kube-prometheus-stack",
  "repository_url": "https://prometheus-community.github.io/helm-charts",
  "chart_path": "kube-prometheus-stack",
  "chart_version": "65.3.1",
  "deploy_order": 2,
  "required": false,
  "default_values": "grafana:\n  enabled: true\n  adminPassword: admin\nprometheus:\n  prometheusSpec:\n    retention: 7d\n    storageSpec:\n      volumeClaimTemplate:\n        spec:\n          resources:\n            requests:\n              storage: 10Gi",
  "locked_values": ""
}' > /dev/null
log "  + 2 charts (cert-manager, kube-prometheus-stack)"

# ── 5. Other Category ────────────────────────────────────────────────
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

# ── Publish all templates ─────────────────────────────────────────────
echo ""
echo "Publishing templates..."
for TID in "$WEB_ID" "$API_ID" "$DATA_ID" "$INFRA_ID" "$OTHER_ID"; do
  curl -s -X POST "${API}/api/v1/templates/${TID}/publish" -H "$AUTH" > /dev/null
done
log "All 5 templates published"

# ── Create a test stack definition from the Web template ──────────────
echo ""
echo "Creating test stack definition + instance..."

DEF_ID=$(api_post "/api/v1/templates/${WEB_ID}/instantiate" "{
  \"name\": \"my-web-app-${RUN_ID}\",
  \"description\": \"Test web application stack for local development\"
}" | get_def_id)
log "Stack definition: ${DEF_ID}"

# Create a stack instance
INST_ID=$(api_post "/api/v1/stack-instances" "{
  \"name\": \"dev-test-${RUN_ID}\",
  \"stack_definition_id\": \"${DEF_ID}\",
  \"branch\": \"main\"
}" | get_id)
log "Stack instance: ${INST_ID}"

# ── Create test users ────────────────────────────────────────────────
echo ""
echo "Creating test users..."

api_post "/api/v1/auth/register" '{
  "username": "developer",
  "password": "developer",
  "display_name": "Test Developer",
  "role": "user"
}' > /dev/null
log "User 'developer' (role: user)"

api_post "/api/v1/auth/register" '{
  "username": "devops",
  "password": "devops",
  "display_name": "Test DevOps",
  "role": "devops"
}' > /dev/null
log "User 'devops' (role: devops)"

# ── Summary ───────────────────────────────────────────────────────────
echo ""
echo "═══════════════════════════════════════════════"
echo -e "${GREEN}Seed data created successfully!${NC}"
echo ""
echo "Templates (5):"
echo "  Web:            Full-Stack Web App (3 charts)"
echo "  API:            REST API Service (2 charts)"
echo "  Data:           Data Pipeline (3 charts)"
echo "  Infrastructure: Cluster Infrastructure (2 charts)"
echo "  Other:          Development Tools (2 charts)"
echo ""
echo "Stack Definition: my-web-app-${RUN_ID} (from Web template)"
echo "Stack Instance:   dev-test-${RUN_ID} (branch: main)"
echo ""
echo "Users:"
echo "  admin/admin       (admin)"
echo "  devops/devops     (devops)"
echo "  developer/developer (user)"
echo "═══════════════════════════════════════════════"
