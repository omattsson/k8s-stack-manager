#!/usr/bin/env bash
set -euo pipefail

# Unset stale PYTHONPATH that points to /usr/local/lib/python3.6/site-packages
# and breaks pipx, pip, and other modern Python tools.
unset PYTHONPATH

GIT_DIR="${HOME}/git"
VENV_PYTHON="${HOME}/.local/pipx/venvs/mempalace/bin/python"
MEMPALACE="${HOME}/.local/bin/mempalace"

if [[ ! -x "$MEMPALACE" ]]; then
  echo "Error: mempalace not found at $MEMPALACE" >&2
  exit 1
fi

# Test the full import chain: chromadb → posthog → dateutil → six
# A bare "import chromadb" succeeds; the crash only happens when
# PersistentClient triggers lazy posthog telemetry imports.
IMPORT_TEST='from dateutil.tz import tzutc; import chromadb'
if ! "$VENV_PYTHON" -c "$IMPORT_TEST" 2>/dev/null; then
  echo "=== Dependency broken in venv, injecting 'six' ==="
  pipx inject mempalace six 2>/dev/null || true

  if ! "$VENV_PYTHON" -c "$IMPORT_TEST" 2>/dev/null; then
    echo "=== Still broken, reinstalling mempalace with Python 3.12 ==="
    PYTHON_312=$(command -v python3.12 || true)
    if [[ -z "$PYTHON_312" ]]; then
      echo "Python 3.12 not found. Installing via brew..."
      brew install python@3.12
      PYTHON_312=$(brew --prefix python@3.12)/bin/python3.12
    fi
    pipx install --force mempalace --python "$PYTHON_312"
  else
    echo "=== Fixed by injecting 'six' ==="
  fi
fi

for dir in "$GIT_DIR"/*/; do
  name=$(basename "$dir")

  # Skip non-directories and known non-project files
  [[ ! -d "$dir" ]] && continue
  [[ "$name" == *.lock ]] && continue
  [[ "$name" == *.tar.gz ]] && continue

  echo "=== Initializing: $name ==="
  "$MEMPALACE" init --yes "$dir" 2>/dev/null || true

  echo "=== Mining: $name ==="
  "$MEMPALACE" mine "$dir" || echo "  ⚠ Failed to mine $name, continuing..."

  echo ""
done

echo "Done. Run 'mempalace status' to see your palace."
