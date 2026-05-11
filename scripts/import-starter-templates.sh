#!/usr/bin/env bash
set -eo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATES_DIR="$(cd "${SCRIPT_DIR}/../examples/starter-templates" && pwd)"

if ! command -v stackctl &>/dev/null; then
  echo "Error: stackctl is not installed."
  echo "Install it with: brew install omattsson/tap/stackctl"
  exit 1
fi

if ! command -v python3 &>/dev/null; then
  echo "Error: python3 is required for JSON parsing."
  exit 1
fi

shopt -s nullglob
json_files=("${TEMPLATES_DIR}"/*.json)
shopt -u nullglob

if [ ${#json_files[@]} -eq 0 ]; then
  echo "No template bundles found in ${TEMPLATES_DIR}"
  exit 1
fi

echo "Importing starter templates..."
echo

imported=0
skipped=0
failed=0

for file in "${json_files[@]}"; do
  name=$(python3 -c "import json,sys; print(json.load(sys.stdin)['definition']['name'])" < "${file}" 2>/dev/null || basename "${file}" .json)

  if N="${name}" stackctl definition list -o json 2>/dev/null | N="${name}" python3 -c "
import json,sys,os
defs = json.load(sys.stdin).get('data', [])
sys.exit(0 if any(d['name'] == os.environ['N'] for d in defs) else 1)
" 2>/dev/null; then
    echo "  Skip: ${name} (already exists)"
    skipped=$((skipped + 1))
    continue
  fi

  output=$(stackctl definition import --file "${file}" -q 2>&1) && {
    echo "  OK:   ${name}"
    imported=$((imported + 1))
  } || {
    echo "  FAIL: ${name}"
    echo "        ${output}" | head -3
    failed=$((failed + 1))
  }
done

echo
echo "Done. Imported: ${imported}, Skipped: ${skipped}, Failed: ${failed}"

if [ "${failed}" -gt 0 ]; then
  exit 1
fi

echo
echo "Next steps:"
echo "  stackctl definition list"
echo "  stackctl stack create --definition <id> --name my-stack"
echo "  stackctl stack deploy my-stack"
