#!/usr/bin/env bash
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATES_DIR="$(cd "${SCRIPT_DIR}/../examples/starter-templates" && pwd)"

if ! command -v stackctl &>/dev/null; then
  echo "Error: stackctl is not installed."
  echo "Install it with: brew install omattsson/tap/stackctl"
  exit 1
fi

echo "Importing starter templates..."
echo

imported=0
skipped=0
failed=0

for file in "${TEMPLATES_DIR}"/*.json; do
  name=$(python3 -c "import json,sys; print(json.load(sys.stdin)['definition']['name'])" < "${file}" 2>/dev/null || basename "${file}" .json)

  if export N="${name}" && stackctl definition list -o json 2>/dev/null | N="${name}" python3 -c "import json,sys,os; defs=json.load(sys.stdin).get('data',[]); sys.exit(0 if any(d['name']==os.environ['N'] for d in defs) else 1)" 2>/dev/null; then
    echo "  Skip: ${name} (already exists)"
    skipped=$((skipped + 1))
    continue
  fi

  if stackctl definition import --file "${file}" -q >/dev/null; then
    echo "  OK:   ${name}"
    imported=$((imported + 1))
  else
    echo "  FAIL: ${name}"
    failed=$((failed + 1))
  fi
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
