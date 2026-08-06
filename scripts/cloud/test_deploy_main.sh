#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")"; pwd)"
SOURCE_SCRIPT="${SCRIPT_DIR}/deploy_main.sh"
TEMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/eigenflux-deploy-test.XXXXXX")"

cleanup() {
  rm -rf -- "${TEMP_ROOT}"
}
trap cleanup EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  local file="$1" expected="$2"
  grep -Fq -- "${expected}" "${file}" || {
    sed -n '1,220p' "${file}" >&2
    fail "expected output to contain: ${expected}"
  }
}

create_fixture() {
  local name="$1"
  local root="${TEMP_ROOT}/${name}"
  local seed="${root}/seed"

  mkdir -p "${root}/api/repos/test/eigenflux/commits" "${root}/bin"
  cat >"${root}/bin/flock" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  chmod +x "${root}/bin/flock"
  git init --bare -q "${root}/remote.git"
  git init -q -b main "${seed}"
  git -C "${seed}" config user.name "Deploy Test"
  git -C "${seed}" config user.email "deploy-test@example.invalid"
  mkdir -p "${seed}/scripts/common" "${seed}/scripts/cloud" \
    "${seed}/skills/ef-broadcast/references" "${seed}/static"
  printf 'contract\n' >"${seed}/skills/ef-broadcast/references/contract.md"
  printf 'contract\n' >"${seed}/static/feed_contract.md"
  printf 'build/\n' >"${seed}/.gitignore"

  cat >"${seed}/scripts/common/build.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'build\n' >>"${TRACE_FILE}"
[[ "${FAIL_STAGE:-}" != "build" ]] || exit 41
ROOT="$(cd "$(dirname "$0")/../.."; pwd)"
mkdir -p "${ROOT}/build"
for service in "$@"; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"${ROOT}/build/${service}"
  chmod +x "${ROOT}/build/${service}"
done
if [[ -n "${ADVANCE_REMOTE_URL:-}" ]]; then
  ADVANCE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/advance-main.XXXXXX")"
  git -c core.hooksPath=/dev/null clone -q "${ADVANCE_REMOTE_URL}" "${ADVANCE_DIR}/repo"
  git -C "${ADVANCE_DIR}/repo" config user.name "Deploy Test"
  git -C "${ADVANCE_DIR}/repo" config user.email "deploy-test@example.invalid"
  printf 'advanced\n' >"${ADVANCE_DIR}/repo/advanced.txt"
  git -C "${ADVANCE_DIR}/repo" add advanced.txt
  git -c core.hooksPath=/dev/null -C "${ADVANCE_DIR}/repo" commit -qm "advance main"
  git -c core.hooksPath=/dev/null -C "${ADVANCE_DIR}/repo" push -q origin main
  rm -rf -- "${ADVANCE_DIR}"
fi
EOF
  cat >"${seed}/scripts/common/migrate_up.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'migrate\n' >>"${TRACE_FILE}"
[[ "${FAIL_STAGE:-}" != "migrate" ]] || exit 42
if [[ "${DIRTY_STAGE:-}" == "migrate" ]]; then
  ROOT="$(cd "$(dirname "$0")/../.."; pwd)"
  printf 'unexpected\n' >"${ROOT}/unexpected-migration-output.txt"
fi
EOF
  cat >"${seed}/scripts/cloud/restart_all_services.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'restart\n' >>"${TRACE_FILE}"
[[ "${FAIL_STAGE:-}" != "restart" ]] || exit 43
EOF
  cat >"${seed}/scripts/cloud/check_services.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'health\n' >>"${TRACE_FILE}"
[[ "${FAIL_STAGE:-}" != "health" ]] || exit 44
EOF
  chmod +x "${seed}/scripts/common/"*.sh "${seed}/scripts/cloud/"*.sh
  git -C "${seed}" add .
  git -C "${seed}" commit -qm "base"
  git -C "${seed}" remote add origin "${root}/remote.git"
  git -C "${seed}" push -q -u origin main
  git clone -q "${root}/remote.git" "${root}/prod"
  git -C "${root}/prod" config user.name "Deploy Test"
  git -C "${root}/prod" config user.email "deploy-test@example.invalid"

  printf 'target\n' >"${seed}/release.txt"
  git -C "${seed}" add release.txt
  git -C "${seed}" commit -qm "release"
  git -C "${seed}" push -q origin main

  FIXTURE_ROOT="${root}"
  FIXTURE_PROD="${root}/prod"
  FIXTURE_TARGET="$(git -C "${seed}" rev-parse HEAD)"
  FIXTURE_BASE="$(git -C "${root}/prod" rev-parse HEAD)"
  FIXTURE_TRACE="${root}/trace.log"
  FIXTURE_OUTPUT="${root}/output.log"
  : >"${FIXTURE_TRACE}"
  write_pr_response "${FIXTURE_TARGET}" true
  make_test_script
}

