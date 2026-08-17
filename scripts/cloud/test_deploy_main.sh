#!/bin/bash
set -euo pipefail

SOURCE_ROOT="$(cd "$(dirname "$0")/../.."; pwd)"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

# shellcheck source=deploy_main_lib.sh
source "${SOURCE_ROOT}/scripts/cloud/deploy_main_lib.sh"

grep -Fq "export PATH='/snap/go/current/bin:" "${SOURCE_ROOT}/scripts/cloud/deploy_main.sh" || {
  echo "FAIL: root-managed wrapper cannot find the production Go toolchain" >&2
  exit 1
}
echo "PASS: root-managed wrapper includes the production Go toolchain"

grep -Fq 'FRIEND_REQUEST_LIMITS_CONFIG="${POLICY_DIR}/friend_request_limits.yaml"' \
  "${SOURCE_ROOT}/scripts/cloud/install_main_deployer.sh" || {
  echo "FAIL: installer does not manage the stable friend-request limit config" >&2
  exit 1
}
grep -Fq 'elif [[ -f "${LEGACY_FRIEND_REQUEST_LIMITS_CONFIG}" ]]' \
  "${SOURCE_ROOT}/scripts/cloud/install_main_deployer.sh" || {
  echo "FAIL: installer does not migrate the legacy friend-request limit config" >&2
  exit 1
}
echo "PASS: installer preserves a stable friend-request limit config"

declare -f deploy_main_prepare_source | \
  grep -Fq 'deploy_main_git_as_user "${deploy_user}" "${deploy_home}"' || {
    echo "FAIL: source export bypasses the unprivileged deployment user" >&2
    exit 1
  }
echo "PASS: source export uses the unprivileged deployment user"

REMOTE="${TEST_ROOT}/remote.git"
REPO="${TEST_ROOT}/prod"
LOCK="${TEST_ROOT}/deploy.lock"
TRACE="${TEST_ROOT}/trace"
TEST_BIN="${TEST_ROOT}/bin"
STATE_DIR="${TEST_ROOT}/state"
RUNTIME_ENV="${TEST_ROOT}/runtime.env"
printf 'APP_ENV=test\n' > "${RUNTIME_ENV}"

deploy_main_restart_services() {
  echo restart >> "${TRACE}"
}

# macOS does not ship flock; the production unit uses the system flock binary.
mkdir -p "${TEST_BIN}"
cat > "${TEST_BIN}/flock" <<'EOF'
#!/bin/bash
[[ ! -f "${FLOCK_FAIL_FILE}" ]]
EOF
chmod +x "${TEST_BIN}/flock"
export PATH="${TEST_BIN}:${PATH}"
export FLOCK_FAIL_FILE="${TEST_ROOT}/flock-fail"

if "${SOURCE_ROOT}/scripts/cloud/deploy_main.sh" >/dev/null 2>&1; then
  echo "FAIL: uninstalled source template was executable" >&2
  exit 1
fi
echo "PASS: only the rendered root-managed wrapper can deploy"

git init --bare -q "${REMOTE}"
git init -q -b main "${REPO}"
git -C "${REPO}" config user.email test@example.com
git -C "${REPO}" config user.name test
git -C "${REPO}" remote add origin "${REMOTE}"
mkdir -p "${REPO}/scripts/common" "${REPO}/scripts/cloud"

write_fixture_scripts() {
  mkdir -p "${REPO}/scripts/common" "${REPO}/scripts/cloud"
  cat > "${REPO}/scripts/common/build.sh" <<EOF
#!/bin/bash
echo build >> "${TRACE}"
if [[ -f "${TEST_ROOT}/fail-build" ]]; then
  exit 1
fi
BUILD_ROOT="\$(cd "\$(dirname "\$0")/../.."; pwd)"
mkdir -p "\${BUILD_ROOT}/build"
for binary in profile item sort feed pm auth notification api ws pipeline cron; do
  printf '#!/bin/bash\n' > "\${BUILD_ROOT}/build/\${binary}"
  chmod +x "\${BUILD_ROOT}/build/\${binary}"
done
EOF
  cat > "${REPO}/scripts/common/migrate_up.sh" <<EOF
#!/bin/bash
SOURCE_ROOT="\$(cd "\$(dirname "\$0")/../.."; pwd)"
source "\${SOURCE_ROOT}/.env"
echo migrate >> "${TRACE}"
EOF
  cat > "${REPO}/scripts/cloud/restart_all_services.sh" <<EOF
#!/bin/bash
echo restart >> "${TRACE}"
EOF
  cat > "${REPO}/scripts/cloud/check_services.sh" <<EOF
#!/bin/bash
SOURCE_ROOT="\$(cd "\$(dirname "\$0")/../.."; pwd)"
source "\${SOURCE_ROOT}/.env"
echo health >> "${TRACE}"
EOF
}

write_fixture_scripts
printf 'build/\n.env\n' > "${REPO}/.gitignore"
git -C "${REPO}" add .
git -C "${REPO}" commit -qm initial
FIRST_SHA="$(git -C "${REPO}" rev-parse HEAD)"
git -C "${REPO}" push -qu origin main

