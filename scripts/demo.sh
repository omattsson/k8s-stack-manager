#!/usr/bin/env bash
# Try K8s Stack Manager on your machine in about 5 minutes.
#
#   scripts/demo.sh up     create a kind cluster, install the chart, deploy a stack
#   scripts/demo.sh down   delete the kind cluster and the demo files
#
# Needs docker, kind, kubectl, helm and stackctl (v0.10.0 or later for
# port-forward and exec). The demo runs in its own kind cluster
# (KSM_DEMO_CLUSTER, default ksm-demo); it never uses your other kube
# contexts. stackctl gets its own config directory, so your stackctl
# contexts stay as they are. The images come from ghcr.io; the starter
# stack pulls the podinfo chart from https://stefanprodan.github.io/podinfo.
set -eo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
CLUSTER="${KSM_DEMO_CLUSTER:-ksm-demo}"
CONTEXT="kind-${CLUSTER}"
NAMESPACE="stack-manager"
RELEASE="stack-manager"
PORT="${KSM_DEMO_PORT:-8080}"
DEMO_DIR="${KSM_DEMO_DIR:-${HOME}/.ksm-demo}"
STACK="hello"

export STACKCTL_CONFIG_DIR="${DEMO_DIR}/stackctl"

say() { printf '\n==> %s\n' "$*"; }
die() { printf 'Error: %s\n' "$*" >&2; exit 1; }
kc() { kubectl --context "${CONTEXT}" "$@"; }

need() {
  for cmd in "$@"; do
    command -v "${cmd}" &>/dev/null || die "${cmd} is not installed."
  done
}

# Keep a kubectl port-forward to the frontend running until stop_forward:
# a port-forward ends when the pod restarts.
start_forward() {
  stop_forward
  rm -f "${DEMO_DIR}/forward.stop"
  (
    while [ ! -f "${DEMO_DIR}/forward.stop" ]; do
      kubectl --context "${CONTEXT}" -n "${NAMESPACE}" port-forward \
        "svc/${RELEASE}-k8s-stack-manager-frontend" "${PORT}:8080" >> "${DEMO_DIR}/forward.log" 2>&1 || true
      sleep 1
    done
  ) &
  echo $! > "${DEMO_DIR}/forward.pid"
  for _ in $(seq 1 30); do
    curl -s -o /dev/null "http://localhost:${PORT}/health/live" && return 0
    sleep 1
  done
  die "the UI does not answer on http://localhost:${PORT} (see ${DEMO_DIR}/forward.log)"
}

stop_forward() {
  touch "${DEMO_DIR}/forward.stop" 2>/dev/null || true
  if [ -f "${DEMO_DIR}/forward.pid" ]; then
    kill "$(cat "${DEMO_DIR}/forward.pid")" 2>/dev/null || true
    rm -f "${DEMO_DIR}/forward.pid"
  fi
  pkill -f "port-forward svc/${RELEASE}-k8s-stack-manager-frontend ${PORT}:8080" 2>/dev/null || true
}

up() {
  need docker kind kubectl helm stackctl openssl curl
  docker info &>/dev/null || die "docker is not running."
  mkdir -p "${DEMO_DIR}"
  chmod 700 "${DEMO_DIR}"

  if kind get clusters 2>/dev/null | grep -qx "${CLUSTER}"; then
    say "Using the existing kind cluster ${CLUSTER}"
  else
    say "Creating the kind cluster ${CLUSTER}"
    kind create cluster --name "${CLUSTER}" --wait 120s
  fi

  # Random secrets, kept for the next run of "up".
  if [ ! -f "${DEMO_DIR}/secrets.yaml" ]; then
    (
      umask 077
      cat > "${DEMO_DIR}/secrets.yaml" <<EOF
backend:
  secrets:
    JWT_SECRET: "$(openssl rand -hex 32)"
    ADMIN_PASSWORD: "$(openssl rand -hex 12)"
    KUBECONFIG_ENCRYPTION_KEY: "$(openssl rand -hex 32)"
mysql:
  auth:
    rootPassword: "$(openssl rand -hex 16)"
EOF
    )
  fi

  say "Installing the Helm chart (namespace ${NAMESPACE})"
  helm upgrade --install "${RELEASE}" "${REPO_DIR}/helm/k8s-stack-manager" \
    --kube-context "${CONTEXT}" --namespace "${NAMESPACE}" --create-namespace \
    --values "${SCRIPT_DIR}/demo-values.yaml" --values "${DEMO_DIR}/secrets.yaml" \
    --wait --timeout 10m

  say "Opening http://localhost:${PORT}"
  start_forward

  say "Configuring stackctl (config in ${STACKCTL_CONFIG_DIR})"
  stackctl config use-context ksm-demo >/dev/null
  stackctl config set api-url "http://localhost:${PORT}" >/dev/null
  STACKCTL_PASSWORD="$(awk -F'"' '/ADMIN_PASSWORD/{print $2}' "${DEMO_DIR}/secrets.yaml")" \
    stackctl login --username admin >/dev/null

  if [ -z "$(stackctl cluster list -q)" ]; then
    say "Registering the cluster (the backend's own service account)"
    cluster_id="$(stackctl cluster create --name local --use-in-cluster --allow-port-forward --allow-exec -q)"
    stackctl cluster set-default "${cluster_id}" >/dev/null
  fi

  if ! stackctl definition list -o json | grep -q '"name": "Hello World"'; then
    say "Importing the Hello World definition"
    stackctl definition import --file "${REPO_DIR}/examples/starter-templates/hello-world.json" >/dev/null
  fi

  if ! stackctl stack get "${STACK}" &>/dev/null; then
    stackctl stack create --definition "Hello World" --name "${STACK}" --ttl 0 >/dev/null
  fi
  say "Deploying the stack ${STACK}"
  stackctl stack deploy "${STACK}" --follow

  cat <<EOF

K8s Stack Manager runs in the kind cluster ${CLUSTER}.

  Web UI:    http://localhost:${PORT}
             user admin, password in ${DEMO_DIR}/secrets.yaml (ADMIN_PASSWORD)

Try stackctl (use the demo config in this shell first):

  export STACKCTL_CONFIG_DIR=${STACKCTL_CONFIG_DIR}
  stackctl stack list
  stackctl stack pods ${STACK}
  stackctl stack port-forward ${STACK} podinfo 9898      # then open http://localhost:9898

Remove everything:  scripts/demo.sh down   (or make demo-down)
EOF
}

down() {
  need kind
  stop_forward
  if kind get clusters 2>/dev/null | grep -qx "${CLUSTER}"; then
    say "Deleting the kind cluster ${CLUSTER}"
    kind delete cluster --name "${CLUSTER}"
  fi
  rm -rf "${DEMO_DIR}"
  say "Removed the demo cluster and ${DEMO_DIR}"
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  *) echo "Usage: $0 up|down" >&2; exit 2 ;;
esac