write_pr_response() {
  local target="$1" merged="$2"
  local path="${FIXTURE_ROOT}/api/repos/test/eigenflux/commits/${target}"
  mkdir -p "${path}"
  if [[ "${merged}" == true ]]; then
    printf '[{"merged_at":"2026-08-06T00:00:00Z","base":{"ref":"main"},"merge_commit_sha":"%s","head":{"sha":"unused"}}]\n' \
      "${target}" >"${path}/pulls"
  else
    printf '[]\n' >"${path}/pulls"
  fi
}

make_test_script() {
  cp "${SOURCE_SCRIPT}" "${FIXTURE_ROOT}/deploy-test.sh"
  sed -i.bak \
    -e "s|readonly PROJECT_ROOT=\"/data/git/eigenflux\"|readonly PROJECT_ROOT=\"${FIXTURE_PROD}\"|" \
    -e "s|readonly REMOTE_URL=\"https://github.com/phronesis-io/eigenflux.git\"|readonly REMOTE_URL=\"${FIXTURE_ROOT}/remote.git\"|" \
    -e "s|readonly GITHUB_API_URL=\"https://api.github.com\"|readonly GITHUB_API_URL=\"file://${FIXTURE_ROOT}/api\"|" \
    -e 's|readonly GITHUB_REPOSITORY="phronesis-io/eigenflux"|readonly GITHUB_REPOSITORY="test/eigenflux"|' \
    -e "s|readonly LOCK_PATH=\"/run/lock/eigenflux-deploy-main.lock\"|readonly LOCK_PATH=\"${FIXTURE_ROOT}/deploy.lock\"|" \
    -e "s|readonly REQUIRED_REPO_OWNER=\"root\"|readonly REQUIRED_REPO_OWNER=\"$(id -un)\"|" \
    -e "s|readonly REQUIRED_REPO_GROUP=\"root\"|readonly REQUIRED_REPO_GROUP=\"$(id -gn)\"|" \
    -e 's|readonly ALLOW_NON_ROOT_INTERNAL=0|readonly ALLOW_NON_ROOT_INTERNAL=1|' \
    -e "s|export PATH='/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'|export PATH='${FIXTURE_ROOT}/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'|" \
    -e "s|/var/tmp/eigenflux-deploy-|${FIXTURE_ROOT}/stage-|" \
    "${FIXTURE_ROOT}/deploy-test.sh"
  rm -f "${FIXTURE_ROOT}/deploy-test.sh.bak"
  chmod +x "${FIXTURE_ROOT}/deploy-test.sh"
}

run_deploy() {
  local expected_status="$1"
  shift
  local status=0

  INVOCATION_ID=test TRACE_FILE="${FIXTURE_TRACE}" "$@" \
    "${FIXTURE_ROOT}/deploy-test.sh" --systemd-internal \
    >"${FIXTURE_OUTPUT}" 2>&1 || status=$?
  if [[ "${expected_status}" == success && "${status}" -ne 0 ]]; then
    sed -n '1,240p' "${FIXTURE_OUTPUT}" >&2
    fail "deployment unexpectedly failed with ${status}"
  fi
  if [[ "${expected_status}" == failure && "${status}" -eq 0 ]]; then
    fail "deployment unexpectedly succeeded"
  fi
}

test_success() {
  create_fixture success
  run_deploy success env
  [[ "$(git -C "${FIXTURE_PROD}" rev-parse HEAD)" == "${FIXTURE_TARGET}" ]] || fail "target was not deployed"
  [[ "$(cat "${FIXTURE_TRACE}")" == $'build\nmigrate\nrestart\nhealth' ]] || fail "unexpected success trace"
  [[ -z "$(git -C "${FIXTURE_PROD}" status --porcelain=v1 --untracked-files=all)" ]] || fail "worktree dirty after success"
  printf 'PASS: success path\n'
}

test_dirty_tracked() {
  create_fixture dirty-tracked
  printf 'dirty\n' >>"${FIXTURE_PROD}/scripts/common/migrate_up.sh"
  run_deploy failure env
  assert_contains "${FIXTURE_OUTPUT}" "refusing to deploy a dirty worktree"
  [[ ! -s "${FIXTURE_TRACE}" ]] || fail "dirty worktree reached build"
  printf 'PASS: dirty tracked file rejected\n'
}

test_dirty_untracked() {
  create_fixture dirty-untracked
  printf 'dirty\n' >"${FIXTURE_PROD}/unexpected.txt"
  run_deploy failure env
  assert_contains "${FIXTURE_OUTPUT}" "refusing to deploy a dirty worktree"
  printf 'PASS: untracked file rejected\n'
}

test_wrong_branch() {
  create_fixture wrong-branch
  git -C "${FIXTURE_PROD}" switch -q -c feature
  run_deploy failure env
  assert_contains "${FIXTURE_OUTPUT}" "production worktree must be on branch main"
  printf 'PASS: non-main branch rejected\n'
}

test_divergent_head() {
  create_fixture divergent
  printf 'local\n' >"${FIXTURE_PROD}/local.txt"
  git -C "${FIXTURE_PROD}" add local.txt
  git -C "${FIXTURE_PROD}" commit -qm "local commit"
  run_deploy failure env
  assert_contains "${FIXTURE_OUTPUT}" "behind or divergent"
  [[ ! -s "${FIXTURE_TRACE}" ]] || fail "divergent HEAD reached build"
  printf 'PASS: divergent/ahead HEAD rejected\n'
}

