#!/bin/bash
# Isolated API deployment with fake build/systemd/health; no real services.
set -euo pipefail
SOURCE_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
source "$SOURCE_ROOT/scripts/cloud/deploy_main_lib.sh"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT
for outcome in healthy failed; do
  test_dir="$TEST_ROOT/$outcome"
  mkdir -p "$test_dir/source/scripts/cloud" "$test_dir/source/deploy" "$test_dir/state"
  cat > "$test_dir/source/scripts/cloud/restart.sh" <<'SCRIPT'
#!/bin/bash
exit 0
SCRIPT
  cat > "$test_dir/source/deploy/release-events.json" <<'JSON'
[{"id":"check-start","kind":"instrumentation","deploy_scope":"api","dashboards":["card-refresh"],"title":"Check starts","description":"Partial client coverage","pr":"https://github.com/phronesis-io/eigenflux/pull/1"}]
JSON
  go() { mkdir -p "$(dirname "$3")"; printf '#!/bin/sh\n' > "$3"; chmod +x "$3"; }
  systemctl() { return 0; }
  curl() { return 0; }
  # Real matching is covered by existing deployer behavior; this gate represents
  # whether the requested executable was observed after the health check.
  deploy_main_api_matches() { [[ "$outcome" == healthy ]]; }
  deploy_main_annotation_tool() { python3 "$SOURCE_ROOT/scripts/cloud/release_annotations.py" "$@"; }
  mv() {
    if [[ $(uname -s) == Darwin && $1 == -Tf ]]; then shift; command mv -fh "$@";
    else command mv "$@"; fi
  }
  if deploy_main_api_only "$test_dir/source" "$test_dir/state" "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" "$test_dir/override"; then
    [[ "$outcome" == healthy ]]
    python3 - "$test_dir/state/annotations/state.json" <<'PY'
import json,sys
state=json.load(open(sys.argv[1]))
assert len(state['pending'])==1
assert set(state['active'])=={'check-start'}
PY
  else
    [[ "$outcome" == failed ]]
    test ! -e "$test_dir/state/annotations/state.json"
    test ! -e "$test_dir/state/api-deployed-sha"
  fi
  echo "PASS: API $outcome rollout records only verified success"
done