echo second > "${REPO}/version"
git -C "${REPO}" add version
git -C "${REPO}" commit -qm second
LATEST_SHA="$(git -C "${REPO}" rev-parse HEAD)"
git -C "${REPO}" push -qu origin main

run_success() {
  : > "${TRACE}"
  deploy_main_run "${REPO}" "${LOCK}" "" "" "${REMOTE}" "${STATE_DIR}" "${RUNTIME_ENV}" "$@"
}

run_success
[[ "$(cat "${STATE_DIR}/deployed-sha")" == "${LATEST_SHA}" ]]
[[ "$(tr '\n' ' ' < "${TRACE}")" == "build migrate restart health " ]]
[[ -x "${STATE_DIR}/current/bin/api" ]]
[[ "$(LC_ALL=C ls -ld "$(readlink "${STATE_DIR}/current")" | awk '{print $1}')" == "drwxr-xr-x" ]]
[[ -f "${STATE_DIR}/current/source/scripts/cloud/check_services.sh" ]]
[[ -z "$(find "${STATE_DIR}" -maxdepth 1 -name '.current.*' -print -quit)" ]]
echo "PASS: latest origin/main deployed"

printf 'touch %q\n' "${TEST_ROOT}/env-pwned" > "${REPO}/.env"
run_success
[[ ! -e "${TEST_ROOT}/env-pwned" ]]
echo "PASS: ignored production .env is never executed as root"

git -C "${REPO}" remote set-url origin "${TEST_ROOT}/untrusted.git"
run_success
[[ "$(cat "${STATE_DIR}/deployed-sha")" == "${LATEST_SHA}" ]]
echo "PASS: mutable origin configuration is ignored"

git clone -q --bare "${REPO}" "${TEST_ROOT}/rewrite.git"
git -C "${REPO}" config "url.${TEST_ROOT}/rewrite.git.insteadOf" "${REMOTE}"
run_success
[[ "$(cat "${STATE_DIR}/deployed-sha")" == "${LATEST_SHA}" ]]
git -C "${REPO}" config --unset-all "url.${TEST_ROOT}/rewrite.git.insteadOf"
echo "PASS: URL rewrite cannot change the trusted main SHA"

echo dirty > "${REPO}/dirty.txt"
if deploy_main_run "${REPO}" "${LOCK}" "" "" "${REMOTE}" "${STATE_DIR}" "${RUNTIME_ENV}" >/dev/null 2>&1; then
  echo "FAIL: dirty worktree was accepted" >&2
  exit 1
fi
rm "${REPO}/dirty.txt"
echo "PASS: dirty worktree rejected"

mkdir -p "${REPO}/build"
printf '#!/bin/bash\n' > "${REPO}/build/obsolete"
chmod +x "${REPO}/build/obsolete"
printf 'MALICIOUS\n' > "${REPO}/build/api"
chmod +x "${REPO}/build/api"
run_success --rollback "${FIRST_SHA}"
[[ "$(cat "${STATE_DIR}/deployed-sha")" == "${FIRST_SHA}" ]]
[[ "$(tr '\n' ' ' < "${TRACE}")" == "build restart health " ]]
[[ ! -e "${STATE_DIR}/current/bin/obsolete" ]]
! grep -q MALICIOUS "${STATE_DIR}/current/bin/api"
echo "PASS: rollback accepts a commit contained in main and skips migrations"

git -C "${REPO}" checkout -q --orphan outside-main
git -C "${REPO}" rm -qrf .
write_fixture_scripts
git -C "${REPO}" add .
git -C "${REPO}" commit -qm outside
OUTSIDE_SHA="$(git -C "${REPO}" rev-parse HEAD)"
git -C "${REPO}" checkout -q --detach "${LATEST_SHA}"
if deploy_main_run "${REPO}" "${LOCK}" "" "" "${REMOTE}" "${STATE_DIR}" "${RUNTIME_ENV}" --rollback "${OUTSIDE_SHA}" >/dev/null 2>&1; then
  echo "FAIL: non-main rollback target was accepted" >&2
  exit 1
fi
echo "PASS: non-main rollback target rejected"

if deploy_main_run "${REPO}" "${LOCK}" "" "" "${REMOTE}" "${STATE_DIR}" "${RUNTIME_ENV}" "${FIRST_SHA}" >/dev/null 2>&1; then
  echo "FAIL: arbitrary deploy target was accepted" >&2
  exit 1
fi
echo "PASS: normal deployment cannot select an arbitrary target"

touch "${FLOCK_FAIL_FILE}"
if deploy_main_run "${REPO}" "${LOCK}" "" "" "${REMOTE}" "${STATE_DIR}" "${RUNTIME_ENV}" >/dev/null 2>&1; then
  echo "FAIL: concurrent deployment lock was ignored" >&2
  exit 1
fi
rm "${FLOCK_FAIL_FILE}"
echo "PASS: concurrent deployment rejected"

touch "${TEST_ROOT}/fail-build"
: > "${TRACE}"
if deploy_main_run "${REPO}" "${LOCK}" "" "" "${REMOTE}" "${STATE_DIR}" "${RUNTIME_ENV}" >/dev/null 2>&1; then
  echo "FAIL: build failure was accepted" >&2
  exit 1
fi
[[ "$(cat "${TRACE}")" == "build" ]]
echo "PASS: build failure stops before migration and restart"