test_missing_pr() {
  create_fixture missing-pr
  write_pr_response "${FIXTURE_TARGET}" false
  run_deploy failure env
  assert_contains "${FIXTURE_OUTPUT}" "not associated with a merged pull request"
  [[ ! -s "${FIXTURE_TRACE}" ]] || fail "missing PR reached build"
  printf 'PASS: target without merged PR rejected\n'
}

test_unmanaged_repository() {
  create_fixture unmanaged-repository
  sed -i.bak \
    "s|readonly REQUIRED_REPO_OWNER=\"$(id -un)\"|readonly REQUIRED_REPO_OWNER=\"owner-that-does-not-exist\"|" \
    "${FIXTURE_ROOT}/deploy-test.sh"
  rm -f "${FIXTURE_ROOT}/deploy-test.sh.bak"
  run_deploy failure env
  assert_contains "${FIXTURE_OUTPUT}" "project root must be owned"
  [[ ! -s "${FIXTURE_TRACE}" ]] || fail "unmanaged repository reached build"
  printf 'PASS: unmanaged repository rejected\n'
}

test_build_failure() {
  create_fixture build-failure
  run_deploy failure env FAIL_STAGE=build
  [[ "$(git -C "${FIXTURE_PROD}" rev-parse HEAD)" == "${FIXTURE_BASE}" ]] || fail "build failure moved production HEAD"
  [[ "$(cat "${FIXTURE_TRACE}")" == "build" ]] || fail "build failure continued"
  printf 'PASS: build failure stops before fast-forward\n'
}

test_main_advances_during_build() {
  create_fixture main-advances
  run_deploy failure env ADVANCE_REMOTE_URL="${FIXTURE_ROOT}/remote.git"
  assert_contains "${FIXTURE_OUTPUT}" "origin/main advanced during the build"
  [[ "$(git -C "${FIXTURE_PROD}" rev-parse HEAD)" == "${FIXTURE_BASE}" ]] || fail "advanced main moved production HEAD"
  [[ "$(cat "${FIXTURE_TRACE}")" == "build" ]] || fail "advanced main continued past build"
  printf 'PASS: main advancing during build aborts before fast-forward\n'
}

test_migration_failure() {
  create_fixture migration-failure
  run_deploy failure env FAIL_STAGE=migrate
  [[ "$(git -C "${FIXTURE_PROD}" rev-parse HEAD)" == "${FIXTURE_TARGET}" ]] || fail "migration failure did not preserve target HEAD"
  [[ "$(cat "${FIXTURE_TRACE}")" == $'build\nmigrate' ]] || fail "migration failure continued to restart"
  [[ ! -e "${FIXTURE_PROD}/build/profile" ]] || fail "migration failure installed new artifacts"
  printf 'PASS: migration failure stops before restart\n'
}

test_migration_dirties_worktree() {
  create_fixture migration-dirties
  run_deploy failure env DIRTY_STAGE=migrate
  assert_contains "${FIXTURE_OUTPUT}" "refusing to deploy a dirty worktree"
  [[ "$(cat "${FIXTURE_TRACE}")" == $'build\nmigrate' ]] || fail "dirty migration continued to restart"
  printf 'PASS: migration-created dirt rejected before restart\n'
}

test_restart_failure() {
  create_fixture restart-failure
  run_deploy failure env FAIL_STAGE=restart
  [[ "$(cat "${FIXTURE_TRACE}")" == $'build\nmigrate\nrestart' ]] || fail "restart failure continued to health check"
  printf 'PASS: restart failure stops before health check\n'
}

test_health_failure() {
  create_fixture health-failure
  run_deploy failure env FAIL_STAGE=health
  [[ "$(cat "${FIXTURE_TRACE}")" == $'build\nmigrate\nrestart\nhealth' ]] || fail "health failure trace mismatch"
  assert_contains "${FIXTURE_OUTPUT}" "failed with exit code"
  printf 'PASS: health failure returns failure\n'
}

test_rollback_target() {
  create_fixture rollback
  git -C "${FIXTURE_PROD}" fetch -q origin main
  git -C "${FIXTURE_PROD}" merge -q --ff-only origin/main
  git --git-dir="${FIXTURE_ROOT}/remote.git" update-ref refs/heads/main "${FIXTURE_BASE}"
  write_pr_response "${FIXTURE_BASE}" true
  run_deploy failure env
  assert_contains "${FIXTURE_OUTPUT}" "rollback and branch deployments are forbidden"
  printf 'PASS: rollback target rejected\n'
}

bash -n "${SOURCE_SCRIPT}"
test_success
test_dirty_tracked
test_dirty_untracked
test_wrong_branch
test_divergent_head
test_missing_pr
test_unmanaged_repository
test_build_failure
test_main_advances_during_build
test_migration_failure
test_migration_dirties_worktree
test_restart_failure
test_health_failure
test_rollback_target
printf 'All deploy-main safety drills passed.\n'
